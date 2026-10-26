package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// defaultQuotaStaleAfter is three default refresh intervals (quota.Service.Run
// falls back to five minutes): one missed refresh is not stale, three are.
const defaultQuotaStaleAfter = 15 * time.Minute

// Quota read-model account states. not-reported (nothing was ever observed),
// unknown (observed, but no bound was reported), unlimited (every reported
// window is unlimited) and exhausted are four distinct outcomes; none of them
// is inferred from another.
const (
	quotaNotReported = "not-reported"
	quotaUnknown     = "unknown"
	quotaUnlimited   = "unlimited"
	quotaAvailable   = "available"
	quotaExhausted   = "exhausted"
	quotaError       = "error"
	quotaCooldown    = "cooldown"
)

// quotaSorts are the account orders GET /admin/v1/quota accepts. "default" is
// provider, priority, name, id: the order the page has always used.
var quotaSorts = map[string]bool{"default": true, "reset": true, "exhausted": true, "error": true}

type quotaRow struct {
	item      map[string]any
	status    string
	remaining *float64
	reset     time.Time
	index     int
}

// quota serves the account quota read model. Filtering, searching and sorting
// happen before paging, so every account is reachable whatever the page size,
// and a sort by status orders all accounts rather than one page. Status comes
// from in-memory runtime state, so the filtered connection list is read whole
// and paged here; it is bounded by the configured connections.
func (h *Handler) quota(w http.ResponseWriter, r *http.Request) error {
	if h.opts.Runtime == nil || h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "quota read model is not configured")
		return nil
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return nil
	}
	query := r.URL.Query()
	order := strings.TrimSpace(query.Get("sort"))
	if order == "" {
		order = "default"
	}
	if !quotaSorts[order] {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be one of default, reset, exhausted or error")
		return nil
	}
	providerFilter := strings.TrimSpace(query.Get("provider"))
	search := strings.ToLower(strings.TrimSpace(query.Get("q")))

	rows, err := h.opts.DB.QueryContext(r.Context(), `SELECT c.id,n.provider_id,c.name,c.enabled FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id ORDER BY n.provider_id,c.priority,c.name,c.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := time.Now()
	staleAfter := h.opts.QuotaStaleAfter
	if staleAfter <= 0 {
		staleAfter = defaultQuotaStaleAfter
	}
	providerSet := map[string]bool{}
	var matched []quotaRow
	for rows.Next() {
		var id, provider, name string
		var enabled int
		if err := rows.Scan(&id, &provider, &name, &enabled); err != nil {
			return err
		}
		providerSet[provider] = true
		if providerFilter != "" && provider != providerFilter {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(name), search) && !strings.Contains(strings.ToLower(provider), search) {
			continue
		}
		row := h.quotaRow(id, provider, name, enabled != 0, now, staleAfter)
		row.index = len(matched)
		matched = append(matched, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sortQuotaRows(matched, order)
	providers := make([]string, 0, len(providerSet))
	for provider := range providerSet {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	items := make([]any, 0, p.Size)
	for i := p.Offset; i < len(matched) && i < p.Offset+p.Size; i++ {
		items = append(items, matched[i].item)
	}
	response := pageResponse(items, p, int64(len(matched)))
	response["providers"] = providers
	response["sort"] = order
	writeJSON(w, http.StatusOK, response)
	return nil
}

// quotaRow builds one account's read model from the cached named windows and
// the routing observation. The provider error text is never serialized.
func (h *Handler) quotaRow(id, provider, name string, enabled bool, now time.Time, staleAfter time.Duration) quotaRow {
	item := map[string]any{"connectionId": id, "providerId": provider, "name": name, "enabled": enabled}
	row := quotaRow{item: item, status: quotaNotReported}
	var windows []quota.Window
	observed := false
	if h.opts.QuotaState != nil {
		if detail, ok := h.opts.QuotaState(id); ok {
			observed = true
			windows = detail.Windows
			item["plan"] = detail.Plan
			item["windows"] = detail.Windows
			if !detail.ObservedAt.IsZero() {
				item["windowsObservedAt"] = detail.ObservedAt
			}
			if detail.AvailableReset != nil {
				item["availableResetCredits"] = *detail.AvailableReset
			}
			if detail.CreditAvailable != nil {
				item["creditAvailable"] = *detail.CreditAvailable
			}
		}
	}
	state := h.opts.Runtime.State()
	if state != nil {
		if observation, ok := state.Quota(id); ok {
			observed = true
			switch {
			case observation.Err != "":
				row.status = quotaError
				item["error"] = true
			case observation.Remaining != nil:
				row.status = quotaAvailable
				if *observation.Remaining <= 0 && (observation.ResetAt.IsZero() || observation.ResetAt.After(now)) {
					row.status = quotaExhausted
				}
				value := *observation.Remaining
				row.remaining = &value
				item["remaining"] = value
			}
			if !observation.ResetAt.IsZero() {
				item["resetAt"] = observation.ResetAt
				row.reset = observation.ResetAt
			}
			if !observation.ObservedAt.IsZero() {
				item["observedAt"] = observation.ObservedAt
				item["stale"] = now.Sub(observation.ObservedAt) > staleAfter
			}
		}
	}
	if row.status == quotaNotReported && observed {
		row.status = quotaUnknown
		if allUnlimited(windows) {
			row.status = quotaUnlimited
		}
	}
	for _, window := range windows {
		if window.ResetAt != nil && (row.reset.IsZero() || window.ResetAt.Before(row.reset)) {
			row.reset = *window.ResetAt
		}
	}
	if !row.reset.IsZero() {
		item["nextResetAt"] = row.reset
	}
	if state != nil {
		if until, ok := state.CooldownUntil(id, now); ok {
			row.status = quotaCooldown
			item["cooldownUntil"] = until
		}
	}
	item["status"] = row.status
	return row
}

func allUnlimited(windows []quota.Window) bool {
	for _, window := range windows {
		if !window.Unlimited {
			return false
		}
	}
	return len(windows) > 0
}

// sortQuotaRows orders accounts for one sort mode. Ties keep the default order,
// so paging is stable across requests.
func sortQuotaRows(rows []quotaRow, order string) {
	rank := func(row quotaRow) int { return 0 }
	switch order {
	case "exhausted":
		rank = func(row quotaRow) int {
			switch row.status {
			case quotaExhausted:
				return 0
			case quotaCooldown:
				return 1
			}
			return 2
		}
	case "error":
		rank = func(row quotaRow) int {
			if row.status == quotaError {
				return 0
			}
			return 1
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		switch order {
		case "reset":
			if a.reset.IsZero() != b.reset.IsZero() {
				return !a.reset.IsZero()
			}
			if !a.reset.Equal(b.reset) {
				return a.reset.Before(b.reset)
			}
		case "exhausted":
			if (a.remaining == nil) != (b.remaining == nil) {
				return a.remaining != nil
			}
			if a.remaining != nil && *a.remaining != *b.remaining {
				return *a.remaining < *b.remaining
			}
		}
		return a.index < b.index
	})
}
