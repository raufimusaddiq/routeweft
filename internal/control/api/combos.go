package api

import (
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type comboRequest struct {
	Name          string             `json:"name"`
	Strategy      string             `json:"strategy"`
	StickyLimit   uint64             `json:"stickyLimit"`
	JudgeModel    string             `json:"judgeModel"`
	FusionEnabled bool               `json:"fusionEnabled"`
	Members       []comboMemberInput `json:"members"`
}

type comboMemberInput struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	Selected   *bool  `json:"selected"`
}

func (h *Handler) handlePutCombo(w http.ResponseWriter, r *http.Request) {
	if h.opts.Combos == nil {
		writeError(w, http.StatusServiceUnavailable, "combos_unavailable", "Combo management is not configured")
		return
	}
	var body comboRequest
	if !decodeJSON(w, r, 1<<20, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_combo", "Combo name is required")
		return
	}
	combo := runtime.Combo{ID: r.PathValue("id"), Name: body.Name, Strategy: strings.TrimSpace(body.Strategy), StickyLimit: body.StickyLimit, JudgeModel: strings.TrimSpace(body.JudgeModel), FusionEnabled: body.FusionEnabled}
	for position, member := range body.Members {
		selected := true
		if member.Selected != nil {
			selected = *member.Selected
		}
		combo.Members = append(combo.Members, runtime.ComboMember{ProviderID: strings.TrimSpace(member.ProviderID), ModelID: strings.TrimSpace(member.ModelID), Position: position, Selected: selected})
	}
	snapshot, err := h.opts.Combos.PutCombo(r.Context(), combo)
	if err != nil {
		writeError(w, http.StatusBadRequest, "combo_save_failed", "Combo could not be saved; members must reference configured models")
		return
	}
	if h.opts.Events != nil {
		h.opts.Events.Publish("config.updated", map[string]any{"resource": "combo", "name": body.Name})
	}
	stored, _ := snapshot.ComboByName(body.Name)
	writeJSON(w, http.StatusOK, comboJSON(stored, snapshot.ConfigRevision()))
}

func (h *Handler) handleDeleteCombo(w http.ResponseWriter, r *http.Request) {
	if h.opts.Combos == nil {
		writeError(w, http.StatusServiceUnavailable, "combos_unavailable", "Combo management is not configured")
		return
	}
	snapshot, err := h.opts.Combos.DeleteCombo(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "combo_not_found", "Combo was not found")
		return
	}
	if h.opts.Events != nil {
		h.opts.Events.Publish("config.updated", map[string]any{"resource": "combo", "id": r.PathValue("id")})
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "deleted": true, "configRevision": snapshot.ConfigRevision()})
}

func comboJSON(combo runtime.Combo, revision uint64) map[string]any {
	members := make([]map[string]any, 0, len(combo.Members))
	for _, member := range combo.Members {
		members = append(members, map[string]any{"providerId": member.ProviderID, "modelId": member.ModelID, "position": member.Position, "selected": member.Selected})
	}
	return map[string]any{"id": combo.ID, "name": combo.Name, "strategy": combo.Strategy, "stickyLimit": combo.StickyLimit, "judgeModel": combo.JudgeModel, "fusionEnabled": combo.FusionEnabled, "members": members, "configRevision": revision}
}
