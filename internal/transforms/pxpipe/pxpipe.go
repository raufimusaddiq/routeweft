// Package pxpipe owns the PXPIPE large/image-heavy context transform
// (SPEC §17.4). It runs last in the token-saver pipeline slot after Ponytail
// and before prompt-cache anchoring (BDR-012).
package pxpipe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const (
	defaultMinChars        = 25000
	defaultTimeout         = 15 * time.Second
	defaultMaxResponseSize = 32 << 20
)

var defaultClient = transport.NewSSRFProtectedClient()

// Client is the HTTP surface PXPIPE needs; tests inject a deterministic fake.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Transform is the host side of PXPIPE (open-sse/rtk/pxpipe.js, PARITY 6.37): it
// decides whether a Claude-format body is worth imaging, hands the whole body to
// the configured helper, which renders bulky context as images, and reads the
// answer. It runs on the final Anthropic Messages body, after the text savers, and
// fails open: any refusal, error or timeout leaves the body as it was.
type Transform struct {
	AllowPrivate bool
	Enabled      bool
	// URL is the helper endpoint that takes {body, model, options} and answers
	// {applied, reason, detail, body, info, cache} (see Compress). Empty means no
	// helper is installed: the reference's "not_installed".
	URL              string
	MinChars         int
	Timeout          time.Duration
	MaxResponseBytes int64
	Client           Client
	Diagnostics      func(Diagnostic)
}

// Diagnostic is the request-detail record for one PXPIPE attempt.
type Diagnostic struct {
	Step        string        `json:"step"`
	Status      string        `json:"status"`
	Skipped     bool          `json:"skipped"`
	Attempted   bool          `json:"attempted"`
	Succeeded   bool          `json:"succeeded"`
	InputChars  int           `json:"input_chars"`
	OutputChars int           `json:"output_chars"`
	Duration    time.Duration `json:"duration"`
	Error       string        `json:"error,omitempty"`
	// Summary is the reference's pxpipe summary for the attempt.
	Summary *Summary `json:"summary,omitempty"`
}

// Stats is the cumulative process-local counter set exposed by the control API.
type Stats struct {
	Attempts  uint64 `json:"attempts"`
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
	Skipped   uint64 `json:"skipped"`
}

type counters struct{ attempts, succeeded, failed, skipped atomic.Uint64 }

// Service holds the transform plus the health/log/stats control state.
type Service struct {
	Transform Transform
	counters  counters
	logMu     sync.Mutex
	log       []Diagnostic
	logLimit  int
}

func (Transform) Name() string { return "pxpipe" }

func (t Transform) client() Client {
	if t.Client != nil {
		return t.Client
	}
	return defaultClient
}

func validateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("pxpipe URL must be an HTTP(S) URL without user info, query or fragment")
	}
	return parsed, nil
}

// NewService wires a transform to the control-plane state (stats/log/health).
func NewService(transform Transform) *Service {
	service := &Service{Transform: transform, logLimit: 100}
	service.Transform.Diagnostics = service.observe
	return service
}

// Observe records one diagnostic. It is safe for concurrent use and is the
// process-level sink that compiled pipelines report into.
func (s *Service) Observe(diagnostic Diagnostic) { s.observe(diagnostic) }

func (s *Service) observe(diagnostic Diagnostic) {
	switch {
	case diagnostic.Skipped:
		s.counters.skipped.Add(1)
	case diagnostic.Succeeded:
		s.counters.attempts.Add(1)
		s.counters.succeeded.Add(1)
	default:
		s.counters.attempts.Add(1)
		s.counters.failed.Add(1)
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.log = append(s.log, diagnostic)
	if len(s.log) > s.logLimit {
		s.log = s.log[len(s.log)-s.logLimit:]
	}
}

// Stats returns cumulative counters for the control API.
func (s *Service) Stats() Stats {
	return Stats{Attempts: s.counters.attempts.Load(), Succeeded: s.counters.succeeded.Load(), Failed: s.counters.failed.Load(), Skipped: s.counters.skipped.Load()}
}

// Logs returns a copy of the bounded diagnostic log, oldest first.
func (s *Service) Logs() []Diagnostic {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return append([]Diagnostic(nil), s.log...)
}

// Health reports service availability through the transform's probe.
func (s *Service) Health(ctx context.Context) error { return s.Transform.Health(ctx) }

// Health probes the PXPIPE service health endpoint.
func (t Transform) Health(ctx context.Context) error {
	endpoint, err := validateURL(t.URL)
	if err != nil {
		return err
	}
	validate := transport.ValidatePublicURL
	if t.AllowPrivate {
		validate = transport.ValidateTrustedLocalURL
	}
	if _, err := validate(endpoint.String()); err != nil {
		return err
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/health"
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	response, err := t.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &transforms.HealthStatusError{Service: "pxpipe", StatusCode: response.StatusCode}
	}
	return nil
}

// Summary is what the reference reports for one PXPIPE attempt (compressWithPxpipe):
// why it did or did not apply and, when it did, an estimate of the tokens saved.
// The estimate is the reference's: bytes are not tokens once text becomes images,
// so the after-figure is the text left plus the images' own token cost, and the
// usage the provider bills stays the ground truth.
type Summary struct {
	Applied bool   `json:"applied"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail,omitempty"`

	OriginalChars       int     `json:"originalChars,omitempty"`
	Threshold           int     `json:"threshold,omitempty"`
	CompressedBodyChars int     `json:"compressedBodyChars,omitempty"`
	ImagedChars         int     `json:"imagedChars,omitempty"`
	ImageCount          int     `json:"imageCount,omitempty"`
	ImageBytes          int     `json:"imageBytes,omitempty"`
	TokensBeforeEst     int     `json:"tokensBeforeEst,omitempty"`
	TokensAfterEst      int     `json:"tokensAfterEst,omitempty"`
	TokensSavedEst      int     `json:"tokensSavedEst,omitempty"`
	SavedPct            float64 `json:"savedPct,omitempty"`
	DurationMs          int64   `json:"durationMs,omitempty"`
	CacheOwnsControl    bool    `json:"cacheOwnsControl,omitempty"`
}

const (
	// estCharsPerToken is PXPIPE's own profitability assumption (pxpipe.js:10).
	estCharsPerToken = 4
	// tokensPerImage is the reference's estimate when a helper reports neither
	// image tokens nor pixels (pxpipe.js:76).
	tokensPerImage = 4761
	pixelsPerToken = 750
)

// helperRequest and helperAnswer are the wire between Routeweft and the helper. They
// carry what the reference's in-process call carries (transformAnthropicMessages
// takes {body: bytes, model, options: {minCompressChars}} and answers {applied,
// reason, detail, body: bytes, info, cache}), with the byte arrays base64-encoded.
type helperRequest struct {
	Body    string        `json:"body"`
	Model   string        `json:"model,omitempty"`
	Options helperOptions `json:"options"`
}

type helperOptions struct {
	MinCompressChars int `json:"minCompressChars"`
}

type helperAnswer struct {
	Applied bool            `json:"applied"`
	Reason  string          `json:"reason"`
	Detail  json.RawMessage `json:"detail"`
	Body    string          `json:"body"`
	Info    struct {
		CompressedChars int     `json:"compressedChars"`
		ImageCount      int     `json:"imageCount"`
		ImageBytes      int     `json:"imageBytes"`
		ImagePixels     float64 `json:"imagePixels"`
		ImageTokens     float64 `json:"imageTokens"`
		BaselineTokens  float64 `json:"baselineTokens"`
	} `json:"info"`
	Cache struct {
		OwnsCacheControl bool `json:"ownsCacheControl"`
	} `json:"cache"`
}

// Compress sends a Claude-format body to the helper and returns the body to use
// and the attempt's summary. The body is returned unchanged unless the summary says
// it applied. Nothing in the answer is trusted beyond being a JSON object.
func (t Transform) Compress(parent context.Context, protocol, model string, raw []byte) ([]byte, Summary) {
	if !t.Enabled {
		return raw, Summary{Reason: "disabled"}
	}
	out, summary, attempted := t.run(parent, protocol, model, raw)
	t.report(diagnosticOf(summary, attempted))
	return out, summary
}

func skipped(reason string) Summary { return Summary{Reason: reason} }

func (t Transform) run(parent context.Context, protocol, model string, raw []byte) ([]byte, Summary, bool) {
	if strings.TrimSpace(t.URL) == "" {
		return raw, skipped("not_installed"), false
	}
	if len(raw) == 0 {
		return raw, skipped("missing_body"), false
	}
	if protocol != anthropic.MessagesProtocol {
		summary := skipped("unsupported_format")
		summary.Detail = protocol
		return raw, summary, false
	}
	started := time.Now()
	originalChars := jsLength(raw)
	threshold := t.MinChars
	if threshold <= 0 {
		threshold = defaultMinChars
	}
	if originalChars < threshold {
		summary := skipped("below_threshold")
		summary.OriginalChars, summary.Threshold = originalChars, threshold
		return raw, summary, false
	}
	fail := func(reason, detail string) ([]byte, Summary, bool) {
		summary := skipped(reason)
		summary.Detail = detail
		summary.OriginalChars = originalChars
		summary.DurationMs = time.Since(started).Milliseconds()
		return raw, summary, true
	}
	endpoint, err := validateURL(t.URL)
	if err != nil {
		return fail("transform_error", err.Error())
	}
	validate := transport.ValidatePublicURL
	if t.AllowPrivate {
		validate = transport.ValidateTrustedLocalURL
	}
	if _, err := validate(endpoint.String()); err != nil {
		return fail("transform_error", err.Error())
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	payload, err := json.Marshal(helperRequest{Body: base64.StdEncoding.EncodeToString(raw), Model: model, Options: helperOptions{MinCompressChars: threshold}})
	if err != nil {
		return fail("transform_error", "request could not be encoded")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return fail("transform_error", "request could not be built")
	}
	request.Header.Set("Content-Type", "application/json")
	result, err := t.client().Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail("timeout", "")
		}
		if parent.Err() != nil {
			return fail("transform_error", "request canceled")
		}
		return fail("transform_error", "PXPIPE request failed")
	}
	defer result.Body.Close()
	maxBytes := t.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseSize
	}
	answerBody, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail("timeout", "")
		}
		return fail("transform_error", "PXPIPE response read failed")
	}
	if int64(len(answerBody)) > maxBytes {
		return fail("transform_error", "pxpipe response exceeds configured byte limit")
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fail("transform_error", fmt.Sprintf("pxpipe returned HTTP %d", result.StatusCode))
	}
	var answer helperAnswer
	if err := json.Unmarshal(answerBody, &answer); err != nil {
		return fail("transform_error", "pxpipe returned invalid JSON")
	}
	if !answer.Applied {
		reason := answer.Reason
		if reason == "" {
			reason = "passthrough"
		}
		summary := skipped(reason)
		summary.Detail = detailText(answer.Detail)
		summary.OriginalChars = originalChars
		summary.DurationMs = time.Since(started).Milliseconds()
		return raw, summary, true
	}
	rewritten, err := base64.StdEncoding.DecodeString(answer.Body)
	if err != nil {
		return fail("transform_error", "pxpipe returned a body that is not base64")
	}
	if trimmed := bytes.TrimSpace(rewritten); len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fail("transform_error", "pxpipe returned a body that is not a JSON object")
	}
	info := answer.Info
	imageTokens := info.ImageTokens
	if imageTokens == 0 {
		if info.ImagePixels != 0 {
			imageTokens = jsRound(info.ImagePixels / pixelsPerToken)
		} else {
			imageTokens = float64(info.ImageCount * tokensPerImage)
		}
	}
	summary := Summary{
		Applied: true, Reason: "applied",
		OriginalChars:       originalChars,
		CompressedBodyChars: jsLength(rewritten),
		ImagedChars:         info.CompressedChars,
		ImageCount:          info.ImageCount,
		ImageBytes:          info.ImageBytes,
		TokensBeforeEst:     int(info.BaselineTokens),
		CacheOwnsControl:    answer.Cache.OwnsCacheControl,
	}
	if info.BaselineTokens == 0 {
		summary.TokensBeforeEst = estimateTokens(originalChars)
	}
	summary.TokensAfterEst = estimateTokens(max(0, originalChars-info.CompressedChars)) + int(imageTokens)
	summary.TokensSavedEst = max(0, summary.TokensBeforeEst-summary.TokensAfterEst)
	if summary.TokensBeforeEst > 0 {
		summary.SavedPct = toFixed2(float64(summary.TokensSavedEst) / float64(summary.TokensBeforeEst) * 100)
	}
	summary.DurationMs = time.Since(started).Milliseconds()
	return rewritten, summary, true
}

func estimateTokens(chars int) int { return int(jsRound(float64(chars) / estCharsPerToken)) }

// FormatLog is the reference's one-line account of an applied attempt
// (formatPxpipeLog); it is empty for an attempt that did not apply.
func FormatLog(s Summary) string {
	if !s.Applied {
		return ""
	}
	return fmt.Sprintf("imaged %dch → %d image(s) | est %d→%d tokens (-%s%%) | %dms", s.ImagedChars, s.ImageCount, s.TokensBeforeEst, s.TokensAfterEst, trimFloat(s.SavedPct), s.DurationMs)
}

func trimFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func detailText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

// diagnosticOf maps a summary to the record the control plane keeps: an applied
// attempt succeeded; one the helper or the gates declined is skipped; a timeout or
// a helper failure failed.
func diagnosticOf(s Summary, attempted bool) Diagnostic {
	d := Diagnostic{Step: "pxpipe", Attempted: attempted, InputChars: s.OriginalChars, OutputChars: s.CompressedBodyChars,
		Duration: time.Duration(s.DurationMs) * time.Millisecond, Summary: &s}
	switch {
	case s.Applied:
		d.Status, d.Succeeded = "succeeded", true
	case s.Reason == "timeout" || s.Reason == "transform_error":
		d.Status = "failed"
		d.Error = s.Reason
		if s.Detail != "" {
			d.Error = s.Detail
		}
	default:
		d.Status, d.Skipped = "skipped", true
	}
	return d
}

func (t Transform) report(diagnostic Diagnostic) {
	if t.Diagnostics != nil {
		t.Diagnostics(diagnostic)
	}
}

// jsLength is JSON.stringify(JSON.parse(raw)).length: the compact encoding's length
// in UTF-16 code units, which is what the reference's thresholds and figures count.
func jsLength(raw []byte) int {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return utf16Length(string(raw))
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(canonicalNumbers(value)); err != nil {
		return utf16Length(string(raw))
	}
	return utf16Length(strings.TrimSuffix(out.String(), "\n"))
}

// utf16Length counts UTF-16 units of compact JSON. Go writes U+2028 and U+2029 as
// six-character escapes where JSON.stringify writes the character itself.
func utf16Length(text string) int {
	total := 0
	for i := 0; i < len(text); {
		if text[i] == '\\' && i+1 < len(text) {
			if text[i+1] == 'u' && i+6 <= len(text) && (text[i+2:i+6] == "2028" || text[i+2:i+6] == "2029") {
				total++
				i += 6
				continue
			}
			if text[i+1] == 'u' {
				total += 6
				i += 6
				continue
			}
			total += 2
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		total++
		if r >= 0x10000 { // a surrogate pair
			total++
		}
		i += size
	}
	return total
}

// jsRound is Math.round: halves round up.
func jsRound(value float64) float64 { return math.Floor(value + 0.5) }

// toFixed2 is +value.toFixed(2): the multiple of 0.01 nearest the exact value, the
// larger one on a tie.
func toFixed2(value float64) float64 {
	exact := new(big.Rat)
	if exact.SetFloat64(value) == nil {
		return value
	}
	scaled := new(big.Rat).Mul(exact, big.NewRat(100, 1))
	scaled.Add(scaled, big.NewRat(1, 2))
	n := new(big.Int).Div(scaled.Num(), scaled.Denom()) // floor for the non-negative values used here
	result, _ := strconv.ParseFloat(fmt.Sprintf("%s.%02d", new(big.Int).Div(n, big.NewInt(100)), new(big.Int).Mod(n, big.NewInt(100)).Int64()), 64)
	return result
}

// canonicalNumbers writes every number the way JSON.stringify would after a parse
// (1.50 is 1.5, 1e2 is 100), so lengths do not depend on how the client spelled them.
func canonicalNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil || math.IsInf(parsed, 0) {
			return typed
		}
		return json.Number(jsNumber(parsed))
	case map[string]any:
		for key, item := range typed {
			typed[key] = canonicalNumbers(item)
		}
	case []any:
		for i, item := range typed {
			typed[i] = canonicalNumbers(item)
		}
	}
	return value
}

// jsNumber is Number.prototype.toString for a finite number: plain decimal from
// 1e-6 up to (not including) 1e21, exponent form outside that range.
func jsNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	magnitude := math.Abs(value)
	if magnitude >= 1e-6 && magnitude < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	text := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponent, _ := strings.Cut(text, "e")
	sign := exponent[:1]
	digits := strings.TrimLeft(exponent[1:], "0")
	return mantissa + "e" + sign + digits
}
