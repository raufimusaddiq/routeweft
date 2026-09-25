package api

import "net/http"

func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	if h.opts.Logs == nil {
		writeError(w, http.StatusServiceUnavailable, "logs_unavailable", "console logs are not configured")
		return
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return
	}
	items, total := h.opts.Logs.list(p, r.URL.Query().Get("level"), r.URL.Query().Get("query"))
	writeJSON(w, http.StatusOK, pageResponse(items, p, total))
}
