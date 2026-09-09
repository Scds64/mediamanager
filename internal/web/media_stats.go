package web

import (
	"net/http"

	"mmbot/internal/transfer"
)

// GET /api/media/stats
func (s *Server) handleMediaStats(w http.ResponseWriter, r *http.Request) {
	executor := transfer.GetTransferExecutor()
	if executor == nil {
		writeJSON(w, http.StatusOK, transfer.MediaStats{})
		return
	}
	writeJSON(w, http.StatusOK, executor.History.MediaStats())
}
