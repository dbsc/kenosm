package handler

import (
	"net/http"

	"kenosm/internal/repository"
)

// Stats exposes aggregated token usage to the frontend.
type Stats struct {
	usage *repository.UsageRepo
}

func NewStats(usage *repository.UsageRepo) *Stats {
	return &Stats{usage: usage}
}

func (h *Stats) Get(w http.ResponseWriter, r *http.Request) {
	s, err := h.usage.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}
