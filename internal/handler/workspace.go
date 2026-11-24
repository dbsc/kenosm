package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	ws   *workspace.Workspace
	busy func() bool // reports whether any run is still executing
}

func NewWorkspace(ws *workspace.Workspace, busy func() bool) *Workspace {
	return &Workspace{ws: ws, busy: busy}
}

func (h *Workspace) List(w http.ResponseWriter, _ *http.Request) {
	files, err := h.ws.ListFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// treeNode is one entry of the lazy file tree: directories carry their
// children (capped) and a truncated flag when more entries were cut off.
type treeNode struct {
	Name      string     `json:"name"`
	Path      string     `json:"path"`
	Dir       bool       `json:"dir"`
	Size      int64      `json:"size,omitempty"`
	Children  []treeNode `json:"children,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// treeMaxChildren caps how many children a directory reports in one shot; the
// frontend lazily expands one directory at a time, so the cap only guards
// against pathological directories.
const treeMaxChildren = 300

// Tree returns the workspace as a lazy file tree rooted at ?path= (empty =
// workspace root). Each directory node includes its children up to the cap.
func (h *Workspace) Tree(w http.ResponseWriter, r *http.Request) {
	root := strings.Trim(strings.TrimPrefix(r.URL.Query().Get("path"), "/"), "/")
	files, err := h.ws.ListFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries := buildTree(files, root)
	writeJSON(w, http.StatusOK, map[string]any{"path": root, "entries": entries})
}

// buildTree groups the flat file list into a sorted tree under root. Directory
// children are dirs-first, then files, both alphabetically; deeper nesting is
// preserved (a path like a/b/c.txt nests c.txt under a/b, not directly under a).
func buildTree(files []workspace.FileInfo, root string) []treeNode {
	prefix := ""
	if root != "" {
		prefix = root + "/"
	}

	type entry struct {
		name  string
		path  string
		isDir bool
		size  int64
	}
	dirs := map[string]bool{}        // directory paths already created
	children := map[string][]entry{} // parent path -> direct children

	addChild := func(parent, name, path string, isDir bool, size int64) {
		children[parent] = append(children[parent], entry{name: name, path: path, isDir: isDir, size: size})
	}
	join := func(a, b string) string {
		if a == "" {
			return b
		}
		return a + "/" + b
	}

	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, prefix)
		if rel == f.Path && prefix != "" {
			continue // not under root
		}
		segs := strings.Split(rel, "/")
		if len(segs) == 0 || segs[0] == "" {
			continue
		}
		// Ensure every ancestor directory exists, then attach the file leaf.
		cur := prefix
		for _, seg := range segs[:len(segs)-1] {
			next := join(cur, seg)
			if !dirs[next] {
				dirs[next] = true
				addChild(cur, seg, next, true, 0)
			}
			cur = next
		}
		addChild(cur, segs[len(segs)-1], f.Path, false, f.Size)
	}

	var build func(dir string) []treeNode
	build = func(dir string) []treeNode {
		kids := children[dir]
		sort.Slice(kids, func(a, b int) bool {
			if kids[a].isDir != kids[b].isDir {
				return kids[a].isDir
			}
			return kids[a].name < kids[b].name
		})
		out := make([]treeNode, 0, len(kids))
		for _, k := range kids {
			n := treeNode{Name: k.name, Path: k.path, Dir: k.isDir, Size: k.size}
			if k.isDir {
				n.Children = build(k.path)
				if len(n.Children) > treeMaxChildren {
					n.Truncated = true
					n.Children = n.Children[:treeMaxChildren]
				}
			}
			out = append(out, n)
		}
		return out
	}

	return build(prefix)
}

// ListDirs lists the subdirectories of an arbitrary host directory (default
// "/"), so the frontend can render a folder browser for picking the working
// folder anywhere on disk — not just inside the workspace.
func (h *Workspace) ListDirs(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = "/"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, "cannot read directory: "+err.Error())
		return
	}
	type dirInfo struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := []dirInfo{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, dirInfo{Name: e.Name(), Path: filepath.Join(abs, e.Name())})
	}
	sort.Slice(dirs, func(a, b int) bool { return dirs[a].Name < dirs[b].Name })
	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": abs, "parent": parent, "dirs": dirs})
}

// Upload saves an attached file (any type) into the workspace so agents can
// read it, returning its workspace-relative path. The frontend attaches files
// of any kind this way — images additionally go to the model as image inputs.
func (h *Workspace) Upload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Data string `json:"data"` // base64, optionally a full data URL
	}
	// Cap the raw body: a 64 MB file base64-encodes to ~85 MB plus JSON framing.
	// The decoded size is re-checked below (max 64 MB raw).
	r.Body = http.MaxBytesReader(w, r.Body, 96<<20)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: {name, data}")
		return
	}
	name := filepath.Base(strings.TrimSpace(in.Name))
	if name == "" || name == "." || name == ".." || strings.Contains(name, "\\") {
		writeError(w, http.StatusBadRequest, "invalid file name")
		return
	}
	b64 := in.Data
	if i := strings.Index(b64, ","); i >= 0 && strings.Contains(b64[:i], "base64") {
		b64 = b64[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid base64 data: "+err.Error())
		return
	}
	if len(raw) > 64<<20 {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large (max 64 MB)")
		return
	}
	rel := "uploads/" + name
	abs, err := h.ws.Resolve(rel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(abs, raw, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "size": len(raw)})
}

// Root returns the current workspace root directory (the working folder).
func (h *Workspace) Root(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"root": h.ws.Root()})
}

// Home returns the current user's home directory — the starting point for the
// folder picker's "Home" button.
func (h *Workspace) Home(w http.ResponseWriter, _ *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/"
	}
	writeJSON(w, http.StatusOK, map[string]any{"home": home})
}

// SetRoot switches the working folder to any directory the user picks — not
// just the default workspace. Refused while a run is executing: re-rooting
// under working agents would strand their files mid-task.
func (h *Workspace) SetRoot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, r, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		writeError(w, http.StatusBadRequest, "invalid body: {path}")
		return
	}
	if h.busy != nil && h.busy() {
		writeError(w, http.StatusConflict, "a run is in progress; stop it before switching the working folder")
		return
	}
	if err := h.ws.SetRoot(strings.TrimSpace(in.Path)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"root": h.ws.Root()})
}

// Clear wipes the workspace, removing all files the agents created, then
// restores the design library so a cleared workspace still has assets. It is
// refused while a run is executing: wiping files from under working agents
// would corrupt the run.
func (h *Workspace) Clear(w http.ResponseWriter, _ *http.Request) {
	if h.busy != nil && h.busy() {
		writeError(w, http.StatusConflict, "a run is in progress; stop it before clearing the workspace")
		return
	}
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
	// Bound the response: an oversized file (a runaway agent, a crafted one)
	// must not balloon into a multi-gigabyte base64 string.
	if len(content) > 64<<20 {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large to serve")
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
