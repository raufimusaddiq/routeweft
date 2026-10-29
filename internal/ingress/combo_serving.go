package ingress

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// planModelRoute is the oracle's chat.js:104-171 decision: a requested name that
// is a Combo is served as its ordered members; a single model that lacks a
// capability the request needs may be rescued by a capacity-adapter model; any
// other request is handled=false and takes the ordinary single-model path.
//
// Every planned model becomes the account attempts of its own provider, in
// order, so the shared attempt loop falls from one member to the next exactly as
// it falls from one account to the next (combo.js:300-361).
func (h *Handler) planModelRoute(r *http.Request, snapshot *runtime.RuntimeSnapshot, model, source string, compact bool) (candidates []routing.ProviderRef, attemptCap int, handled bool, err error) {
	if h.opts.Candidates == nil || h.opts.AccountProvider == nil {
		return nil, 0, false, nil
	}
	var ordered []runtime.ComboMember
	// A name with a slash is never a Combo (combo.js:254-257).
	if combo, ok := snapshot.ComboByName(model); ok && !strings.Contains(model, "/") && len(combo.Resolve()) > 0 {
		if combo.FusionEnabled {
			return nil, 0, true, &dispatchError{http.StatusNotImplemented, "combo_fusion_unavailable", fmt.Sprintf("combo %q uses the fusion strategy, which is not served yet", model)}
		}
		ordered, _ = h.PlanCombo(snapshot, callerKey(r, snapshot), model, detectRequiredCapabilities(clientBodyOf(r, nil)))
	} else {
		if !snapshot.AdaptersActive() {
			return nil, 0, false, nil
		}
		target, known := snapshot.ServingModel(model)
		if !known {
			return nil, 0, false, nil
		}
		required := detectRequiredCapabilities(clientBodyOf(r, nil))
		if len(required) == 0 {
			return nil, 0, false, nil
		}
		var ok bool
		if ordered, ok = h.PlanSolo(snapshot, callerKey(r, snapshot), target.ProviderID, target.ID, required); !ok {
			return nil, 0, false, nil
		}
	}
	now := time.Now()
	for _, member := range ordered {
		attempts, known := h.planProviderAttempts(snapshot, member.ProviderID, member.ProviderID+"/"+member.ModelID, source, compact, now)
		if known {
			candidates = append(candidates, attempts...)
		}
	}
	// Each member keeps its own attempt budget, so a later member is never
	// starved by an earlier one that burned through its accounts.
	return candidates, maxAccountAttempts * max(len(ordered), 1), true, nil
}
