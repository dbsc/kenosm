package swarm

import (
	"reflect"
	"testing"
)

func TestParseOrchestrationDependencies(t *testing.T) {
	raw := `{"plan":["a","b","c"],"contract":"use ./x.css", "assignments":{"Gemini A":"m"},"dependencies":{"2":[0,1]}}`
	o, err := parseOrchestration(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.Direct {
		t.Fatalf("direct = true, want false")
	}
	if !reflect.DeepEqual(o.Plan, []string{"a", "b", "c"}) {
		t.Fatalf("plan = %v", o.Plan)
	}
	if o.Contract != "use ./x.css" {
		t.Fatalf("contract = %q", o.Contract)
	}
	if o.Assignments["Gemini A"] != "m" {
		t.Fatalf("assignments = %v", o.Assignments)
	}
	if o.Dependencies == nil {
		t.Fatalf("deps is nil, want non-nil with {2:[0 1]}")
	}
	if !reflect.DeepEqual(o.Dependencies[2], []int{0, 1}) {
		t.Fatalf("deps[2] = %v", o.Dependencies[2])
	}
}

func TestParseOrchestrationDirect(t *testing.T) {
	raw := `{"direct": true, "answer": "Привет! Чем помочь?"}`
	o, err := parseOrchestration(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !o.Direct {
		t.Fatalf("direct = false, want true")
	}
	if o.Answer != "Привет! Чем помочь?" {
		t.Fatalf("answer = %q", o.Answer)
	}
}

func TestParseOrchestrationDirectEmpty(t *testing.T) {
	if _, err := parseOrchestration(`{"direct": true, "answer": ""}`); err == nil {
		t.Fatalf("expected error for empty direct answer")
	}
}

func TestParseOrchestrationAbsentDependencies(t *testing.T) {
	o, err := parseOrchestration(`{"plan":["a","b"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.Dependencies != nil {
		t.Fatalf("deps = %v, want nil for absent field", o.Dependencies)
	}
}

func TestParseOrchestrationEmptyDependencies(t *testing.T) {
	o, err := parseOrchestration(`{"plan":["a","b"],"dependencies":{}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.Dependencies == nil {
		t.Fatalf("deps is nil, want empty non-nil map for {}")
	}
	if len(o.Dependencies) != 0 {
		t.Fatalf("deps = %v, want empty", o.Dependencies)
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

func TestParseWaveTasks(t *testing.T) {
	raw := "```json\n{\"tasks\":{\"0\":\"write index.html\",\"2\":\"test everything\"}}\n```"
	tasks, err := parseWaveTasks(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tasks[0] != "write index.html" || tasks[2] != "test everything" || len(tasks) != 2 {
		t.Fatalf("tasks = %v", tasks)
	}

	// Bare {"0": "..."} form (models sometimes drop the "tasks" wrapper).
	bare, err := parseWaveTasks(`{"0":"build app","1":"test it"}`)
	if err != nil {
		t.Fatalf("bare parse: %v", err)
	}
	if bare[0] != "build app" || bare[1] != "test it" || len(bare) != 2 {
		t.Fatalf("bare tasks = %v", bare)
	}

	// Empty string values are dropped.
	if _, err := parseWaveTasks(`{"tasks":{"0":""}}`); err == nil {
		t.Fatalf("expected error when all task values are empty")
	}
	if _, err := parseWaveTasks("no json here"); err == nil {
		t.Fatalf("expected error for non-JSON output")
	}
}

func TestMaxSubtasks(t *testing.T) {
	cases := []struct {
		agents int
		want   int
	}{
		{0, 4},
		{1, 4},
		{2, 4},
		{3, 6},
		{5, 10},
		{10, 20},
		{15, 30},
	}
	for _, c := range cases {
		if got := maxSubtasks(c.agents); got != c.want {
			t.Errorf("maxSubtasks(%d) = %d, want %d", c.agents, got, c.want)
		}
	}
}
