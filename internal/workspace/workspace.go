package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Workspace is a directory that agents can write files into and run commands in.
// A mutex serializes file operations so that concurrent agents (parallel
// subtasks) cannot corrupt each other's read-modify-write edits.
type Workspace struct {
	root           string
	commandTimeout time.Duration
	mu             sync.Mutex
}

func New(root string, commandTimeout time.Duration) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{root: abs, commandTimeout: commandTimeout}, nil
}

func (w *Workspace) Root() string { return w.root }

// Clear removes every file and directory inside the workspace, resetting the
// agents' working context for a fresh run.
func (w *Workspace) Clear() error {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(w.root, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// CleanupProcesses kills any process whose working directory is the workspace
// (or inside it) — i.e. servers the agents started in the background and left
// running. It never touches the kenosm process itself (that runs from the repo
// root, not the workspace).
func (w *Workspace) CleanupProcesses() {
	rootAbs, err := filepath.Abs(w.root)
	if err != nil {
		return
	}
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue
		}
		if cwd == rootAbs || strings.HasPrefix(cwd, rootAbs+string(filepath.Separator)) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// resolve maps a relative path to an absolute path inside the workspace,
// refusing to escape root. It normalizes common model mistakes: backslashes,
// a leading "./" or "/", a leading "<workspace>/" prefix (so agents that
// over-prefix paths don't create a nested workspace/workspace directory), and
// absolute paths that already point inside the workspace.
func (w *Workspace) resolve(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	rootBase := filepath.Base(w.root)

	// If the model passed an absolute path already inside the workspace, make
	// it relative so it doesn't nest a full path under the root.
	if abs, err := filepath.Abs(filepath.FromSlash(rel)); err == nil {
		if r, rerr := filepath.Rel(w.root, abs); rerr == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			rel = filepath.ToSlash(r)
		}
	}

	clean := filepath.ToSlash(filepath.Clean(rel))
	clean = strings.TrimPrefix(clean, "./")
	for strings.HasPrefix(clean, "/") {
		clean = strings.TrimPrefix(clean, "/")
	}

	// Strip a leading "<workspace>/" (the root's own basename) so a path like
	// "workspace/foo.txt" resolves to "foo.txt" instead of nesting.
	for strings.HasPrefix(clean, rootBase+"/") || clean == rootBase {
		if clean == rootBase {
			clean = ""
			break
		}
		clean = strings.TrimPrefix(clean, rootBase+"/")
	}

	abs := filepath.Join(w.root, filepath.FromSlash(clean))
	rel2, err := filepath.Rel(w.root, abs)
	if err != nil {
		return "", err
	}
	if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return abs, nil
}

func (w *Workspace) WriteFile(rel, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	abs, err := w.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

func (w *Workspace) ReadFile(rel string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	abs, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (w *Workspace) ListFiles() ([]FileInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]FileInfo, 0)
	err := filepath.Walk(w.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(w.root, path)
		if err != nil {
			return err
		}
		out = append(out, FileInfo{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// EditFile replaces every occurrence of oldStr with newStr in rel and returns
// how many replacements were made. It errors if oldStr is not present.
func (w *Workspace) EditFile(rel, oldStr, newStr string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	abs, err := w.resolve(rel)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return 0, err
	}
	content := string(data)
	if !strings.Contains(content, oldStr) {
		return 0, fmt.Errorf("old_string not found in %s", rel)
	}
	n := strings.Count(content, oldStr)
	updated := strings.ReplaceAll(content, oldStr, newStr)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return 0, err
	}
	return n, nil
}

// Search returns matching lines across the workspace as "path:line: text",
// using a case-insensitive substring match. Binary and oversized files are
// skipped.
func (w *Workspace) Search(query string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	q := strings.ToLower(query)
	var out []string
	err := filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || (info != nil && info.Size() > 256*1024) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(w.root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), q) {
				out = append(out, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line)))
				if len(out) >= 100 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RunCommand executes a shell command inside the workspace with a timeout.
// It returns combined output, the exit code, and a non-nil error only for
// execution-level failures (timeout, missing shell), not non-zero exits.
func (w *Workspace) RunCommand(ctx context.Context, command string) (string, int, error) {
	timeout := w.commandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", command)
	cmd.Dir = w.root
	// Run in its own process group so a timeout kills the whole tree,
	// including backgrounded servers and hanging children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	var sb strings.Builder
	cmd.Stdout = &sb
	cmd.Stderr = &sb

	err := cmd.Run()
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return sb.String(), -1, fmt.Errorf("command timed out after %s", timeout)
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return sb.String(), ee.ExitCode(), nil
		}
		return sb.String(), -1, err
	}
	return sb.String(), 0, nil
}
