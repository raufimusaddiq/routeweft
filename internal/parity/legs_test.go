package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/commandcode"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// legsPin is compat/parity/generate-protocol-legs.mjs's recording of the oracle's
// own conversion legs over the corpus in compat/parity/legs-corpus.mjs (#274).
type legsPin struct {
	Requests []legRecord `json:"requests"`
}

type legRecord struct {
	Leg    string         `json:"leg"`
	Case   string         `json:"case"`
	Stream bool           `json:"stream"`
	Input  map[string]any `json:"input"`
	Output any            `json:"output"`
	Throws string         `json:"throws"`
}

func loadLegsPin(t *testing.T) legsPin {
	t.Helper()
	raw, err := os.ReadFile("testdata/legs_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin legsPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	return pin
}

const (
	testModel     = "x"
	testProject   = "proj-1"
	testSession   = "1234567890"
	testProfile   = "arn:aws:codewhisperer:us-east-1:000000000000:profile/SYNTHETIC"
	conversationA = "00000000-0000-4000-8000-000000000001"
)

// requestAdapters run Routeweft's side of each request leg on the same input the
// oracle's leg was given. They call the leg itself, not the pipeline around it:
// the oracle's recording is of the registered function.
var requestAdapters = map[string]func(input map[string]any, stream bool) (any, error){
	"openai>claude": func(in map[string]any, stream bool) (any, error) {
		out, _, err := anthropic.ChatToMessages(in, testModel, stream)
		return out, err
	},
	"claude>openai": func(in map[string]any, stream bool) (any, error) {
		return anthropic.MessagesToChat(in, testModel, stream)
	},
	"openai>openai-responses": func(in map[string]any, stream bool) (any, error) {
		raw, _ := json.Marshal(in)
		request, err := openai.ParseChatRequest(raw)
		if err != nil {
			return nil, err
		}
		return decode(openai.ChatToResponses(request))
	},
	"openai-responses>openai": func(in map[string]any, stream bool) (any, error) {
		raw, _ := json.Marshal(in)
		request, err := openai.ParseResponsesRequest(raw, false)
		if err != nil {
			return nil, err
		}
		return decode(openai.ResponsesToChat(request))
	},
	"openai>gemini": func(in map[string]any, stream bool) (any, error) {
		out, _, err := gemini.ChatRequestToGemini(in, gemini.RequestOptions{Model: testModel})
		return decode(out, err)
	},
	"openai>vertex": func(in map[string]any, stream bool) (any, error) {
		out, _, err := gemini.ChatRequestToGemini(in, gemini.RequestOptions{Model: testModel, Vertex: true})
		return decode(out, err)
	},
	"openai>gemini-cli": func(in map[string]any, stream bool) (any, error) {
		out, _, err := gemini.ChatRequestToGeminiCLI(in, gemini.RequestOptions{Model: testModel}, gemini.CloudCodeOptions{Project: testProject, SessionID: testSession, RequestID: "req-1"})
		return decode(out, err)
	},
	"openai>antigravity": func(in map[string]any, stream bool) (any, error) {
		out, _, err := gemini.ChatRequestToAntigravity(in, gemini.RequestOptions{Model: testModel}, gemini.CloudCodeOptions{Project: testProject, SessionID: testSession, RequestID: "req-1"})
		return decode(out, err)
	},
	"gemini>openai": func(in map[string]any, stream bool) (any, error) {
		return decode(gemini.GeminiRequestToChat(in, testModel, stream))
	},
	"gemini-cli>openai": func(in map[string]any, stream bool) (any, error) {
		return decode(gemini.GeminiCLIRequestToChat(in, testModel, stream))
	},
	"antigravity>openai": func(in map[string]any, stream bool) (any, error) {
		return decode(gemini.AntigravityRequestToChat(in, testModel, stream))
	},
	"openai>ollama": func(in map[string]any, stream bool) (any, error) {
		return decode(ollama.ChatRequestToOllama(in, testModel, stream, false))
	},
	"openai>cursor": func(in map[string]any, stream bool) (any, error) {
		return decode(cursor.ChatRequestToCursor(in, testModel, stream, false))
	},
	"openai>commandcode": func(in map[string]any, stream bool) (any, error) {
		// The oracle writes today's UTC date and its recording ran on a frozen clock, so the
		// conversion runs on that clock too; a wall clock would make the compared date change
		// with the day the test runs.
		defer commandcode.SetClock(commandcodeClock)()
		return decode(commandcode.ChatRequestToCommandCode(in, testModel, stream, false))
	},
	"openai>kiro": func(in map[string]any, stream bool) (any, error) {
		out, _, _, err := kiro.ChatRequestToKiro(in, kiro.RequestOptions{Model: testModel, NewConversationID: func() string { return conversationA }})
		return decode(out, err)
	},
	"claude>kiro": func(in map[string]any, stream bool) (any, error) {
		out, _, _, err := kiro.MessagesRequestToKiro(in, kiro.RequestOptions{Model: testModel, NewConversationID: func() string { return conversationA }})
		return decode(out, err)
	},
}

func decode(raw []byte, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("leg output is not JSON: %w", err)
	}
	return out, nil
}

// oracleInstant is the instant the oracle's recording ran at: compat/parity/generate-protocol-legs.mjs
// freezes Date to 1700000000000 ms (FixedDate, line 41).
var oracleInstant = time.UnixMilli(1700000000000)

// commandcodeClock is the clock the openai>commandcode leg runs on; a test may move it.
var commandcodeClock = func() time.Time { return oracleInstant }

// legDiff is one leaf difference between the oracle's output and Routeweft's.
type legDiff struct {
	Path   string
	Kind   string // changed | only-oracle | only-routeweft | go-error | oracle-error
	Oracle any
	Go     any
}

var (
	uuidPattern    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	indexPattern   = regexp.MustCompile(`\[\d+\]`)
	volatileFields = map[string]bool{"sessionId": true, "requestId": true, "threadId": true, "conversationId": true}
)

// normalize makes a value comparable: a generated id is not a difference.
func normalize(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = normalize(k, v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = normalize(key, v)
		}
		return out
	case string:
		if volatileFields[key] {
			return "<generated>"
		}
		return uuidPattern.ReplaceAllString(typed, "<uuid>")
	case float64:
		if volatileFields[key] {
			return "<generated>"
		}
	}
	return value
}

func diffValues(path string, oracle, got any, out *[]legDiff) {
	switch o := oracle.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, legDiff{path, "changed", oracle, got})
			return
		}
		keys := map[string]bool{}
		for k := range o {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			child := path + "." + k
			ov, inO := o[k]
			gv, inG := g[k]
			switch {
			case inO && !inG:
				*out = append(*out, legDiff{child, "only-oracle", ov, nil})
			case !inO && inG:
				*out = append(*out, legDiff{child, "only-routeweft", nil, gv})
			default:
				diffValues(child, ov, gv, out)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			*out = append(*out, legDiff{path, "changed", oracle, got})
			return
		}
		for i := 0; i < len(o) || i < len(g); i++ {
			child := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case i >= len(g):
				*out = append(*out, legDiff{child, "only-oracle", o[i], nil})
			case i >= len(o):
				*out = append(*out, legDiff{child, "only-routeweft", nil, g[i]})
			default:
				diffValues(child, o[i], g[i], out)
			}
		}
	default:
		if fmt.Sprint(oracle) != fmt.Sprint(got) || fmt.Sprintf("%T", oracle) != fmt.Sprintf("%T", got) {
			*out = append(*out, legDiff{path, "changed", oracle, got})
		}
	}
}

// runRequestLeg returns every difference for one recorded case.
func runRequestLeg(record legRecord) []legDiff {
	adapter, ok := requestAdapters[record.Leg]
	if !ok {
		return []legDiff{{"$", "go-error", nil, "no adapter for leg " + record.Leg}}
	}
	// Bodies reach a leg as the request path decodes them (numbers kept exact).
	raw, _ := json.Marshal(record.Input)
	input, err := concerns.WireObject(raw)
	if err != nil {
		return []legDiff{{"$", "go-error", nil, "input: " + err.Error()}}
	}
	output, err := adapter(input, record.Stream)
	if err == nil {
		// Compare as JSON: an int and a float64 of the same value are the same number.
		encoded, marshalErr := json.Marshal(output)
		if marshalErr == nil {
			output = nil
			err = json.Unmarshal(encoded, &output)
		} else {
			err = marshalErr
		}
	}
	switch {
	case err != nil && record.Throws != "":
		return nil
	case err != nil:
		return []legDiff{{"$", "go-error", record.Output, err.Error()}}
	case record.Throws != "":
		return []legDiff{{"$", "oracle-error", record.Throws, output}}
	}
	var diffs []legDiff
	diffValues("$", normalize("", record.Output), normalize("", output), &diffs)
	return diffs
}

// legGroup is a set of differences with one leg, kind and path pattern.
type legGroup struct {
	Leg, Kind, Path string
	Cases           []string
	Oracle, Go      string
}

func (g legGroup) key() string { return g.Leg + " | " + g.Kind + " | " + g.Path }

// groupRequestDiffs runs every recorded request case and groups the differences.
func groupRequestDiffs(pin legsPin) map[string]*legGroup {
	groups := map[string]*legGroup{}
	for _, record := range pin.Requests {
		for _, d := range runRequestLeg(record) {
			g := legGroup{Leg: record.Leg, Kind: d.Kind, Path: indexPattern.ReplaceAllString(d.Path, "[]")}
			existing, ok := groups[g.key()]
			if !ok {
				g.Oracle, g.Go = short(d.Oracle), short(d.Go)
				existing = &g
				groups[g.key()] = existing
			}
			existing.Cases = append(existing.Cases, record.Case)
		}
	}
	return groups
}

// TestRequestLegsReport prints the grouped differences; it is the working view while
// the ledger is built (LEGS_REPORT=1) and asserts nothing.
func TestRequestLegsReport(t *testing.T) {
	if os.Getenv("LEGS_REPORT") == "" {
		t.Skip("set LEGS_REPORT=1 to print the differences")
	}
	groups := groupRequestDiffs(loadLegsPin(t))
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		g := groups[key]
		fmt.Fprintf(&out, "%3d %s\n      case=%q\n      oracle=%s\n      go    =%s\n", len(g.Cases), key, g.Cases[0], g.Oracle, g.Go)
	}
	if path := os.Getenv("LEGS_REPORT"); path != "1" {
		if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d groups", len(groups))
}

func short(v any) string {
	raw, _ := json.Marshal(v)
	if len(raw) > 110 {
		return string(raw[:110]) + "..."
	}
	return string(raw)
}

// ledger is compat/parity/divergences.json: each difference the differential finds,
// tied to the PARITY item that accounts for it.
type ledger struct {
	Entries []ledgerEntry `json:"entries"`
}

type ledgerEntry struct {
	Direction string `json:"direction"`
	Leg       string `json:"leg"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Ref       string `json:"ref"`
	// Example pins what the difference is, in the first case that shows it: a leg
	// that changes how it differs, while still differing, must change the ledger.
	Example ledgerExample `json:"example"`
}

type ledgerExample struct {
	Case      string `json:"case"`
	Oracle    string `json:"oracle"`
	Routeweft string `json:"routeweft"`
}

func (e ledgerEntry) key() string { return e.Leg + " | " + e.Kind + " | " + e.Path }

func loadLedger(t *testing.T) ledger {
	t.Helper()
	raw, err := os.ReadFile("../../compat/parity/divergences.json")
	if err != nil {
		t.Fatal(err)
	}
	var l ledger
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// parityRef reports whether a ledger reference resolves in docs/PARITY.md: a D row,
// a register item (a table row or a bold bullet with that id), or a numbered section.
func parityRef(doc, ref string) bool {
	switch {
	case strings.HasPrefix(ref, "§"):
		return strings.Contains(doc, "\n### "+strings.TrimPrefix(ref, "§")+" ") || strings.Contains(doc, "\n## "+strings.TrimPrefix(ref, "§")+" ")
	case regexp.MustCompile(`^D\d+$`).MatchString(ref):
		return strings.Contains(doc, "\n| "+ref+" |")
	default:
		return strings.Contains(doc, "\n| "+ref+" |") || strings.Contains(doc, "- **"+ref+"**")
	}
}

func readParity(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/PARITY.md")
	if err != nil {
		t.Fatal(err)
	}
	return "\n" + string(raw)
}

// TestLedgerReferencesResolve keeps the ledger honest: nothing may cite a row that
// does not exist.
func TestLedgerReferencesResolve(t *testing.T) {
	doc := readParity(t)
	for _, entry := range loadLedger(t).Entries {
		if !parityRef(doc, entry.Ref) {
			t.Errorf("%s cites %q, which docs/PARITY.md does not define", entry.key(), entry.Ref)
		}
	}
}

// TestRequestLegsMatchTheOracleOrAreInTheLedger is the differential: Routeweft's
// request legs against the oracle's own, over the corpus. Every difference must be
// accounted for in the ledger, and every ledger entry must still be a difference.
func TestRequestLegsMatchTheOracleOrAreInTheLedger(t *testing.T) {
	groups := groupRequestDiffs(loadLegsPin(t))
	listed := map[string]ledgerEntry{}
	for _, entry := range loadLedger(t).Entries {
		if entry.Direction == "request" {
			listed[entry.key()] = entry
		}
	}
	checkLedger(t, groups, listed)
}

// checkLedger holds the groups found against the entries recorded.
func checkLedger(t *testing.T, groups map[string]*legGroup, listed map[string]ledgerEntry) {
	t.Helper()
	for key, group := range groups {
		entry, ok := listed[key]
		if !ok {
			t.Errorf("unrecorded difference: %s (case %q: oracle %s, routeweft %s). Fix the leg, or record it in compat/parity/divergences.json with a PARITY item (LEGS_LEDGER_UPDATE=1 writes the entry)", key, group.Cases[0], group.Oracle, group.Go)
			continue
		}
		if want := (ledgerExample{group.Cases[0], group.Oracle, group.Go}); entry.Example != want {
			t.Errorf("%s differs differently now: recorded case %q oracle %s routeweft %s, found case %q oracle %s routeweft %s. If the new behavior is intended, update the entry (LEGS_LEDGER_UPDATE=1) and check it against %s",
				key, entry.Example.Case, entry.Example.Oracle, entry.Example.Routeweft, want.Case, want.Oracle, want.Routeweft, entry.Ref)
		}
	}
	for key := range listed {
		if _, ok := groups[key]; !ok {
			t.Errorf("stale ledger entry %s: the legs no longer differ here; remove it", key)
		}
	}
}

// TestUpdateLedger rewrites the examples (and adds the groups that are new, with the
// reference UNTRIAGED, which fails TestLedgerReferencesResolve until someone cites an
// item). It is how the ledger is refreshed after a leg, the corpus or the oracle
// changes: LEGS_LEDGER_UPDATE=1 go test ./internal/parity -run TestUpdateLedger.
func TestUpdateLedger(t *testing.T) {
	if os.Getenv("LEGS_LEDGER_UPDATE") == "" {
		t.Skip("set LEGS_LEDGER_UPDATE=1 to rewrite compat/parity/divergences.json")
	}
	current := loadLedger(t)
	refs := map[string]string{}
	for _, entry := range current.Entries {
		refs[entry.Direction+"|"+entry.key()] = entry.Ref
	}
	var entries []ledgerEntry
	add := func(direction string, groups map[string]*legGroup) {
		for key, group := range groups {
			ref := refs[direction+"|"+key]
			if ref == "" {
				ref = "UNTRIAGED"
			}
			entries = append(entries, ledgerEntry{Direction: direction, Leg: group.Leg, Kind: group.Kind, Path: group.Path, Ref: ref, Example: ledgerExample{group.Cases[0], group.Oracle, group.Go}})
		}
	}
	add("request", groupRequestDiffs(loadLegsPin(t)))
	add("response", groupResponseDiffs(loadResponseRecords(t)))
	// Entries of the control workflows belong to internal/control/api's updater.
	for _, entry := range current.Entries {
		if entry.Direction != "request" && entry.Direction != "response" {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		return a.Direction+a.Leg+a.Kind+a.Path < b.Direction+b.Leg+b.Kind+b.Path
	})
	raw, err := os.ReadFile("../../compat/parity/divergences.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["entries"] = entries
	encoded, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("../../compat/parity/divergences.json", append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d entries written", len(entries))
}

// TestCommandCodeDateIsStillCompared keeps the frozen clock from hiding the field: on any
// other day the conversion's date differs from the oracle's recording, and that has to show.
func TestCommandCodeDateIsStillCompared(t *testing.T) {
	pin := loadLegsPin(t)
	key := legGroup{Leg: "openai>commandcode", Kind: "changed", Path: "$.config.date"}.key()
	if _, ok := groupRequestDiffs(pin)[key]; ok {
		t.Fatalf("%s differs on the oracle's own clock", key)
	}
	previous := commandcodeClock
	defer func() { commandcodeClock = previous }()
	commandcodeClock = func() time.Time { return oracleInstant.AddDate(0, 0, 1) }
	group, ok := groupRequestDiffs(pin)[key]
	if !ok {
		t.Fatalf("a different day's date is not reported at %s", key)
	}
	if group.Oracle != `"2023-11-14"` || group.Go != `"2023-11-15"` {
		t.Errorf("oracle %s routeweft %s", group.Oracle, group.Go)
	}
}
