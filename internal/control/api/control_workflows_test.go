package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// workflowPin is compat/parity/generate-control-workflows.mjs's recording of the
// oracle's own combo, key and model alias routes over short workflows (#274).
type workflowPin struct {
	Workflows []struct {
		Workflow string `json:"workflow"`
		Steps    []struct {
			Step   string  `json:"step"`
			Status int     `json:"status"`
			Error  *string `json:"error"`
		} `json:"steps"`
		Final map[string]any `json:"final"`
	} `json:"workflows"`
}

// outcome is what a step came to, coarse enough to compare two different APIs: any
// success is "ok" (the oracle creates with 201 where Routeweft answers 200), a
// refusal keeps its status.
func outcome(status int) string {
	if status >= 200 && status < 300 {
		return "ok"
	}
	return fmt.Sprint(status)
}

// workflowRunner executes one oracle step on Routeweft's admin API. A step with no
// route here answers noRoute.
type workflowRunner struct {
	t       *testing.T
	mux     *http.ServeMux
	handler *Handler
	cookie  *http.Cookie
	ids     map[string]string
}

const noRoute = -1

func (r *workflowRunner) call(method, path, body string) (int, map[string]any) {
	r.t.Helper()
	recorder := doJSON(r.t, r.mux, r.cookie, method, path, body)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

func envelopeCode(decoded map[string]any) string {
	if inner, ok := decoded["error"].(map[string]any); ok {
		code, _ := inner["code"].(string)
		return code
	}
	return ""
}

// combosSteps replays the combo workflow. Models are left out: the oracle accepts any
// model string, and Routeweft's member check is its own step below.
func (r *workflowRunner) combosStep(step string) (int, string) {
	combo := func(name string) string { return fmt.Sprintf(`{"name":%q,"members":[]}`, name) }
	create := func(label, name string) (int, string) {
		status, decoded := r.call(http.MethodPost, "/admin/v1/combos", combo(name))
		if id, _ := decoded["id"].(string); status < 300 && id != "" {
			r.ids[label] = id
		}
		return status, envelopeCode(decoded)
	}
	update := func(id, body string) (int, string) {
		status, decoded := r.call(http.MethodPut, "/admin/v1/combos/"+id, body)
		return status, envelopeCode(decoded)
	}
	switch step {
	case "create a combo":
		return create("good", "good-name")
	case "create a combo with the same name":
		return create("dup", "good-name")
	case "create a combo whose name has a space":
		return create("space", "with space")
	case "create a combo named with dots, underscores and hyphens":
		return create("dots", "dots.and_under-scores")
	case "create a combo with no name":
		status, decoded := r.call(http.MethodPost, "/admin/v1/combos", `{"members":[]}`)
		return status, envelopeCode(decoded)
	case "create a combo named with spaces only":
		return create("blank", "   ")
	case "rename a combo to a name in use":
		return update(r.ids["dots"], combo("good-name"))
	case "rename a combo to a new name":
		return update(r.ids["dots"], combo("renamed"))
	case "rename a combo to a name with a space":
		return update(r.ids["dots"], combo("bad name"))
	case "update a combo that does not exist":
		return update("missing", combo("ghost"))
	case "keep a combo's own name on update":
		return update(r.ids["good"], combo("good-name"))
	case "delete a combo":
		status, decoded := r.call(http.MethodDelete, "/admin/v1/combos/"+r.ids["dots"], "")
		return status, envelopeCode(decoded)
	case "delete the same combo again":
		status, decoded := r.call(http.MethodDelete, "/admin/v1/combos/"+r.ids["dots"], "")
		return status, envelopeCode(decoded)
	}
	return noRoute, "no read-by-id route (the list carries combos)"
}

func (r *workflowRunner) keysStep(step string) (int, string) {
	switch step {
	case "create a key":
		status, decoded := r.call(http.MethodPost, "/admin/v1/keys", `{"name":"ci"}`)
		if key, _ := decoded["key"].(map[string]any); key != nil {
			r.ids["first"], _ = key["id"].(string)
		}
		return status, envelopeCode(decoded)
	case "create a key with no name":
		status, decoded := r.call(http.MethodPost, "/admin/v1/keys", `{}`)
		return status, envelopeCode(decoded)
	case "create a key named with spaces only":
		status, decoded := r.call(http.MethodPost, "/admin/v1/keys", `{"name":"   "}`)
		return status, envelopeCode(decoded)
	case "disable a key":
		status, decoded := r.call(http.MethodPatch, "/admin/v1/keys/"+r.ids["first"], `{"paused":true}`)
		return status, envelopeCode(decoded)
	case "update a key that does not exist":
		status, decoded := r.call(http.MethodPatch, "/admin/v1/keys/missing", `{"paused":true}`)
		return status, envelopeCode(decoded)
	case "delete a key":
		status, decoded := r.call(http.MethodDelete, "/admin/v1/keys/"+r.ids["first"], "")
		return status, envelopeCode(decoded)
	case "delete the same key again":
		status, decoded := r.call(http.MethodDelete, "/admin/v1/keys/"+r.ids["first"], "")
		return status, envelopeCode(decoded)
	}
	return noRoute, "no read-by-id route (the list carries keys)"
}

func (r *workflowRunner) aliasesStep(step string) (int, string) {
	put := func(alias, model string) (int, string) {
		provider, id, _ := strings.Cut(model, "/")
		status, decoded := r.call(http.MethodPost, "/admin/v1/aliases", fmt.Sprintf(`{"alias":%q,"providerId":%q,"modelId":%q}`, alias, provider, id))
		return status, envelopeCode(decoded)
	}
	del := func(alias string) (int, string) {
		status, decoded := r.call(http.MethodDelete, "/admin/v1/aliases/"+url.PathEscape(alias), "")
		return status, envelopeCode(decoded)
	}
	switch step {
	case "set an alias", "replace an alias":
		return put("fast", "openai/gpt-4o")
	case "set an alias with no model":
		status, decoded := r.call(http.MethodPost, "/admin/v1/aliases", `{"alias":"fast"}`)
		return status, envelopeCode(decoded)
	case "set an alias with no name":
		status, decoded := r.call(http.MethodPost, "/admin/v1/aliases", `{"providerId":"openai","modelId":"gpt-4o"}`)
		return status, envelopeCode(decoded)
	case "set an alias named with spaces only":
		return put("   ", "openai/gpt-4o")
	case "point an alias at a model nothing provides":
		return put("ghost", "nowhere/none")
	case "delete an alias", "delete the same alias again":
		return del("fast")
	}
	return noRoute, "the alias is part of the path, so a request without one has no route"
}

// finalState reads the state the oracle's workflow ended in, from Routeweft.
func (r *workflowRunner) finalState(workflow string) map[string]any {
	switch workflow {
	case "combos":
		var page struct{ Items []struct{ Name string } }
		_, decoded := r.call(http.MethodGet, "/admin/v1/combos", "")
		raw, _ := json.Marshal(decoded)
		_ = json.Unmarshal(raw, &page)
		names := []string{}
		for _, item := range page.Items {
			names = append(names, item.Name)
		}
		sort.Strings(names)
		return map[string]any{"names": names}
	case "keys":
		_, decoded := r.call(http.MethodGet, "/admin/v1/keys", "")
		items, _ := decoded["items"].([]any)
		return map[string]any{"count": float64(len(items))}
	default:
		_, decoded := r.call(http.MethodGet, "/admin/v1/aliases", "")
		items, _ := decoded["items"].([]any)
		names := []string{}
		for _, item := range items {
			if entry, ok := item.(map[string]any); ok {
				names = append(names, fmt.Sprint(entry["alias"]))
			}
		}
		sort.Strings(names)
		return map[string]any{"aliases": names}
	}
}

type controlGroup struct {
	Workflow, Step, Kind string
	Oracle, Routeweft    string
}

func (g controlGroup) key() string { return g.Workflow + " | " + g.Kind + " | " + g.Step }

// controlDiffs runs every workflow on Routeweft and lists where it ends differently.
func controlDiffs(t *testing.T) []controlGroup {
	t.Helper()
	raw, err := os.ReadFile("../../parity/testdata/control_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin workflowPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	var out []controlGroup
	for _, workflow := range pin.Workflows {
		handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
		runner := &workflowRunner{t: t, mux: mux, handler: handler, cookie: cookie, ids: map[string]string{}}
		if manager, ok := handler.opts.ProviderCatalog.(*runtime.Manager); ok {
			if err := manager.PutModel(context.Background(), runtime.Model{ProviderID: "openai", ID: "gpt-4o"}); err != nil {
				t.Fatal(err)
			}
		}
		for _, step := range workflow.Steps {
			var status int
			var detail string
			switch workflow.Workflow {
			case "combos":
				status, detail = runner.combosStep(step.Step)
			case "keys":
				status, detail = runner.keysStep(step.Step)
			default:
				status, detail = runner.aliasesStep(step.Step)
			}
			oracle := outcome(step.Status)
			if step.Error != nil && oracle != "ok" {
				oracle += " " + *step.Error
			}
			got := "no route: " + detail
			if status != noRoute {
				got = outcome(status)
				if got != "ok" && detail != "" {
					got += " " + detail
				}
			}
			if outcome(step.Status) != outcome(status) {
				out = append(out, controlGroup{workflow.Workflow, step.Step, "outcome", oracle, got})
			}
		}
		final := runner.finalState(workflow.Workflow)
		if want, got := fmt.Sprint(workflow.Final), fmt.Sprint(final); want != got {
			out = append(out, controlGroup{workflow.Workflow, "final state", "state", want, got})
		}
		store.Close()
	}
	return out
}

type controlLedgerEntry struct {
	Direction string `json:"direction"`
	Leg       string `json:"leg"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Ref       string `json:"ref"`
	Example   struct {
		Case      string `json:"case"`
		Oracle    string `json:"oracle"`
		Routeweft string `json:"routeweft"`
	} `json:"example"`
}

func (e controlLedgerEntry) key() string { return e.Leg + " | " + e.Kind + " | " + e.Path }

const ledgerPath = "../../../compat/parity/divergences.json"

func loadControlLedger(t *testing.T) []controlLedgerEntry {
	t.Helper()
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []controlLedgerEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var out []controlLedgerEntry
	for _, entry := range doc.Entries {
		if entry.Direction == "control" {
			out = append(out, entry)
		}
	}
	return out
}

// TestControlWorkflowsMatchTheOracleOrAreInTheLedger replays the oracle's combo, key
// and alias workflows through Routeweft's admin API. A step that comes to a different
// outcome, or a workflow that ends in a different state, must be in the ledger with
// the PARITY item that accounts for it.
func TestControlWorkflowsMatchTheOracleOrAreInTheLedger(t *testing.T) {
	found := map[string]controlGroup{}
	for _, group := range controlDiffs(t) {
		found[group.key()] = group
	}
	listed := map[string]controlLedgerEntry{}
	for _, entry := range loadControlLedger(t) {
		listed[entry.key()] = entry
	}
	for key, group := range found {
		entry, ok := listed[key]
		if !ok {
			t.Errorf("unrecorded difference: %s (oracle %q, routeweft %q). Fix the route, or record it in compat/parity/divergences.json with a PARITY item (CONTROL_LEDGER_UPDATE=1 writes the entry)", key, group.Oracle, group.Routeweft)
			continue
		}
		if entry.Example.Oracle != group.Oracle || entry.Example.Routeweft != group.Routeweft {
			t.Errorf("%s differs differently now: recorded oracle %q routeweft %q, found oracle %q routeweft %q (check against %s)", key, entry.Example.Oracle, entry.Example.Routeweft, group.Oracle, group.Routeweft, entry.Ref)
		}
	}
	for key := range listed {
		if _, ok := found[key]; !ok {
			t.Errorf("stale ledger entry %s: the workflow no longer ends differently here; remove it", key)
		}
	}
}

// TestUpdateControlLedger rewrites the control entries of the ledger, keeping the
// references already chosen and marking new ones UNTRIAGED.
func TestUpdateControlLedger(t *testing.T) {
	if os.Getenv("CONTROL_LEDGER_UPDATE") == "" {
		t.Skip("set CONTROL_LEDGER_UPDATE=1 to rewrite the control entries of compat/parity/divergences.json")
	}
	refs := map[string]string{}
	for _, entry := range loadControlLedger(t) {
		refs[entry.key()] = entry.Ref
	}
	var entries []map[string]any
	for _, group := range controlDiffs(t) {
		ref := refs[group.key()]
		if ref == "" {
			ref = "UNTRIAGED"
		}
		entries = append(entries, map[string]any{"direction": "control", "leg": group.Workflow, "kind": group.Kind, "path": group.Step, "ref": ref,
			"example": map[string]any{"case": group.Step, "oracle": group.Oracle, "routeweft": group.Routeweft}})
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var kept []any
	for _, item := range doc["entries"].([]any) {
		if entry, ok := item.(map[string]any); ok && entry["direction"] != "control" {
			kept = append(kept, entry)
		}
	}
	for _, entry := range entries {
		kept = append(kept, entry)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i].(map[string]any), kept[j].(map[string]any)
		return fmt.Sprint(a["direction"], a["leg"], a["kind"], a["path"]) < fmt.Sprint(b["direction"], b["leg"], b["kind"], b["path"])
	})
	doc["entries"] = kept
	encoded, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d control entries written", len(entries))
}
