package swarm

import (
	"reflect"
	"testing"
)

func TestParseOrchestrationDependencies(t *testing.T) {
	raw := `{"plan":["a","b","c"],"contract":"use ./x.css", "assignments":{"Gemini A":"m"},"dependencies":{"2":[0,1]}}`
	plan, contract, assignments, deps, err := parseOrchestration(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(plan, []string{"a", "b", "c"}) {
		t.Fatalf("plan = %v", plan)
	}
	if contract != "use ./x.css" {
		t.Fatalf("contract = %q", contract)
	}
	if assignments["Gemini A"] != "m" {
		t.Fatalf("assignments = %v", assignments)
	}
	if deps == nil {
		t.Fatalf("deps is nil, want non-nil with {2:[0 1]}")
	}
	if !reflect.DeepEqual(deps[2], []int{0, 1}) {
		t.Fatalf("deps[2] = %v", deps[2])
	}
}

func TestParseOrchestrationAbsentDependencies(t *testing.T) {
	raw := `{"plan":["a","b"]}`
	_, _, _, deps, err := parseOrchestration(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if deps != nil {
		t.Fatalf("deps = %v, want nil for absent field", deps)
	}
}

func TestParseOrchestrationEmptyDependencies(t *testing.T) {
	raw := `{"plan":["a","b"],"dependencies":{}}`
	_, _, _, deps, err := parseOrchestration(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if deps == nil {
		t.Fatalf("deps is nil, want empty non-nil map for {}")
	}
	if len(deps) != 0 {
		t.Fatalf("deps = %v, want empty", deps)
	}
}

func TestDowngradeFallbacks(t *testing.T) {
	available := []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-flash-latest"}
	got := downgradeFallbacks("gemini-3.5-flash", available)
	// Lite models must come first (the lightweight downgrade target).
	if len(got) == 0 || got[0] != "gemini-3.5-flash-lite" {
		t.Fatalf("got = %v, want lite model first", got)
	}
	// The primary must be excluded.
	for _, m := range got {
		if m == "gemini-3.5-flash" {
			t.Fatalf("primary leaked into fallbacks: %v", got)
		}
	}
}

func TestComputeWaves(t *testing.T) {
	// 0 and 1 independent (wave 0), 2 depends on 0, 3 depends on 0 and 1.
	deps := map[int][]int{2: {0}, 3: {0, 1}}
	waves := computeWaves(4, deps)
	// wave 0 = {0,1}, wave 1 = {2,3}
	if len(waves) != 2 {
		t.Fatalf("waves = %v, want 2 levels", waves)
	}
	if !reflect.DeepEqual(waves[0], []int{0, 1}) {
		t.Fatalf("wave 0 = %v", waves[0])
	}
	if !reflect.DeepEqual(waves[1], []int{2, 3}) {
		t.Fatalf("wave 1 = %v", waves[1])
	}
}

func TestComputeWavesSequential(t *testing.T) {
	deps := map[int][]int{1: {0}, 2: {1}, 3: {2}}
	waves := computeWaves(4, deps)
	if len(waves) != 4 {
		t.Fatalf("waves = %v, want 4 levels (sequential)", waves)
	}
	for i, w := range waves {
		if !reflect.DeepEqual(w, []int{i}) {
			t.Fatalf("wave %d = %v, want [%d]", i, w, i)
		}
	}
}

func TestComputeWavesCycle(t *testing.T) {
	deps := map[int][]int{0: {1}, 1: {0}}
	waves := computeWaves(2, deps)
	if len(waves) == 0 {
		t.Fatalf("waves is empty on cycle, want non-empty")
	}
}

func TestSubtaskBounds(t *testing.T) {
	cases := []struct {
		agents int
		lo, hi int
	}{
		{0, 1, 6},
		{1, 1, 6},
		{2, 2, 6},
		{3, 3, 6},
		{5, 5, 10},
		{10, 10, 20},
		{15, 15, 30},
	}
	for _, c := range cases {
		lo, hi := subtaskBounds(c.agents)
		if lo != c.lo || hi != c.hi {
			t.Errorf("subtaskBounds(%d) = (%d,%d), want (%d,%d)", c.agents, lo, hi, c.lo, c.hi)
		}
	}
}
