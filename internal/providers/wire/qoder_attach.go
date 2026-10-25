package wire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// shared/qoder/attachments.js: the native Qoder CLI never inlines image bytes into
// agent_chat_generation. It PUTs them to the file API and sends the returned URL, so
// a 10 MB screenshot does not become a 30 MB JSON body the gateway answers with 413.
const (
	qoderMaxImageBytes      = 10 * 1024 * 1024 // config/mediaConfig.js:4
	qoderMaxPayloadBytes    = 6 * 1024 * 1024  // constants.js:39
	qoderInlineFallbackMax  = 512 * 1024       // constants.js:41
	qoderLongTextGate       = 8192             // attachments.js:215,230
	qoderImageUploadSigPath = "/api/v2/image/upload"
)

var (
	// JavaScript's \s, which RE2's is narrower than: the same set isJSSpace trims.
	qoderDataURIText = regexp.MustCompile(`data:[^;]+;base64,[A-Za-z0-9+/=\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)
	qoderDataURI     = regexp.MustCompile(`(?s)^data:([^;]+);base64,(.+)$`)
	qoderImageMime   = regexp.MustCompile(`(?i)^image/`)
)

// QoderUpload is one image bound for Qoder's file API.
type QoderUpload struct {
	Data      []byte
	MediaType string
}

// QoderUploader returns the URL Qoder serves the image from. An error, or an empty
// URL, leaves the image inline when it is small and stubs it when it is not.
type QoderUploader func(context.Context, QoderUpload) (string, error)

// QoderAttachmentStats is what rewriteQoderMessageAttachments reports (:294).
type QoderAttachmentStats struct {
	ImageURLs []string
	Uploaded  int
	Stubbed   int
}

type attachmentContext struct {
	ctx    context.Context
	upload QoderUploader
	cache  map[string]string
}

func qoderMimeExt(mime string) string {
	m := strings.ToLower(mime)
	for _, e := range []struct{ has, ext string }{
		{"png", "png"}, {"jpeg", "jpg"}, {"jpg", "jpg"}, {"gif", "gif"}, {"webp", "webp"}, {"bmp", "bmp"}, {"pdf", "pdf"},
	} {
		if strings.Contains(m, e.has) {
			return e.ext
		}
	}
	return "bin"
}

func stripJSSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if isJSSpace(r) {
			return -1
		}
		return r
	}, s)
}

// qoderDecodedBytes is decodedBytes (:44-48): the size the base64 text stands for,
// measured on its UTF-16 length like the pin.
func qoderDecodedBytes(b64 string) int {
	if b64 == "" {
		return 0
	}
	return utf16Len(stripJSSpace(b64)) * 3 / 4
}

func qoderStub(name, mime string, bytes int, reason string) string {
	label := name
	if label == "" {
		label = mime
	}
	if label == "" {
		label = "attachment"
	}
	size := ""
	if bytes != 0 {
		size = ", " + strconv.Itoa(bytes) + " bytes"
	}
	return "[file omitted: " + label + size + " — " + reason + "]"
}

func qoderTextBlock(text string) *jsObject {
	return newJSObject().set("type", "text").set("text", text)
}

func qoderImageBlock(url string) *jsObject {
	return newJSObject().set("type", "image_url").set("image_url", newJSObject().set("url", url))
}

// nodeBase64Decode is Buffer.from(text, "base64"): both alphabets are read, anything
// else is skipped, and an "=" ends the data.
func nodeBase64Decode(text string) []byte {
	var out []byte
	var acc uint32
	bits := 0
	for i := 0; i < len(text); i++ {
		var v int
		switch c := text[i]; {
		case c >= 'A' && c <= 'Z':
			v = int(c - 'A')
		case c >= 'a' && c <= 'z':
			v = int(c-'a') + 26
		case c >= '0' && c <= '9':
			v = int(c-'0') + 52
		case c == '+' || c == '-':
			v = 62
		case c == '/' || c == '_':
			v = 63
		case c == '=':
			return out
		default:
			continue
		}
		acc = acc<<6 | uint32(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>uint(bits)))
			acc &= 1<<uint(bits) - 1
		}
	}
	return out
}

type uploadOutcome struct {
	url   string
	keep  bool
	bytes int
}

// uploadImage is uploadImageData (:123-152): a url when uploaded, keep when a small
// image stays inline, neither when it is to be stubbed.
func (a *attachmentContext) uploadImage(b64, mediaType string) *uploadOutcome {
	compact := stripJSSpace(b64)
	if compact == "" {
		return nil
	}
	size := qoderDecodedBytes(compact)
	if size > qoderMaxImageBytes {
		return &uploadOutcome{bytes: size}
	}
	data := nodeBase64Decode(compact)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if url, ok := a.cache[digest]; ok {
		return &uploadOutcome{url: url, bytes: size}
	}
	if a.upload != nil {
		if url, err := a.upload(a.ctx, QoderUpload{Data: data, MediaType: mediaType}); err == nil && url != "" {
			a.cache[digest] = url
			return &uploadOutcome{url: url, bytes: size}
		}
	}
	if size <= qoderInlineFallbackMax {
		return &uploadOutcome{keep: true, bytes: size}
	}
	return &uploadOutcome{bytes: size}
}

func parseQoderDataURI(url string) (mime, b64 string, ok bool) {
	m := qoderDataURI.FindStringSubmatch(url)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func (a *attachmentContext) stripLargeDataURIs(text, reason string) string {
	return qoderDataURIText.ReplaceAllStringFunc(text, func(m string) string {
		size := len(m)
		mime := ""
		if mt, b64, ok := parseQoderDataURI(jsTrim(m)); ok {
			size, mime = qoderDecodedBytes(b64), mt
		}
		if size <= qoderInlineFallbackMax {
			return m
		}
		return qoderStub("", mime, size, reason)
	})
}

// rewriteBlock is :158-226. A nil result drops the block.
func (a *attachmentContext) rewriteBlock(value any) any {
	block, ok := value.(*jsObject)
	if !ok {
		return value
	}
	const documents = "Qoder reads documents via its file API, not inlined bytes"
	switch block.get("type") {
	case "image_url":
		raw := qoderImageURL(block)
		if raw == "" {
			return nil
		}
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return qoderImageBlock(raw)
		}
		mime, b64, ok := parseQoderDataURI(raw)
		if !ok {
			return qoderTextBlock(qoderStub("attachment", "", 0, "unreadable data URI"))
		}
		if !qoderImageMime.MatchString(mime) {
			return qoderTextBlock(qoderStub("file", mime, qoderDecodedBytes(b64), "non-image bytes are not inlined into Qoder context"))
		}
		up := a.uploadImage(b64, mime)
		switch {
		case up != nil && up.url != "":
			return qoderImageBlock(up.url)
		case up != nil && up.keep:
			return qoderImageBlock(raw)
		}
		return qoderTextBlock(qoderStub("image", mime, upBytes(up), "upload failed; not inlined"))
	case "image":
		src := block.get("source")
		if prop(src, "type") == "url" {
			if url, ok := prop(src, "url").(string); ok {
				return qoderImageBlock(url)
			}
		}
		if prop(src, "type") == "base64" && jsIsTruthy(prop(src, "data")) {
			mime := "image/png"
			if jsIsTruthy(prop(src, "media_type")) {
				mime = jsToString(prop(src, "media_type"))
			}
			data := jsToString(prop(src, "data"))
			up := a.uploadImage(data, mime)
			switch {
			case up != nil && up.url != "":
				return qoderImageBlock(up.url)
			case up != nil && up.keep:
				return qoderImageBlock("data:" + mime + ";base64," + data)
			}
			return qoderTextBlock(qoderStub("image", mime, upBytes(up), "upload failed; not inlined"))
		}
	case "file":
		if file, ok := block.get("file").(*jsObject); ok && jsIsTruthy(file) {
			name := "file"
			if jsIsTruthy(file.get("filename")) {
				name = jsToString(file.get("filename"))
			} else if jsIsTruthy(file.get("name")) {
				name = jsToString(file.get("name"))
			}
			data, hasData := file.get("file_data").(string)
			var b64, mime string
			if hasData {
				if m, b, ok := parseQoderDataURI(data); ok {
					mime, b64 = m, b
				} else if !strings.HasPrefix(data, "data:") {
					b64 = data
				}
			}
			if mime == "" {
				mime = "application/octet-stream"
				if jsIsTruthy(file.get("format")) {
					mime = jsToString(file.get("format"))
				}
			}
			if b64 != "" && qoderImageMime.MatchString(mime) {
				if up := a.uploadImage(b64, mime); up != nil && up.url != "" {
					return qoderImageBlock(up.url)
				}
			}
			return qoderTextBlock(qoderStub(name, mime, qoderDecodedBytes(b64), documents))
		}
	case "document":
		if src := block.get("source"); jsIsTruthy(src) {
			name := "document"
			if jsIsTruthy(block.get("title")) {
				name = jsToString(block.get("title"))
			}
			if prop(src, "type") == "base64" && jsIsTruthy(prop(src, "data")) {
				mime := "application/pdf"
				if jsIsTruthy(prop(src, "media_type")) {
					mime = jsToString(prop(src, "media_type"))
				}
				data := jsToString(prop(src, "data"))
				if qoderImageMime.MatchString(mime) {
					if up := a.uploadImage(data, mime); up != nil && up.url != "" {
						return qoderImageBlock(up.url)
					}
				}
				return qoderTextBlock(qoderStub(name, mime, qoderDecodedBytes(data), documents))
			}
		}
	}
	if text, ok := block.get("text").(string); ok && strings.Contains(text, "data:") && utf16Len(text) > qoderLongTextGate {
		return block.clone().set("text", a.stripLargeDataURIs(text, "inlined data URI stripped from Qoder context"))
	}
	return block
}

func upBytes(up *uploadOutcome) int {
	if up == nil {
		return 0
	}
	return up.bytes
}

func (a *attachmentContext) rewriteContent(content any) any {
	switch c := content.(type) {
	case string:
		if strings.Contains(c, "data:") && utf16Len(c) > qoderLongTextGate {
			return a.stripLargeDataURIs(c, "inlined data URI stripped from Qoder context")
		}
		return c
	case []any:
		out := []any{}
		for _, block := range c {
			if next := a.rewriteBlock(block); next != nil {
				out = append(out, next)
			}
		}
		if len(out) == 0 {
			return ""
		}
		return out
	}
	return content
}

func qoderBlockImageURL(block any) string {
	if o, ok := block.(*jsObject); ok && o.get("type") == "image_url" {
		return qoderImageURL(o)
	}
	return ""
}

// rewriteQoderAttachments is rewriteQoderMessageAttachments (:287-331), in place.
func rewriteQoderAttachments(ctx context.Context, messages []any, upload QoderUploader) QoderAttachmentStats {
	stats := QoderAttachmentStats{}
	a := &attachmentContext{ctx: ctx, upload: upload, cache: map[string]string{}}
	for _, value := range messages {
		msg, ok := value.(*jsObject)
		if !ok {
			continue
		}
		if images, ok := msg.get("images").([]any); ok {
			extras := make([]any, len(images))
			for i, img := range images {
				extras[i] = qoderImageBlock(jsToString(img))
			}
			if list, ok := msg.get("content").([]any); ok {
				msg.set("content", append(append([]any{}, list...), extras...))
			} else {
				text, _ := msg.get("content").(string)
				msg.set("content", append([]any{qoderTextBlock(text)}, extras...))
			}
			msg.remove("images")
		}
		// The pin assigns msg.content even when it is missing, which JSON.stringify drops.
		if _, present := msg.lookup("content"); present {
			msg.set("content", a.rewriteContent(msg.get("content")))
		}
	}
	for _, value := range messages {
		msg, ok := value.(*jsObject)
		if !ok {
			continue
		}
		list, ok := msg.get("content").([]any)
		if !ok {
			continue
		}
		for _, block := range list {
			if url := qoderBlockImageURL(block); strings.HasPrefix(strings.ToLower(url), "http://") || strings.HasPrefix(strings.ToLower(url), "https://") {
				stats.ImageURLs = append(stats.ImageURLs, url)
			}
			if o, ok := block.(*jsObject); ok && o.get("type") == "text" {
				if text, ok := o.get("text").(string); ok && strings.HasPrefix(text, "[file omitted:") {
					stats.Stubbed++
				}
			}
		}
	}
	stats.Uploaded = len(stats.ImageURLs)
	if len(stringifyJS(messages)) > qoderMaxPayloadBytes {
		stripRemainingQoderDataURIs(messages)
	}
	return stats
}

// stripRemainingQoderDataURIs is :258-281.
func stripRemainingQoderDataURIs(messages []any) {
	const reason = "payload over Qoder size budget"
	strip := func(text string) string {
		return qoderDataURIText.ReplaceAllStringFunc(text, func(m string) string { return qoderStub("", "", len(m), reason) })
	}
	for _, value := range messages {
		msg, ok := value.(*jsObject)
		if !ok {
			continue
		}
		switch content := msg.get("content").(type) {
		case string:
			if strings.Contains(content, "data:") {
				msg.set("content", strip(content))
			}
		case []any:
			mapped := make([]any, len(content))
			for i, entry := range content {
				mapped[i] = entry
				block, ok := entry.(*jsObject)
				if !ok {
					continue
				}
				if block.get("type") == "image_url" && strings.HasPrefix(qoderImageURL(block), "data:") {
					mapped[i] = qoderTextBlock(qoderStub("image", "", 0, reason))
				} else if text, ok := block.get("text").(string); ok && strings.Contains(text, "data:") {
					mapped[i] = block.clone().set("text", strip(text))
				}
			}
			msg.set("content", mapped)
		}
	}
}

// ---- the file API ------------------------------------------------------------------

// QoderHTTPDoer sends one request. It is the pooled, proxy-aware transport; a Qoder
// request is signed, so it must not be replayed on a failure that may have reached
// the gateway (qoder-proxy-replay.test.js, executors/qoder.js:714-717).
type QoderHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// QoderMultipart is buildMultipartFile (:56-65): one field named "file".
func QoderMultipart(data []byte, fileName, mediaType, boundary string) []byte {
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\nContent-Type: %s\r\n\r\n", boundary, fileName, mediaType)
	b.Write(data)
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return b.Bytes()
}

// qoderUploadURLFromResponse is extractUrlFromUploadResponse (:67-83).
func qoderUploadURLFromResponse(value any) string {
	obj, ok := value.(*jsObject)
	if !ok {
		return ""
	}
	result := obj
	if r, ok := obj.get("result").(*jsObject); ok {
		result = r
	}
	for _, list := range []any{result.get("imageUrls"), result.get("image_urls"), obj.get("imageUrls"), obj.get("image_urls")} {
		if items, ok := list.([]any); ok && len(items) > 0 {
			if first, ok := items[0].(string); ok && first != "" {
				return first
			}
		}
	}
	for _, key := range []string{"imageUrl", "image_url", "url", "ossUrl", "oss_url", "originalUrl", "originUrl", "link", "image"} {
		v := result.get(key)
		if v == nil {
			v = obj.get(key)
		}
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if body, ok := obj.get("body").(string); ok {
		if inner, err := parseJS([]byte(body)); err == nil {
			return qoderUploadURLFromResponse(inner)
		}
	}
	return ""
}

// QoderImageUploader returns the default uploader (defaultUploadImage, :85-121):
// a COSY-signed multipart PUT to the file API on the host the token belongs to.
func QoderImageUploader(doer QoderHTTPDoer, creds QoderCredentials) QoderUploader {
	return qoderImageUploader(doer, creds, qoderLive, BuildCosyHeaders, qoderNonce(time.Now))
}

func qoderImageUploader(doer QoderHTTPDoer, creds QoderCredentials, env qoderEnv, sign cosySigner, nonce func() string) QoderUploader {
	return func(ctx context.Context, up QoderUpload) (string, error) {
		url := qoderInferenceBase(creds) + "/algo" + qoderImageUploadSigPath + "?request_id=" + env.uuid()
		boundary := "----9routerQoder" + nonce()
		body := QoderMultipart(up.Data, "image."+qoderMimeExt(up.MediaType), orDefault(up.MediaType, "application/octet-stream"), boundary)
		headers, err := sign(body, url, creds.cosy())
		if err != nil {
			return "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		for k, v := range headers {
			request.Header.Set(k, v)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		request.Header.Set("AI-CLIENT-TIMESTAMP", strconv.FormatInt(env.now().Unix(), 10))
		request.Header.Set("Accept-Encoding", "identity")
		request.ContentLength = int64(len(body))
		response, err := doer.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		text, _ := io.ReadAll(io.LimitReader(response.Body, qoderMaxResponseBytes))
		if response.StatusCode < 200 || response.StatusCode > 299 {
			detail := ""
			if len(text) > 0 {
				prefix, _ := sliceUTF16(string(text), 180)
				detail = ": " + prefix
			}
			return "", fmt.Errorf("HTTP %d%s", response.StatusCode, detail)
		}
		value, err := parseJS(text)
		if err != nil {
			return "", fmt.Errorf("upload response missing url")
		}
		if found := qoderUploadURLFromResponse(value); found != "" {
			return found, nil
		}
		return "", fmt.Errorf("upload response missing url")
	}
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
