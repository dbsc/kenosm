package handler

import (
	"encoding/base64"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"

	"kenosm/internal/workspace"
)

// imageMimes maps file extensions to MIME types served as images.
var imageMimes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// Workspace exposes the shared workspace files to the frontend.
type Workspace struct {
	ws *workspace.Workspace
}

func NewWorkspace(ws *workspace.Workspace) *Workspace {
	return &Workspace{ws: ws}
}

func (h *Workspace) List(w http.ResponseWriter, _ *http.Request) {
	files, err := h.ws.ListFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// Clear wipes the workspace, removing all files the agents created, then
// restores the design library so a cleared workspace still has assets.
func (h *Workspace) Clear(w http.ResponseWriter, _ *http.Request) {
	h.ws.CleanupProcesses()
	if err := h.ws.Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.ws.SeedAssets(); err != nil {
		writeError(w, http.StatusInternalServerError, "clear ok, reseed failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

func (h *Workspace) Read(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "*")
	content, err := h.ws.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found: "+err.Error())
		return
	}
	// Serve images as base64 so the frontend can render them (screenshots etc.).
	if mime, ok := imageMimes[filepath.Ext(path)]; ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"path":      path,
			"mime_type": mime,
			"base64":    base64.StdEncoding.EncodeToString([]byte(content)),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "content": content})
}
