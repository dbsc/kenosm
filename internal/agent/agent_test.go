package agent

import (
	"testing"

	"kenosm/internal/browser"
	"kenosm/internal/desktop"
)

func TestCleanRelPath(t *testing.T) {
	cases := map[string]string{
		"app/index.html":            "app/index.html",
		"workspace/app/index.html":  "app/index.html",
		"workspace/workspace/x.css": "x.css",
		"workspace":                 "",
		"./app/index.html":          "app/index.html",
		"/app/index.html":           "app/index.html",
		"dir\\file.txt":             "dir/file.txt",
		"  app/index.html  ":        "app/index.html",
		"workspace/":                "",
	}
	for in, want := range cases {
		if got := cleanRelPath(in); got != want {
			t.Errorf("cleanRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func toolNames(a *Agent) []string {
	names := make([]string, 0, len(a.tools))
	for _, t := range a.tools {
		names = append(names, t.def.Name)
	}
	return names
}

func has(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func TestToolModulesCompose(t *testing.T) {
	// No browser/desktop: only the always-on file and shell modules.
	a := New(Config{})
	names := toolNames(a)
	if len(names) != 6 {
		t.Fatalf("file+shell tools = %d, want 6 (%v)", len(names), names)
	}

	// Browser and desktop modules enable only when their runtime is present.
	a = New(Config{Browser: &browser.Browser{}, Desktop: &desktop.Desktop{}})
	names = toolNames(a)
	if len(names) != 22 {
		t.Fatalf("all modules = %d tools, want 22", len(names))
	}
	for _, want := range []string{"write_file", "run_command", "browser_navigate", "desktop_click"} {
		if !has(names, want) {
			t.Errorf("expected tool %q in the full toolset", want)
		}
	}
}

func TestDisabledToolsFilter(t *testing.T) {
	a := New(Config{
		Browser:       &browser.Browser{},
		Desktop:       &desktop.Desktop{},
		DisabledTools: []string{"run_command", "browser_click"},
	})
	names := toolNames(a)
	if len(names) != 20 {
		t.Fatalf("after disabling 2 tools = %d, want 20", len(names))
	}
	if has(names, "run_command") || has(names, "browser_click") {
		t.Fatalf("disabled tool still present: %v", names)
	}
	if !has(names, "write_file") || !has(names, "desktop_type") {
		t.Fatalf("non-disabled tools missing: %v", names)
	}
}
