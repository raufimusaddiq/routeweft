package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// attemptSlot is one entry of the attempt loop's candidate list. Ordinarily it is
// one account of one model. A Combo member that is itself a Combo is a slot that
// expands when the loop reaches it, so a nested Combo's rotation only advances
// when the outer Combo gets that far (open-sse chat.js:177-229).
type attemptSlot struct {
	ref routing.ProviderRef
	// member numbers the model within the request; accounts of one model share it.
	member int
	// adapter marks a model the gateway added; contextWindow is what its history
	// is fitted to (capacityAdapter.js:156-168).
	adapter       bool
	contextWindow int
	// label names provider/model for failure messages.
	label string
	// unavailable is set for a model that has no account to try; reaching it
	// counts as that model failing.
	unavailable *dispatchError
	// expand turns a nested Combo into its own slots.
	expand func() ([]attemptSlot, error)
}

// modelRoute is the candidate list for one request.
type modelRoute struct {
	slots []attemptSlot
	// attemptCap is the wire-attempt budget: ten per model, so a later model is
	// never starved by an earlier one.
	attemptCap int
	// combo is set when the request is served as a Combo (or an adapter rescue),
	// so an exhausted list is answered as combo.js:363-381 answers it.
	combo bool
}

// planModelRoute is the oracle's chat.js:104-171 decision: a requested name that
// is a Combo is served as its ordered members; a single model that lacks a
// capability the request needs may be rescued by a capacity-adapter model; any
// other request is handled=false and takes the ordinary single-model path.
//
// Every planned model becomes the account attempts of its own provider, in
// order, so the shared attempt loop falls from one member to the next exactly as
// it falls from one account to the next (combo.js:300-361).
func (h *Handler) planModelRoute(r *http.Request, snapshot *runtime.RuntimeSnapshot, model, source string, compact bool) (route modelRoute, handled bool, err error) {
	if h.opts.Candidates == nil || h.opts.AccountProvider == nil {
		return modelRoute{}, false, nil
	}
	var ordered []plannedMember
	plan := comboPlan{h: h, r: r, snapshot: snapshot, source: source, compact: compact}
	// A name with a slash is never a Combo (combo.js:254-257).
	if combo, ok := snapshot.ComboByName(model); ok && !strings.Contains(model, "/") && len(combo.Resolve()) > 0 {
		if _, fusion := snapshot.FusionCombo(model); fusion {
			// Fusion Combos are answered before the attempt loop (fusion_serving.go);
			// a request that still gets here has no handler to run a panel.
			return modelRoute{}, true, &dispatchError{http.StatusNotImplemented, "combo_fusion_unavailable", fmt.Sprintf("combo %q uses the fusion strategy, which cannot be served from here", model)}
		}
		plan.access = callerKey(r, snapshot)
		plan.required = detectRequiredCapabilities(clientBodyOf(r, nil))
		ordered, _ = h.planCombo(snapshot, plan.access, model, plan.required)
	} else {
		if !snapshot.AdaptersActive() {
			return modelRoute{}, false, nil
		}
		target, known := snapshot.ServingModel(model)
		if !known {
			return modelRoute{}, false, nil
		}
		plan.required = detectRequiredCapabilities(clientBodyOf(r, nil))
		if len(plan.required) == 0 {
			return modelRoute{}, false, nil
		}
		plan.access = callerKey(r, snapshot)
		var ok bool
		if ordered, ok = h.planSolo(snapshot, plan.access, target.ProviderID, target.ID, plan.required); !ok {
			return modelRoute{}, false, nil
		}
	}
	slots, err := plan.slots(ordered, 1)
	if err != nil {
		return modelRoute{}, true, err
	}
	return modelRoute{slots: slots, attemptCap: maxAccountAttempts * max(plan.models, 1), combo: true}, true, nil
}

// comboPlan carries what turning ordered models into attempt slots needs.
type comboPlan struct {
	h        *Handler
	r        *http.Request
	snapshot *runtime.RuntimeSnapshot
	access   auth.Entry
	required []string
	source   string
	compact  bool
	models   int // models that have accounts to try, across the whole request
	nextID   int
}

// slots expands ordered models into attempt slots. A nested Combo is left as a
// slot that plans itself, with the same required capabilities and caller key, when
// the loop reaches it; the key is not checked again because the outer name was
// (keyAccess.js:162-177 filters only adapter models).
func (p *comboPlan) slots(ordered []plannedMember, depth int) ([]attemptSlot, error) {
	now := time.Now()
	var slots []attemptSlot
	for _, member := range ordered {
		if member.IsCombo() {
			name := member.ModelID
			if depth >= runtime.MaxComboDepth {
				return nil, &dispatchError{http.StatusBadGateway, "combo_too_deep", fmt.Sprintf("combo %q nests deeper than %d", name, runtime.MaxComboDepth)}
			}
			slots = append(slots, attemptSlot{label: name, expand: func() ([]attemptSlot, error) {
				if _, ok := p.snapshot.ComboByName(name); !ok {
					return nil, nil
				}
				if _, fusion := p.snapshot.FusionCombo(name); fusion {
					return nil, &dispatchError{http.StatusNotImplemented, "combo_fusion_unavailable", fmt.Sprintf("combo %q is a Fusion Combo nested in an ordered Combo, which is not served", name)}
				}
				planned, _ := p.h.planCombo(p.snapshot, p.access, name, p.required)
				return p.slots(planned, depth+1)
			}})
			continue
		}
		p.nextID++
		id := p.nextID
		label := member.ProviderID + "/" + member.ModelID
		attempts, known := p.h.planProviderAttempts(p.snapshot, member.ProviderID, label, p.source, p.compact, now)
		switch {
		case !known:
			slots = append(slots, attemptSlot{member: id, label: label, unavailable: &dispatchError{http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured for inference", label)}})
		case len(attempts) == 0:
			slots = append(slots, attemptSlot{member: id, label: label, unavailable: &dispatchError{http.StatusServiceUnavailable, "no_eligible_accounts", fmt.Sprintf("[%s] %s", label, routing.ErrNoEligibleAccounts.Error())}})
		default:
			p.models++
			for _, attempt := range attempts {
				slots = append(slots, attemptSlot{ref: attempt, member: id, adapter: member.Adapter, contextWindow: member.ContextWindow, label: label})
			}
		}
	}
	return slots, nil
}

// failureText is the oracle's errorText of a failed member result
// (combo.js:313-332): the error message of the JSON body, else the status text.
func failureText(status int, reason string, body []byte) string {
	if text := jsonErrorMessage(body); text != "" {
		return text
	}
	if reason != "" {
		return reason
	}
	// combo.js:352: lastError = errorText || String(result.status).
	return strconv.Itoa(status)
}

// reasonPhrase is the response's status text without the code (fetch's statusText).
func reasonPhrase(response *http.Response) string {
	return strings.TrimSpace(strings.TrimPrefix(response.Status, strconv.Itoa(response.StatusCode)))
}

// transientWait is combo.js:342-349: before a Combo falls past a 502, 503 or 504
// it waits out the cooldown the oracle would give that failure when it is five
// seconds or less and not zero. checkFallbackError gives a cooldown from the
// message first (errorConfig.js:64-75) and 30 s to a bare 502/503/504, so only a
// message that names the condition is ever waited on.
func transientWait(status int, text string) time.Duration {
	if status != 502 && status != 503 && status != 504 {
		return 0
	}
	lower := strings.ToLower(text)
	for _, rule := range []struct {
		text string
		wait time.Duration
	}{
		{"context_length_exceeded", 0},
		{"maximum context length", 0},
		{"no credentials", 2 * time.Minute},
		{"request not allowed", 5 * time.Second},
		{"improperly formed request", 2 * time.Minute},
		// Every backoff rule starts at level 1: BACKOFF_CONFIG.base (2 s).
		{"rate limit", 2 * time.Second},
		{"too many requests", 2 * time.Second},
		{"quota exceeded", 2 * time.Second},
		{"insufficient credit", 2 * time.Second},
		{"credit balance is too low", 2 * time.Second},
		{"capacity", 2 * time.Second},
		{"overloaded", 2 * time.Second},
	} {
		if strings.Contains(lower, rule.text) {
			if rule.wait > 0 && rule.wait <= 5*time.Second {
				return rule.wait
			}
			return 0
		}
	}
	return 0
}

// comboExhausted is combo.js:363-381: when every model failed the status is the
// first failure's (503 when the last message names missing credentials) and the
// message is the last failure's.
func comboExhausted(firstStatus int, lastText string) *dispatchError {
	status := firstStatus
	if strings.Contains(strings.ToLower(lastText), "no credentials") || status == 0 {
		status = http.StatusServiceUnavailable
	}
	message := strings.TrimFunc(lastText, unicode.IsSpace)
	if message == "" {
		message = "All combo models unavailable"
	}
	return &dispatchError{status, "combo_unavailable", message}
}

// jsonErrorMessage is errorBody?.error?.message || errorBody?.error ||
// errorBody?.message (combo.js:318), with a non-string value written as JSON the
// way combo.js:330-332 does.
func jsonErrorMessage(body []byte) string {
	var parsed struct {
		Error   json.RawMessage `json:"error"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	asString := func(raw json.RawMessage) string {
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			return text
		}
		return ""
	}
	var nested struct {
		Message json.RawMessage `json:"message"`
	}
	if len(parsed.Error) > 0 && json.Unmarshal(parsed.Error, &nested) == nil {
		if text := asString(nested.Message); text != "" {
			return text
		}
	}
	if text := asString(parsed.Error); text != "" {
		return text
	}
	if len(parsed.Error) > 0 && string(parsed.Error) != "null" && string(parsed.Error) != "false" && string(parsed.Error) != `""` {
		return string(parsed.Error)
	}
	return asString(parsed.Message)
}

type modelFailure struct {
	status int
	text   string
}

// comboTrace records how each model of a Combo failed. A nil trace (an ordinary
// request) records and waits for nothing.
type comboTrace struct {
	failures map[int]modelFailure
	order    []int // models in the order they first failed
	lastText string
}

func (t *comboTrace) note(member, status int, text string) {
	if t == nil {
		return
	}
	if _, seen := t.failures[member]; !seen {
		t.order = append(t.order, member)
	}
	t.failures[member] = modelFailure{status, text}
	t.lastText = text
}

// wait is the pause before a Combo falls past a model that failed transiently
// (combo.js:342-349); it ends with the client.
func (t *comboTrace) wait(ctx context.Context, member int) error {
	if t == nil {
		return nil
	}
	last, ok := t.failures[member]
	if !ok {
		return nil
	}
	delay := transientWait(last.status, last.text)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
