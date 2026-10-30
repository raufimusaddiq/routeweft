// Package netlify provisions a Netlify relay: it creates a site, deploys the relay
// function by digest and waits for the deploy to be live, which is what the
// reference's POST /api/proxy-pools/netlify-deploy does
// (src/app/api/proxy-pools/netlify-deploy/route.js, src/lib/network/netlifyRelay.js;
// PARITY 6.38). The Netlify token comes with the request, is used only for the
// calls to Netlify, and is neither stored nor returned.
package netlify

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// API is Netlify's API root.
	API = "https://api.netlify.com/api/v1"
	// FunctionName is the function the relay is deployed as, and FunctionPath where
	// a digest deploy serves it (it runs no build step).
	FunctionName = "relay"
	FunctionPath = "/.netlify/functions/relay"

	defaultPollInterval = 3 * time.Second
	defaultPollTimeout  = 120 * time.Second
	maxAnswerBytes      = 1 << 20
)

// Error is a failure to provision, with the HTTP status the admin API answers.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Result is a deployed relay.
type Result struct {
	// Name is the (sanitized) site name, which names the pool.
	Name string
	// RelayURL is where the relay function is served.
	RelayURL string
}

// Client talks to Netlify. The zero value is not usable: set HTTP.
type Client struct {
	// API overrides the API root (tests); empty means Netlify's.
	API  string
	HTTP *http.Client
	// PollInterval and PollTimeout bound the wait for a live deploy (3 s and 120 s
	// in the reference); Now and Sleep are replaceable for tests.
	PollInterval, PollTimeout time.Duration
	Now                       func() time.Time
	Sleep                     func(context.Context, time.Duration) error
}

func (c Client) root() string {
	if c.API != "" {
		return strings.TrimRight(c.API, "/")
	}
	return API
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var (
	nonSiteName = regexp.MustCompile(`[^a-z0-9-]`)
	dashRuns    = regexp.MustCompile(`-+`)
)

// SiteName is the Netlify site name for a requested one: lower case, anything but
// letters, digits and hyphens turned into a hyphen, runs collapsed, the ends
// trimmed. An empty or unusable name becomes "relay-" and the time in base 36
// (route.js:13-19). requested is "" when the client sent no usable string.
func SiteName(requested string, now time.Time) string {
	name := strings.ToLower(strings.TrimSpace(requested))
	if name != "" {
		name = strings.Trim(dashRuns.ReplaceAllString(nonSiteName.ReplaceAllString(name, "-"), "-"), "-")
	}
	if name == "" {
		return "relay-" + strconv.FormatInt(now.UnixMilli(), 36)
	}
	return name
}

// RelayURL is where a site serves the relay function.
func RelayURL(site string) string { return strings.TrimRight(site, "/") + FunctionPath }

// Provision deploys a relay named name and returns where it is served. Every
// failure is an *Error carrying the status and message the reference answers with.
func (c Client) Provision(ctx context.Context, token, name string) (Result, *Error) {
	function, functionSum := RelayFunctionZip()
	index, indexSum := IndexFile()

	// 1. Create the site (a taken name is 422 at Netlify, 409 here).
	status, body, err := c.call(ctx, token, http.MethodPost, "/sites", "application/json", mustJSON(map[string]string{"name": name}))
	if err != nil {
		return Result{}, c.failure(err, token)
	}
	if !ok(status) {
		message := answerMessage(body, "Failed to create Netlify site")
		hint := ""
		if status == http.StatusUnprocessableEntity {
			status = http.StatusConflict
			hint = fmt.Sprintf(" Site name %q is taken — choose a different name.", name)
		}
		return Result{}, &Error{Status: status, Message: message + "." + hint}
	}
	var site struct {
		ID      string `json:"id"`
		SiteID  string `json:"site_id"`
		SSLURL  string `json:"ssl_url"`
		URL     string `json:"url"`
		Deploys string `json:"-"`
	}
	_ = json.Unmarshal(body, &site)
	siteID, siteURL := first(site.ID, site.SiteID), first(site.SSLURL, site.URL)
	if siteID == "" || siteURL == "" {
		return Result{}, &Error{Status: http.StatusBadGateway, Message: "Netlify site created but no site URL returned"}
	}

	// 2. Digest deploy: index.html by SHA-1, the function bundle by SHA-256.
	request := mustJSON(map[string]any{"files": map[string]string{"/index.html": indexSum}, "functions": map[string]string{FunctionName: functionSum}})
	status, body, err = c.call(ctx, token, http.MethodPost, "/sites/"+siteID+"/deploys", "application/json", request)
	if err != nil {
		return Result{}, c.failure(err, token)
	}
	if !ok(status) {
		return Result{}, &Error{Status: status, Message: answerMessage(body, "Failed to create Netlify deploy")}
	}
	var deploy struct {
		ID                string   `json:"id"`
		Required          []string `json:"required"`
		RequiredFunctions []string `json:"required_functions"`
	}
	_ = json.Unmarshal(body, &deploy)
	if deploy.ID == "" {
		return Result{}, &Error{Status: http.StatusBadGateway, Message: "Netlify deploy created but no deploy ID returned"}
	}

	// 3. Upload only what Netlify asks for.
	if contains(deploy.Required, indexSum) {
		status, body, err = c.call(ctx, token, http.MethodPut, "/deploys/"+deploy.ID+"/files/index.html", "application/octet-stream", index)
		if err != nil {
			return Result{}, c.failure(err, token)
		}
		if !ok(status) {
			return Result{}, &Error{Status: status, Message: answerMessage(body, "Failed to upload site file to Netlify")}
		}
	}
	if contains(deploy.RequiredFunctions, functionSum) {
		status, body, err = c.call(ctx, token, http.MethodPut, "/deploys/"+deploy.ID+"/functions/"+FunctionName+"?runtime=js", "application/octet-stream", function)
		if err != nil {
			return Result{}, c.failure(err, token)
		}
		if !ok(status) {
			return Result{}, &Error{Status: status, Message: answerMessage(body, "Failed to upload relay function to Netlify")}
		}
	}

	// 4. Wait until the deploy is live.
	if failure := c.waitReady(ctx, token, deploy.ID); failure != nil {
		return Result{}, failure
	}
	return Result{Name: name, RelayURL: RelayURL(siteURL)}, nil
}

func (c Client) waitReady(ctx context.Context, token, deployID string) *Error {
	interval, limit := c.PollInterval, c.PollTimeout
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if limit <= 0 {
		limit = defaultPollTimeout
	}
	started := c.now()
	for {
		status, body, err := c.call(ctx, token, http.MethodGet, "/deploys/"+deployID, "", nil)
		if err != nil {
			return c.failure(err, token)
		}
		if !ok(status) {
			return &Error{Status: http.StatusInternalServerError, Message: fmt.Sprintf("Netlify status failed (%d)", status)}
		}
		var state struct {
			State        string `json:"state"`
			ErrorMessage string `json:"error_message"`
		}
		_ = json.Unmarshal(body, &state)
		switch state.State {
		case "ready":
			return nil
		case "error":
			return &Error{Status: http.StatusInternalServerError, Message: first(state.ErrorMessage, "Netlify deploy failed")}
		}
		if c.now().Sub(started) > limit {
			return &Error{Status: http.StatusInternalServerError, Message: "Netlify deploy timed out"}
		}
		if err := c.sleep(ctx, interval); err != nil {
			return c.failure(err, token)
		}
	}
}

// call makes one Netlify request. The token is sent only here, as a bearer
// credential, with the reference's User-Agent.
func (c Client) call(ctx context.Context, token, method, path, contentType string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.root()+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "9Router")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	return response.StatusCode, answer, err
}

// failure turns a transport error into the 500 the reference answers with, after
// making sure the token cannot be in the text.
func (c Client) failure(err error, token string) *Error {
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	if message == "" {
		message = "Deploy failed"
	}
	return &Error{Status: http.StatusInternalServerError, Message: message}
}

// answerMessage is a Netlify error body's message, else its error, else the
// fallback (route.js:21-24).
func answerMessage(body []byte, fallback string) string {
	var decoded struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &decoded)
	return first(decoded.Message, decoded.Error, fallback)
}

func ok(status int) bool { return status >= 200 && status < 300 }

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// IndexFile is the placeholder page and its SHA-1.
func IndexFile() (content []byte, sha1Hex string) {
	content = []byte(IndexHTML)
	sum := sha1.Sum(content)
	return content, hex.EncodeToString(sum[:])
}

// RelayFunctionZip is the one-file archive Netlify takes for a js function, and its
// SHA-256.
func RelayFunctionZip() (archive []byte, sha256Hex string) {
	archive = StoredZip([]ZipFile{{Name: FunctionName + ".js", Data: []byte(RelayFunctionCode)}})
	sum := sha256.Sum256(archive)
	return archive, hex.EncodeToString(sum[:])
}

// ZipFile is one entry of a stored archive.
type ZipFile struct {
	Name string
	Data []byte
}

// StoredZip writes an uncompressed ZIP byte for byte as the reference does
// (netlifyRelay.js:133-179): UTF-8 names, no timestamps, version 20, stored.
func StoredZip(files []ZipFile) []byte {
	var out, central bytes.Buffer
	offset := 0
	for _, file := range files {
		name := []byte(file.Name)
		sum := crc32.ChecksumIEEE(file.Data)
		local := make([]byte, 30)
		binary.LittleEndian.PutUint32(local[0:], 0x04034b50)
		binary.LittleEndian.PutUint16(local[4:], 20)
		binary.LittleEndian.PutUint16(local[6:], 0x0800)
		binary.LittleEndian.PutUint32(local[14:], sum)
		binary.LittleEndian.PutUint32(local[18:], uint32(len(file.Data)))
		binary.LittleEndian.PutUint32(local[22:], uint32(len(file.Data)))
		binary.LittleEndian.PutUint16(local[26:], uint16(len(name)))
		out.Write(local)
		out.Write(name)
		out.Write(file.Data)

		header := make([]byte, 46)
		binary.LittleEndian.PutUint32(header[0:], 0x02014b50)
		binary.LittleEndian.PutUint16(header[8:], 0x0800)
		binary.LittleEndian.PutUint32(header[16:], sum)
		binary.LittleEndian.PutUint32(header[20:], uint32(len(file.Data)))
		binary.LittleEndian.PutUint32(header[24:], uint32(len(file.Data)))
		binary.LittleEndian.PutUint16(header[28:], uint16(len(name)))
		binary.LittleEndian.PutUint32(header[42:], uint32(offset))
		central.Write(header)
		central.Write(name)
		offset += 30 + len(name) + len(file.Data)
	}
	end := make([]byte, 22)
	binary.LittleEndian.PutUint32(end[0:], 0x06054b50)
	binary.LittleEndian.PutUint16(end[8:], uint16(len(files)))
	binary.LittleEndian.PutUint16(end[10:], uint16(len(files)))
	binary.LittleEndian.PutUint32(end[12:], uint32(central.Len()))
	binary.LittleEndian.PutUint32(end[16:], uint32(offset))
	out.Write(central.Bytes())
	out.Write(end)
	return out.Bytes()
}

// Request is the body of POST /admin/v1/proxy-pools/netlify-deploy. The fields are
// kept raw because the reference reads them loosely: a name that is not a string
// is "no name", and projectName, when present at all, wins over siteName
// (`body.projectName ?? body.siteName`).
type Request struct {
	NetlifyToken json.RawMessage `json:"netlifyToken"`
	ProjectName  json.RawMessage `json:"projectName"`
	SiteName     json.RawMessage `json:"siteName"`
}

// Token is the trimmed Netlify token, or "" when none was sent (or it is not a
// string).
func (r Request) Token() string {
	var token string
	if json.Unmarshal(r.NetlifyToken, &token) != nil {
		return ""
	}
	return strings.TrimSpace(token)
}

// RequestedName is the site name asked for, or "" when none that is usable.
func (r Request) RequestedName() string {
	raw := r.ProjectName
	if len(raw) == 0 || string(raw) == "null" {
		raw = r.SiteName
	}
	var name string
	if json.Unmarshal(raw, &name) != nil {
		return ""
	}
	return name
}
