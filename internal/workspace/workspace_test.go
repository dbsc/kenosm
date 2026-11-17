package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	w, err := New(root, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

func TestWriteFileStripsWorkspacePrefix(t *testing.T) {
	w := newTestWorkspace(t)

	if err := w.WriteFile("workspace/app/index.html", "<html></html>"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// The file must live at root/app/index.html, not root/workspace/app/index.html.
	if _, err := os.Stat(filepath.Join(w.Root(), "app", "index.html")); err != nil {
		t.Fatalf("expected app/index.html under root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "workspace")); err == nil {
		t.Fatal("unexpected nested workspace/ directory created")
	}

	files, err := w.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "app/index.html" {
		t.Fatalf("files = %v, want [app/index.html]", files)
	}
}

func TestListFilesSkipsAssetsLibrary(t *testing.T) {
	w := newTestWorkspace(t)

	if err := w.WriteFile("app/index.html", "<html></html>"); err != nil {
		t.Fatalf("WriteFile app: %v", err)
	}
	// The design library has thousands of files; it must never be listed.
	for _, p := range []string{"assets/icons/a.svg", "assets/icons/b.svg", "assets/README.md"} {
		if err := w.WriteFile(p, "<svg/>"); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}

	files, err := w.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 || files[0].Path != "app/index.html" {
		t.Fatalf("files = %v, want only [app/index.html]", files)
	}
}

func TestReadFileStripsWorkspacePrefix(t *testing.T) {
	w := newTestWorkspace(t)
	if err := w.WriteFile("app/index.html", "hello"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := w.ReadFile("workspace/app/index.html")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if data != "hello" {
		t.Fatalf("data = %q, want hello", data)
	}
}

func TestResolveAbsoluteInsideRoot(t *testing.T) {
	w := newTestWorkspace(t)
	abs := filepath.Join(w.Root(), "x.txt")
	if err := w.WriteFile(abs, "abs"); err != nil {
		t.Fatalf("WriteFile(abs): %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "x.txt")); err != nil {
		t.Fatalf("expected x.txt under root: %v", err)
	}
}

func TestResolveNormalizesBackslashes(t *testing.T) {
	w := newTestWorkspace(t)
	if err := w.WriteFile(`dir\file.txt`, "x"); err != nil {
		t.Fatalf("WriteFile(backslash): %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "dir", "file.txt")); err != nil {
		t.Fatalf("expected dir/file.txt under root: %v", err)
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	w := newTestWorkspace(t)
	err := w.WriteFile("../escape.txt", "x")
	if err == nil {
		t.Fatal("expected escape error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("err = %v, want mention of escape", err)
	}
}

func TestSeedAssetsCopiesLibrary(t *testing.T) {
	w := newTestWorkspace(t)

	src := filepath.Join(t.TempDir(), "lib")
	for _, p := range []string{"README.md", "icons/a.svg", "uiux/scripts/search.py"} {
		full := filepath.Join(src, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w.SetAssetsSource(src)
	if err := w.SeedAssets(); err != nil {
		t.Fatalf("SeedAssets: %v", err)
	}

	for _, p := range []string{"assets/README.md", "assets/icons/a.svg", "assets/uiux/scripts/search.py"} {
		if _, err := os.Stat(filepath.Join(w.Root(), filepath.FromSlash(p))); err != nil {
			t.Fatalf("expected %s seeded: %v", p, err)
		}
	}

	// Seeding is idempotent: a second call must not fail or duplicate.
	if err := w.SeedAssets(); err != nil {
		t.Fatalf("second SeedAssets: %v", err)
	}
}

func TestSeedAssetsRestoresAfterClear(t *testing.T) {
	w := newTestWorkspace(t)

	src := filepath.Join(t.TempDir(), "lib")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "DESIGN.md"), []byte("design"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.SetAssetsSource(src)

	if err := w.SeedAssets(); err != nil {
		t.Fatal(err)
	}
	if err := w.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "assets", "DESIGN.md")); err == nil {
		t.Fatal("assets should be gone after Clear")
	}
	if err := w.SeedAssets(); err != nil {
		t.Fatalf("re-seed after clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "assets", "DESIGN.md")); err != nil {
		t.Fatalf("assets not restored after re-seed: %v", err)
	}
}

func TestSeedAssetsNoSourceIsNoop(t *testing.T) {
	w := newTestWorkspace(t)
	if err := w.SeedAssets(); err != nil {
		t.Fatalf("SeedAssets with no source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "assets")); err == nil {
		t.Fatal("assets dir must not be created when no source is configured")
	}
}
