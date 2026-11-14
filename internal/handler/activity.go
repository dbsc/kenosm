package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"kenosm/internal/repository"
)

// Activity exposes the per-agent activity feed (what agents read and wrote) and
// the per-run parallel-distribution timeline.
type Activity struct {
	repo *repository.ActivityRepo
}

func NewActivity(repo *repository.ActivityRepo) *Activity {
	return &Activity{repo: repo}
}

// List returns recent activity, optionally filtered to one run:
//
//	GET /api/v1/activity?limit=100
//	GET /api/v1/activity?run_id=<id>&limit=500
func (h *Activity) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if runID := r.URL.Query().Get("run_id"); runID != "" {
		items, err := h.repo.ByRun(r.Context(), runID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"activity": items})
		return
	}
	items, err := h.repo.Recent(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": items})
}

// Timeline returns the parallel-distribution timeline for one run: for each
// subtask, which agent ran it and when it started/finished.
func (h *Activity) Timeline(w http.ResponseWriter, r *http.Request) {
	bars, err := h.repo.Timeline(r.Context(), chi.URLParam(r, "runID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timeline": bars})
}
