package swarm

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestDirectionFor(t *testing.T) {
	reads := []string{"read_file", "search_files", "list_files", "browser_navigate", "browser_snapshot", "browser_text", "browser_screenshot", "desktop_windows", "desktop_screenshot"}
	for _, tool := range reads {
		if got := DirectionFor(tool); got != "read" {
			t.Errorf("DirectionFor(%q) = %q, want read", tool, got)
		}
	}
	writes := []string{"write_file", "edit_file", "run_command", "desktop_launch", "desktop_focus", "desktop_move", "desktop_click", "desktop_type", "desktop_key", "desktop_scroll"}
	for _, tool := range writes {
		if got := DirectionFor(tool); got != "write" {
			t.Errorf("DirectionFor(%q) = %q, want write", tool, got)
		}
	}
	if got := DirectionFor("something_else"); got != "other" {
		t.Errorf("DirectionFor(unknown) = %q, want other", got)
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

func TestRedecomposeBudget(t *testing.T) {
	cases := []struct {
		agents int
		want   int
	}{
		{0, 3},
		{1, 3},
		{3, 5},
		{5, 7},
		{10, 12},
		{50, 12},
	}
	for _, c := range cases {
		if got := redecomposeBudget(c.agents); got != c.want {
			t.Errorf("redecomposeBudget(%d) = %d, want %d", c.agents, got, c.want)
		}
	}
}

func TestExecStateScheduling(t *testing.T) {
	// 4 jobs: 0 and 1 independent, 2 depends on 0, 3 depends on 0 and 1.
	st := newExecState(3)
	st.jobs[0] = &job{id: 0, text: "a"}
	st.jobs[1] = &job{id: 1, text: "b"}
	st.jobs[2] = &job{id: 2, text: "c"}
	st.jobs[3] = &job{id: 3, text: "d"}
	st.nextID = 4
	st.jobs[2].deps = []int{0}
	st.jobs[3].deps = []int{0, 1}
	st.depCount[2] = 1
	st.depCount[3] = 2
	st.dependents[0] = []int{2, 3}
	st.dependents[1] = []int{3}
	st.ready = []int{0, 1}

	// The two independent jobs are handed out first.
	j, split, ok := st.take()
	if !ok || split || j == nil || j.id != 0 {
		t.Fatalf("take() = %+v, split=%v, ok=%v; want job 0", j, split, ok)
	}
	j, split, ok = st.take()
	if !ok || split || j == nil || j.id != 1 {
		t.Fatalf("take() = %+v, split=%v, ok=%v; want job 1", j, split, ok)
	}

	// Finishing 0 makes 2 runnable immediately (no wave barrier); 3 still waits.
	st.finish(0, SubtaskResult{Agent: "a", Task: "a"})
	j, split, ok = st.take()
	if !ok || split || j == nil || j.id != 2 {
		t.Fatalf("take() after finish(0) = %+v, split=%v, ok=%v; want job 2", j, split, ok)
	}

	// Finishing 1 unblocks 3.
	st.finish(1, SubtaskResult{Agent: "b", Task: "b"})
	j, split, ok = st.take()
	if !ok || split || j == nil || j.id != 3 {
		t.Fatalf("take() after finish(1) = %+v, split=%v, ok=%v; want job 3", j, split, ok)
	}

	st.finish(2, SubtaskResult{Agent: "c", Task: "c"})
	st.finish(3, SubtaskResult{Agent: "d", Task: "d"})

	if _, _, ok := st.take(); ok {
		t.Fatalf("take() after all jobs finished = ok true, want done")
	}
	if len(st.results) != 4 {
		t.Fatalf("results = %d, want 4", len(st.results))
	}
}

func TestExecStateCycleForced(t *testing.T) {
	// Two jobs that depend on each other (a cycle). With maxRounds=0 the
	// scheduler must force them to run rather than wait forever.
	st := newExecState(1)
	st.maxRounds = 0
	st.jobs[0] = &job{id: 0, text: "a"}
	st.jobs[1] = &job{id: 1, text: "b"}
	st.nextID = 2
	st.jobs[0].deps = []int{1}
	st.jobs[1].deps = []int{0}
	st.depCount[0] = 1
	st.depCount[1] = 1
	st.dependents[0] = []int{1}
	st.dependents[1] = []int{0}

	j1, split1, ok1 := st.take()
	if !ok1 || split1 || j1 == nil {
		t.Fatalf("take() on cycle = %+v, split=%v, ok=%v; want a forced job", j1, split1, ok1)
	}
	j2, split2, ok2 := st.take()
	if !ok2 || split2 || j2 == nil {
		t.Fatalf("second take() on cycle = %+v, split=%v, ok=%v; want a forced job", j2, split2, ok2)
	}
	st.finish(j1.id, SubtaskResult{Agent: "a", Task: "a"})
	st.finish(j2.id, SubtaskResult{Agent: "b", Task: "b"})
	if _, _, ok := st.take(); ok {
		t.Fatalf("take() after cycle jobs done = ok true, want done")
	}
}

func TestExecStateReplaceBlocked(t *testing.T) {
	// 0 finished; 1 blocked on 0; 2 blocked on 1. Re-decomposition replaces the
	// blocked chain with a finer plan.
	st := newExecState(2)
	st.jobs[0] = &job{id: 0, text: "a"}
	st.jobs[1] = &job{id: 1, text: "b"}
	st.jobs[2] = &job{id: 2, text: "c"}
	st.nextID = 3
	st.finished[0] = true
	st.depCount[1] = 1
	st.depCount[2] = 1
	st.jobs[1].deps = []int{0}
	st.jobs[2].deps = []int{1}
	st.dependents[0] = []int{1}
	st.dependents[1] = []int{2}

	st.replaceBlocked([]string{"x", "y", "z"}, map[int][]int{1: {0}, 2: {1}})

	if len(st.jobs) != 4 { // finished 0 + three new jobs
		t.Fatalf("len(jobs) = %d, want 4", len(st.jobs))
	}
	if !st.finished[0] {
		t.Fatalf("job 0 must remain finished")
	}
	// New jobs carry ids 3,4,5 with text x,y,z; 4 depends on 3, 5 depends on 4.
	if st.depCount[3] != 0 || st.depCount[4] != 1 || st.depCount[5] != 1 {
		t.Fatalf("depCount = %v, want {3:0, 4:1, 5:1}", st.depCount)
	}
	if len(st.ready) != 1 || st.ready[0] != 3 {
		t.Fatalf("ready = %v, want [3]", st.ready)
	}
	if st.jobs[3].text != "x" || st.jobs[4].text != "y" || st.jobs[5].text != "z" {
		t.Fatalf("new job texts wrong: %+v %+v %+v", st.jobs[3], st.jobs[4], st.jobs[5])
	}
}

func TestWorkerPrompt(t *testing.T) {
	p := workerPrompt("build a site", "use ./style.css", "- [a] write index.html: done", "write app.js", []string{"index.html"})
	for _, want := range []string{"build a site", "use ./style.css", "write app.js", "Work completed so far", "index.html"} {
		if !strings.Contains(p, want) {
			t.Errorf("workerPrompt missing %q in:\n%s", want, p)
		}
	}
}

func TestExecStateConcurrentWorkers(t *testing.T) {
	// 20 independent jobs, 5 workers: every agent keeps pulling work and all
	// jobs finish. Exercises the cond-based blocking and termination under race
	// detection.
	const jobs = 20
	const workers = 5
	st := newExecState(workers)
	for i := 0; i < jobs; i++ {
		st.jobs[i] = &job{id: i, text: "job"}
	}
	st.nextID = jobs
	for i := 0; i < jobs; i++ {
		st.ready = append(st.ready, i)
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, split, ok := st.take()
				if !ok {
					return
				}
				if split {
					t.Errorf("unexpected split request on independent jobs")
					return
				}
				time.Sleep(time.Millisecond)
				st.finish(j.id, SubtaskResult{Agent: "a", Task: j.text})
			}
		}()
	}
	wg.Wait()

	if len(st.results) != jobs {
		t.Fatalf("results = %d, want %d", len(st.results), jobs)
	}
}
