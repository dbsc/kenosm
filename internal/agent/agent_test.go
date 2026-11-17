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
	// Desktop is screenshot-only now: files(5) + shell(1) + browser(7) + desktop(2) = 15.
	a = New(Config{Browser: &browser.Browser{}, Desktop: &desktop.Desktop{}})
	names = toolNames(a)
	if len(names) != 15 {
		t.Fatalf("all modules = %d tools, want 15", len(names))
	}
	for _, want := range []string{"write_file", "run_command", "browser_navigate", "desktop_screenshot"} {
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
	if len(names) != 13 {
		t.Fatalf("after disabling 2 tools = %d, want 13", len(names))
	}
	if has(names, "run_command") || has(names, "browser_click") {
		t.Fatalf("disabled tool still present: %v", names)
	}
	if !has(names, "write_file") || !has(names, "desktop_screenshot") {
		t.Fatalf("non-disabled tools missing: %v", names)
	}
}

func TestModulesAndDisabledToolNames(t *testing.T) {
	mods := Modules()
	seen := map[string]bool{}
	for _, m := range mods {
		seen[m.Name] = true
		if len(m.Tools) == 0 {
			t.Errorf("module %q has no tools", m.Name)
		}
	}
	for _, want := range []string{"files", "shell", "browser", "desktop"} {
		if !seen[want] {
			t.Errorf("missing module %q", want)
		}
	}

	disabled := DisabledToolNames([]string{"shell", "desktop"})
	if len(disabled) == 0 {
		t.Fatal("expected disabled tools for shell+desktop")
	}
	set := map[string]bool{}
	for _, n := range disabled {
		set[n] = true
	}
	if !set["run_command"] {
		t.Errorf("expected run_command to be disabled, got %v", disabled)
	}
	if set["write_file"] || set["browser_navigate"] {
		t.Errorf("files/browser tools should not be disabled, got %v", disabled)
	}
}
