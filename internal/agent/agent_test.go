package agent

import (
	"strings"
	"testing"

	"kenosm/internal/browser"
	"kenosm/internal/desktop"
	"kenosm/internal/llm"
)

func TestCompactHistoryPreservesAlternation(t *testing.T) {
	turn := func(role string) llm.Turn { return llm.Turn{Role: role, Text: "x"} }
	mk := func(n int) []llm.Turn {
		h := []llm.Turn{turn("user")} // task
		for i := 0; i < n; i++ {
			h = append(h, turn("model"), turn("user"))
		}
		return h
	}

	// Under the cap: unchanged.
	small := mk(5)
	if got := compactHistoryBudget(small, defaultHistoryMaxBytes); len(got) != len(small) {
		t.Fatalf("small history must stay intact, got %d turns", len(got))
	}

	// Over the cap: pairs dropped, task kept, alternation valid.
	big := mk(30)
	got := compactHistoryBudget(big, defaultHistoryMaxBytes)
	if len(got) > historyMaxTurns {
		t.Fatalf("compacted to %d turns, cap is %d", len(got), historyMaxTurns)
	}
	if got[0].Role != "user" {
		t.Fatal("task turn must survive compaction")
	}
	for i := 1; i < len(got); i += 2 {
		if got[i].Role != "model" || (i+1 < len(got) && got[i+1].Role != "user") {
			t.Fatalf("alternation broken at %d: %s then %s", i, got[i].Role, got[i+1].Role)
		}
	}
}

func TestCompactHistoryDropsOversizedContent(t *testing.T) {
	h := []llm.Turn{
		{Role: "user", Text: "task"},
		{Role: "model", Text: "thinking"},
		{Role: "user", Text: strings.Repeat("a", 20*1024), ToolResponses: []llm.FunctionResponse{{Response: map[string]any{"content": strings.Repeat("b", 20*1024)}}}},
		{Role: "model", Text: "thinking"},
		{Role: "user", Text: strings.Repeat("c", 20*1024), ToolResponses: []llm.FunctionResponse{{Response: map[string]any{"content": strings.Repeat("d", 20*1024)}}}},
		{Role: "model", Text: "thinking"},
		{Role: "user", Text: "final"},
	}
	got := compactHistoryBudget(h, defaultHistoryMaxBytes)
	if historyBytes(got) > defaultHistoryMaxBytes {
		t.Fatalf("compacted history is %d bytes, cap %d", historyBytes(got), defaultHistoryMaxBytes)
	}
	if got[0].Text != "task" || got[len(got)-1].Text != "final" {
		t.Fatal("task and newest turns must survive")
	}
}

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
