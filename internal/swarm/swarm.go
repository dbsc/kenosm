package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"kenosm/internal/agent"
	"kenosm/internal/browser"
	"kenosm/internal/desktop"
	"kenosm/internal/llm"
	"kenosm/internal/transport"
	"kenosm/internal/workspace"
)

// Agent is one swarm member: a Gemini key plus the SOCKS5 proxy it must use.
type Agent struct {
	Name        string
	Slug        string
	BaseURL     string
	APIKey      string
	Proxy       transport.ProxySpec
	ManualModel string // empty = chosen automatically by the orchestrator
}

// Config configures the swarm.
type Config struct {
	Agents            []Agent
	OrchestratorModel string
	DefaultModel      string
	AvailableModels   []string
	DisabledModules   []string // module names to exclude (see agent.Modules)
	Workspace         *workspace.Workspace
	Browser           *browser.Browser // optional; enables the browser_* tools
	Desktop           *desktop.Desktop // optional; enables the desktop_* tools
	Emitter           Emitter          // optional progress-event sink
	TransportCache    *transport.Cache // optional; reuses pooled SOCKS5 transports across agents/runs
}

type Swarm struct {
	cfg Config
}

func New(cfg Config) *Swarm {
	return &Swarm{cfg: cfg}
}

type TranscriptEntry struct {
	Agent string `json:"agent"`
	Role  string `json:"role"`
	Text  string `json:"text"`
}

type SubtaskResult struct {
	Task      string    `json:"task"`
	Agent     string    `json:"agent"`
	Model     string    `json:"model"`
	Summary   string    `json:"summary"`
	ToolCalls []string  `json:"tool_calls"`
	Files     []string  `json:"files"`
	Usage     llm.Usage `json:"usage"`
	Error     string    `json:"error,omitempty"`
}

// UsageRecord attributes token usage to one agent (API key) + model during a run.
type UsageRecord struct {
	Agent string    `json:"agent"`
	Model string    `json:"model"`
	Usage llm.Usage `json:"usage"`
}

type Result struct {
	Task        string               `json:"task"`
	Plan        []string             `json:"plan"`
	Contract    string               `json:"contract"`
	Assignments map[string]string    `json:"assignments"`
	Subtasks    []SubtaskResult      `json:"subtasks"`
	Files       []workspace.FileInfo `json:"files"`
	Final       string               `json:"final"`
	Transcript  []TranscriptEntry    `json:"transcript"`
	Usage       []UsageRecord        `json:"usage"`
}

// Run orchestrates the swarm. First the orchestrator plans the STRUCTURE: the
// concrete steps, a shared technical contract, per-agent model assignments and
// dependency edges between steps, aiming for at least one independent subtask per
// agent. Then the swarm executes the steps on a continuous, dependency-aware
// work-stealing queue: a step runs the moment its prerequisites finish (no wave
// barrier), and an agent that finishes early immediately takes the next runnable
// step. Blocked or overly sequential remaining work is re-decomposed on demand so
// every agent stays busy. Results pass between steps through a shared handoff log.
func (s *Swarm) Run(ctx context.Context, task string, images []llm.Image, prior []llm.Turn) (*Result, error) {
	agents := s.cfg.Agents
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents configured")
	}
	res := &Result{Task: task}

	// Reap any background servers the agents leave running when we finish,
	// whether the run completes, errors, or is stopped.
	if s.cfg.Workspace != nil {
		defer s.cfg.Workspace.CleanupProcesses()
	}

	s.emit(Event{Time: time.Now(), Type: "planning", Detail: "orchestrator is planning the task"})

	orch, err := s.buildClient(agents[0], s.cfg.OrchestratorModel)
	if err != nil {
		return nil, fmt.Errorf("build orchestrator: %w", err)
	}

	o, orchUsage, err := s.plan(ctx, orch, task, images, prior)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}
	orchModel := orch.Model()
	res.Usage = append(res.Usage, UsageRecord{Agent: "orchestrator", Model: orchModel, Usage: orchUsage})
	s.emit(Event{Time: time.Now(), Type: "usage", Agent: "orchestrator", Model: orchModel, Usage: orchUsage})

	// A trivial or conversational task (a greeting, small talk, a simple
	// question) doesn't need the swarm: the orchestrator answers directly and
	// we skip planning subtasks and spawning workers entirely.
	if o.Direct {
		res.Final = o.Answer
		res.Transcript = append(res.Transcript, TranscriptEntry{Agent: "orchestrator", Role: "assistant", Text: o.Answer})
		s.emit(Event{Time: time.Now(), Type: "direct", Agent: "orchestrator", Model: orchModel, Detail: truncate(o.Answer, 240)})
		if s.cfg.Workspace != nil {
			res.Files, _ = s.cfg.Workspace.ListFiles()
		}
		return res, nil
	}

	plan, contract, assignments, deps := o.Plan, o.Contract, o.Assignments, o.Dependencies
	if deps == nil {
		// No dependency info: treat every step as independent so the swarm runs
		// as parallel as possible. The orchestrator is told to specify any
		// required ordering explicitly via "dependencies".
		deps = map[int][]int{}
	}

	// A coarse plan (fewer subtasks than agents) would leave agents idle. Ask the
	// orchestrator once, before anything runs, to expand it into enough parallel
	// subtasks for every agent.
	if len(plan) < len(agents) && len(agents) > 1 {
		if eplan, edeps, eusage, eerr := s.expand(ctx, orch, task, contract, plan, agents); eerr == nil && len(eplan) > len(plan) {
			s.emit(Event{Time: time.Now(), Type: "plan", Detail: fmt.Sprintf("plan expanded to %d parallel steps for %d agents", len(eplan), len(agents))})
			res.Usage = append(res.Usage, UsageRecord{Agent: "orchestrator", Model: orch.Model(), Usage: eusage})
			s.emit(Event{Time: time.Now(), Type: "usage", Agent: "orchestrator", Model: orch.Model(), Usage: eusage})
			plan, deps = eplan, edeps
		}
	}

	res.Plan = plan
	res.Contract = contract
	res.Assignments = assignments
	res.Transcript = append(res.Transcript, TranscriptEntry{Agent: "orchestrator", Role: "planner", Text: strings.Join(plan, "\n")})

	s.emit(Event{Time: time.Now(), Type: "plan", Detail: fmt.Sprintf("%d steps planned", len(plan))})

	// The shared contract is injected into every step's system context so agents
	// agree on file paths, IDs, CSS classes and signatures up front.
	systemExtra := ""
	if contract != "" {
		systemExtra = "Technical contract for this task (shared file paths, IDs, CSS classes and function signatures — follow these exactly):\n" + contract
	}

	// Run the steps on a continuous, dependency-aware work queue: a step becomes
	// runnable the moment its prerequisites finish (no wave barrier), and an
	// agent that finishes early immediately steals the next runnable step — so
	// all agents stay busy. Blocked work is re-decomposed on demand.
	subResults, usage := s.execute(ctx, orch, task, plan, contract, assignments, deps, systemExtra, agents)
	res.Subtasks = subResults
	res.Usage = append(res.Usage, usage...)

	if s.cfg.Workspace != nil {
		files, _ := s.cfg.Workspace.ListFiles()
		res.Files = files
	}

	var sb strings.Builder
	for _, sr := range res.Subtasks {
		sb.WriteString(fmt.Sprintf("[%s / %s] %s: %s\n", sr.Agent, sr.Model, sr.Task, sr.Summary))
	}

	// Ask one agent to turn the raw per-subtask notes into a single, clean,
	// human-readable answer. If that fails (or comes back empty), fall back to
	// the raw notes so the user still gets something.
	res.Final = sb.String()
	if final, model, usage, err := s.finalize(ctx, agents[0], task, subResults); err == nil && strings.TrimSpace(final) != "" {
		res.Final = final
		res.Usage = append(res.Usage, UsageRecord{Agent: agents[0].Name, Model: model, Usage: usage})
		s.emit(Event{Time: time.Now(), Type: "usage", Agent: agents[0].Name, Model: model, Usage: usage})
	}

	return res, nil
}

// execState is the shared state of one continuous scheduling pass. Workers pull
// runnable jobs from it, finish them, and — whenever they would otherwise sit
// idle while blocked work remains — ask the orchestrator to re-decompose the
// remaining work into finer, parallel subtasks.
type execState struct {
	mu          sync.Mutex
	cond        *sync.Cond
	jobs        map[int]*job
	nextID      int
	depCount    map[int]int   // unmet prerequisites for not-yet-runnable jobs
	dependents  map[int][]int // reverse edges: job -> jobs that wait on it
	ready       []int         // runnable, not-yet-dispatched job ids
	finished    map[int]bool
	results     map[int]SubtaskResult
	usage       []UsageRecord
	running     int // jobs currently executing
	splitRounds int
	splitting   bool
	done        bool
	quit        bool // run cancelled
	agents      int
	maxRounds   int
	doneLog     strings.Builder
}

// job is one schedulable unit of work: an original plan step or a finer subtask
// produced by a later re-decomposition.
type job struct {
	id   int
	text string
	deps []int
}

func newExecState(nAgents int) *execState {
	st := &execState{
		jobs:       map[int]*job{},
		depCount:   map[int]int{},
		dependents: map[int][]int{},
		finished:   map[int]bool{},
		results:    map[int]SubtaskResult{},
		agents:     nAgents,
		maxRounds:  redecomposeBudget(nAgents),
	}
	st.cond = sync.NewCond(&st.mu)
	return st
}

// execute runs the plan on a continuous, dependency-aware work-stealing queue.
// A step becomes runnable the moment its prerequisites finish — there is no wave
// barrier — so an agent that finishes early immediately picks up the next
// runnable step, and every agent stays busy until the work is done. Blocked work
// is re-decomposed on demand so idle agents have something to do.
func (s *Swarm) execute(ctx context.Context, orch *llm.Gemini, task string, plan []string, contract string, assignments map[string]string, deps map[int][]int, systemExtra string, agents []Agent) ([]SubtaskResult, []UsageRecord) {
	st := newExecState(len(agents))
	for i, p := range plan {
		st.jobs[i] = &job{id: i, text: p}
	}
	st.nextID = len(plan)
	for i := range plan {
		for _, d := range deps[i] {
			if d < 0 || d >= len(plan) || d == i {
				continue
			}
			st.jobs[i].deps = append(st.jobs[i].deps, d)
			st.depCount[i]++
			st.dependents[d] = append(st.dependents[d], i)
		}
		if st.depCount[i] == 0 {
			st.ready = append(st.ready, i)
		}
	}

	// Wake idle workers if the run is cancelled, so they can exit instead of
	// blocking forever on the work queue.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			st.mu.Lock()
			st.quit = true
			st.cond.Broadcast()
			st.mu.Unlock()
		case <-stop:
		}
	}()

	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func(ag Agent) {
			defer wg.Done()
			s.worker(ctx, orch, ag, st, task, contract, assignments, systemExtra)
		}(a)
	}
	wg.Wait()
	close(stop)

	ids := make([]int, 0, len(st.results))
	for id := range st.results {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]SubtaskResult, 0, len(ids))
	for _, id := range ids {
		out = append(out, st.results[id])
	}
	return out, st.usage
}

// worker is one agent's scheduling loop: pull a runnable job, run it, and record
// the result — or become the re-decomposer when the queue has run dry but work
// remains, so no agent idles while there is still something to do.
func (s *Swarm) worker(ctx context.Context, orch *llm.Gemini, ag Agent, st *execState, task, contract string, assignments map[string]string, systemExtra string) {
	for {
		if ctx.Err() != nil {
			return
		}
		j, split, ok := st.take()
		if !ok {
			return
		}
		if split {
			s.redecomposeAndMerge(ctx, orch, st, task, contract)
			continue
		}

		model := resolveModel(ag, assignments, s.cfg.DefaultModel)
		round := st.round()
		doneText := st.doneSnapshot()
		prompt := workerPrompt(task, contract, doneText, j.text, s.fileNames())

		s.emit(Event{Time: time.Now(), Type: "subtask", Agent: ag.Name, Model: model, Step: j.id, Wave: round, Detail: truncate(j.text, 140)})

		// Workers get a text-only prompt: the raw images are sent to the
		// orchestrator alone (for planning), and image-derived requirements reach
		// workers through the plan/contract. Re-attaching the base64 images here
		// would duplicate their token cost across every agent.
		agRes, usedModel, aerr := s.runWorker(ctx, ag, model, systemExtra, []llm.Turn{{Role: "user", Text: prompt}})

		sr := SubtaskResult{Task: j.text, Agent: ag.Name, Model: usedModel, Files: agRes.Files}
		if aerr != nil {
			sr.Error = aerr.Error()
			s.emit(Event{Time: time.Now(), Type: "subtask_done", Agent: ag.Name, Model: usedModel, Step: j.id, Wave: round, Detail: "error: " + truncate(aerr.Error(), 240)})
		} else {
			sr.Summary = agRes.Summary
			sr.ToolCalls = agRes.ToolCalls
			sr.Usage = agRes.Usage
			s.emit(Event{Time: time.Now(), Type: "subtask_done", Agent: ag.Name, Model: usedModel, Step: j.id, Wave: round, Detail: truncate(agRes.Summary, 240)})
		}
		st.finish(j.id, sr)
	}
}

// take returns the next runnable job, or asks the caller to re-decompose the
// remaining blocked work, or signals that the run is finished. It blocks while
// jobs are still running (a completion will unlock their dependents).
func (st *execState) take() (*job, bool, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for {
		if st.done || st.quit {
			return nil, false, false
		}
		if len(st.ready) > 0 {
			j := st.jobs[st.ready[0]]
			st.ready = st.ready[1:]
			st.running++
			return j, false, true
		}
		pending := len(st.jobs) - len(st.finished)
		if st.running == 0 && pending == 0 {
			st.done = true
			return nil, false, false
		}
		if st.running == 0 && pending > 0 {
			// Every agent is idle but work remains: it is all blocked (or cyclic).
			if st.splitting {
				st.cond.Wait()
				continue
			}
			if st.splitRounds < st.maxRounds {
				st.splitting = true
				return nil, true, true
			}
			// Re-decomposition budget exhausted: break the deadlock by forcing
			// the blocked jobs to run anyway, guaranteeing the run terminates.
			st.forceReady()
			continue
		}
		st.cond.Wait()
	}
}

// finish records a completed job, wakes its dependents and appends the result to
// the shared hand-off log that later workers read.
func (st *execState) finish(id int, sr SubtaskResult) {
	st.mu.Lock()
	st.running--
	st.finished[id] = true
	st.results[id] = sr
	st.usage = append(st.usage, UsageRecord{Agent: sr.Agent, Model: sr.Model, Usage: sr.Usage})
	if sr.Error != "" {
		fmt.Fprintf(&st.doneLog, "- [%s] %s FAILED: %s\n", sr.Agent, truncate(sr.Task, 60), truncate(sr.Error, 100))
	} else {
		fmt.Fprintf(&st.doneLog, "- [%s] %s: %s (files: %s)\n", sr.Agent, truncate(sr.Task, 60), truncate(sr.Summary, 120), strings.Join(sr.Files, ", "))
	}
	for _, d := range st.dependents[id] {
		st.depCount[d]--
		if st.depCount[d] == 0 {
			st.ready = append(st.ready, d)
		}
	}
	st.cond.Broadcast()
	st.mu.Unlock()
}

// forceReady breaks a deadlock (e.g. a dependency cycle the orchestrator failed
// to resolve) by making every blocked job runnable, so the run always ends.
func (st *execState) forceReady() {
	for id := range st.jobs {
		if !st.finished[id] && st.depCount[id] > 0 {
			st.depCount[id] = 0
			st.ready = append(st.ready, id)
		}
	}
}

func (st *execState) round() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.splitRounds
}

func (st *execState) doneSnapshot() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.doneLog.String()
}

// workerPrompt builds the single-task prompt handed to a worker. The live
// hand-off log and current file list let each worker build on what others have
// already finished, without recreating or colliding with their files.
func workerPrompt(task, contract, doneText, jobText string, names []string) string {
	var b strings.Builder
	b.WriteString("Overall task:\n" + task + "\n\n")
	if contract != "" {
		b.WriteString("Technical contract (shared file paths, IDs, CSS classes and function signatures — follow these exactly):\n" + contract + "\n\n")
	}
	b.WriteString("Current workspace files: " + strings.Join(names, ", ") + "\n\n")
	if doneText != "" {
		b.WriteString("Work completed so far (reuse these files and names, do not recreate them):\n" + doneText + "\n\n")
	}
	b.WriteString("Your task:\n" + jobText + "\n\n")
	b.WriteString("When you finish, summarize exactly what you created or changed and how later steps should reuse it.")
	return b.String()
}

// redecomposeAndMerge is called by the worker chosen to re-decompose blocked
// work. It snapshots the still-blocked jobs, asks the orchestrator to split them
// into finer parallel subtasks, and replaces the blocked jobs with the result so
// the idle agents have work again.
func (s *Swarm) redecomposeAndMerge(ctx context.Context, orch *llm.Gemini, st *execState, task, contract string) {
	st.mu.Lock()
	var remaining []string
	for id, j := range st.jobs {
		if !st.finished[id] && st.depCount[id] > 0 {
			remaining = append(remaining, j.text)
		}
	}
	doneText := st.doneLog.String()
	st.splitRounds++
	st.mu.Unlock()

	if len(remaining) == 0 {
		st.mu.Lock()
		st.splitting = false
		st.cond.Broadcast()
		st.mu.Unlock()
		return
	}

	plan, deps, usage, err := s.redecompose(ctx, orch, task, contract, remaining, doneText, st.agents)

	st.mu.Lock()
	st.splitting = false
	if err != nil || len(plan) == 0 {
		st.cond.Broadcast()
		st.mu.Unlock()
		if err != nil {
			s.emit(Event{Time: time.Now(), Type: "warning", Detail: "re-decomposition failed: " + truncate(err.Error(), 160)})
		}
		return
	}
	st.usage = append(st.usage, UsageRecord{Agent: "orchestrator", Model: orch.Model(), Usage: usage})
	st.replaceBlocked(plan, deps)
	st.cond.Broadcast()
	st.mu.Unlock()

	s.emit(Event{Time: time.Now(), Type: "usage", Agent: "orchestrator", Model: orch.Model(), Usage: usage})
	s.emit(Event{Time: time.Now(), Type: "plan", Detail: fmt.Sprintf("remaining work re-decomposed into %d parallel steps", len(plan))})
}

// replaceBlocked drops every blocked (not-yet-runnable, unfinished) job and
// replaces them with a freshly re-decomposed plan. It is only called when no job
// is running, so the surviving jobs are exactly the finished ones.
func (st *execState) replaceBlocked(plan []string, deps map[int][]int) {
	for id := range st.jobs {
		if !st.finished[id] {
			delete(st.jobs, id)
		}
	}
	st.depCount = map[int]int{}
	st.dependents = map[int][]int{}
	st.ready = nil
	st.running = 0

	base := st.nextID
	for i, p := range plan {
		st.jobs[base+i] = &job{id: base + i, text: p}
	}
	st.nextID = base + len(plan)

	for i := range plan {
		id := base + i
		for _, d := range deps[i] {
			if d < 0 || d >= len(plan) || d == i {
				continue
			}
			dep := base + d
			st.jobs[id].deps = append(st.jobs[id].deps, dep)
			st.depCount[id]++
			st.dependents[dep] = append(st.dependents[dep], id)
		}
		if st.depCount[id] == 0 {
			st.ready = append(st.ready, id)
		}
	}
}

// redecompose asks the orchestrator to break a set of remaining (coarse or
// sequential) work items into finer, maximally-parallel subtasks. It returns the
// new plan and its dependencies (0-based within the new list).
func (s *Swarm) redecompose(ctx context.Context, orch *llm.Gemini, task, contract string, remaining []string, done string, minSubtasks int) ([]string, map[int][]int, llm.Usage, error) {
	var b strings.Builder
	b.WriteString("You are the orchestrator of a swarm of AI agents.\n")
	b.WriteString("Overall task:\n" + task + "\n\n")
	if contract != "" {
		b.WriteString("Shared technical contract:\n" + contract + "\n\n")
	}
	if done != "" {
		b.WriteString("Already completed (reuse these files and names, do not recreate them):\n" + done + "\n\n")
	}
	b.WriteString("The remaining work below is too coarse or too sequential. Re-decompose it into finer subtasks so the swarm can work in parallel:\n")
	for i, r := range remaining {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, r))
	}
	b.WriteString(fmt.Sprintf("\nThere are %d agents that must all stay busy. Split the remaining work into at least %d independent subtasks that can run concurrently, up to as many as genuinely make sense. Chain one subtask after another ONLY when it truly needs the earlier one's output.\n", len(s.cfg.Agents), minSubtasks))
	b.WriteString("Return ONLY valid JSON — no markdown, no code fences, no extra text — in this shape:\n{\"plan\":[\"subtask 0\",...],\"dependencies\":{\"<index>\":[<prerequisite indices>]}}\n")
	b.WriteString("Indices are 0-based within this new list. Independent subtasks need no \"dependencies\" entry.\n")

	res, err := orch.Generate(ctx, "You are the orchestrator of a swarm of AI agents.", []llm.Turn{{Role: "user", Text: b.String()}}, nil)
	if err != nil {
		return nil, nil, llm.Usage{}, err
	}
	o, err := parseOrchestration(res.Text)
	if err != nil {
		return nil, nil, llm.Usage{}, err
	}
	if o.Direct || len(o.Plan) == 0 {
		return nil, nil, llm.Usage{}, fmt.Errorf("re-decomposition returned no plan")
	}
	if o.Dependencies == nil {
		o.Dependencies = map[int][]int{}
	}
	return o.Plan, o.Dependencies, res.Usage, nil
}

// expand re-decomposes a coarse initial plan (fewer subtasks than agents) into
// enough parallel subtasks for every agent, before any execution starts.
func (s *Swarm) expand(ctx context.Context, orch *llm.Gemini, task, contract string, plan []string, agents []Agent) ([]string, map[int][]int, llm.Usage, error) {
	return s.redecompose(ctx, orch, task, contract, plan, "", len(agents))
}

// redecomposeBudget caps how many re-decomposition rounds a single run may make,
// scaling with the number of agents but bounded so a stuck planner cannot loop.
func redecomposeBudget(nAgents int) int {
	if nAgents < 1 {
		nAgents = 1
	}
	b := nAgents + 2
	if b > 12 {
		b = 12
	}
	return b
}

// fileNames lists the current workspace file paths, used to tell workers and the
// re-planner what already exists.
func (s *Swarm) fileNames() []string {
	if s.cfg.Workspace == nil {
		return nil
	}
	files, _ := s.cfg.Workspace.ListFiles()
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Path)
	}
	return names
}

// finalize produces the final answer for a swarm run by summarising the
// subtask results with a single agent (no tools). It returns the text, the
// model used, and the token usage.
func (s *Swarm) finalize(ctx context.Context, a Agent, task string, subs []SubtaskResult) (string, string, llm.Usage, error) {
	var sb strings.Builder
	for _, sr := range subs {
		if sr.Error != "" {
			sb.WriteString(fmt.Sprintf("- [%s / %s] %s: ERROR: %s\n", sr.Agent, sr.Model, sr.Task, truncate(sr.Error, 120)))
			continue
		}
		sb.WriteString(fmt.Sprintf("- [%s / %s] %s: %s\n", sr.Agent, sr.Model, sr.Task, truncate(sr.Summary, 160)))
	}

	prompt := fmt.Sprintf(
		"You are the final summarizer for a swarm of coding agents.\n"+
			"Overall task:\n%s\n\n"+
			"What the agents did:\n%s\n\n"+
			"Write a concise, human-readable answer to the user describing what was done and the result. Mention any files created and how to use them. Reply in the same language as the task.",
		task, sb.String())

	client, err := s.buildClient(a, s.cfg.DefaultModel)
	if err != nil {
		return "", "", llm.Usage{}, err
	}
	res, err := client.Generate(ctx, "You are the final summarizer for a swarm of coding agents.", []llm.Turn{{Role: "user", Text: prompt}}, nil)
	if err != nil {
		return "", "", llm.Usage{}, err
	}
	return res.Text, client.Model(), res.Usage, nil
}

// RunSingle runs a turn through a single agent with the full conversation
// history (single-agent mode). No planning, no fan-out: the one agent answers
// or works directly, like a normal chat assistant with tools.
func (s *Swarm) RunSingle(ctx context.Context, a Agent, history []llm.Turn) (*Result, error) {
	res := &Result{Task: lastUserText(history)}

	if s.cfg.Workspace != nil {
		defer s.cfg.Workspace.CleanupProcesses()
	}

	model := resolveModel(a, nil, s.cfg.DefaultModel)
	s.emit(Event{Time: time.Now(), Type: "planning", Agent: a.Name, Model: model, Detail: "single agent answering"})

	agRes, usedModel, err := s.runWorker(ctx, a, model, "", history)
	if err != nil {
		return nil, err
	}
	res.Final = agRes.Summary
	res.Subtasks = []SubtaskResult{{
		Task:      res.Task,
		Agent:     a.Name,
		Model:     usedModel,
		Summary:   agRes.Summary,
		ToolCalls: agRes.ToolCalls,
		Files:     agRes.Files,
		Usage:     agRes.Usage,
	}}
	res.Usage = append(res.Usage, UsageRecord{Agent: a.Name, Model: usedModel, Usage: agRes.Usage})
	res.Transcript = append(res.Transcript, TranscriptEntry{Agent: a.Name, Role: "assistant", Text: agRes.Summary})
	if s.cfg.Workspace != nil {
		res.Files, _ = s.cfg.Workspace.ListFiles()
	}
	return res, nil
}

// lastUserText returns the text of the most recent user turn, or "" if none.
func lastUserText(history []llm.Turn) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[i].Text
		}
	}
	return ""
}

// maxSubtasks is the ceiling on how many subtasks the orchestrator may plan for
// a given number of agents. It leaves room to exploit parallelism on genuinely
// parallelizable work, but the orchestrator may plan as few as one subtask (or
// answer directly) when the task doesn't warrant splitting.
func maxSubtasks(nAgents int) int {
	if nAgents < 1 {
		nAgents = 1
	}
	n := 2 * nAgents
	if n < 4 {
		n = 4
	}
	return n
}

func (s *Swarm) emit(e Event) {
	if s.cfg.Emitter != nil {
		s.cfg.Emitter(e)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (s *Swarm) plan(ctx context.Context, orch *llm.Gemini, task string, images []llm.Image, prior []llm.Turn) (orchestration, llm.Usage, error) {
	names := make([]string, 0, len(s.cfg.Agents))
	for _, a := range s.cfg.Agents {
		names = append(names, a.Name)
	}
	// The orchestrator may answer trivially short tasks itself and may plan as
	// few as one subtask: the swarm only fans out when the task really needs it,
	// so a greeting isn't forced through a full multi-agent run.
	max := maxSubtasks(len(names))
	prompt := fmt.Sprintf(
		"You are the orchestrator of a swarm of AI agents.\n"+
			"Agents: %s\n"+
			"Available models: %s\n\n"+
			"Task:\n%s\n\n"+
			"First decide whether this task needs a swarm at all.\n"+
			"- If the task is trivial or conversational — a greeting, small talk, a simple question, or anything a single assistant can answer directly with no tools, no files and no multi-step work — return ONLY:\n"+
			"  {\"direct\": true, \"answer\": \"<your reply, in the same language as the task>\"}\n"+
			"- Otherwise break the task into concrete subtasks. KEEP EVERY AGENT BUSY: split the work into at least %d independent subtasks (one per agent) and more — up to %d — wherever the task naturally decomposes further. Prefer subtasks that can run in parallel; chain a subtask after another ONLY when it truly needs the earlier one's output, and record that ordering in \"dependencies\". Never serialize work that could be parallel.\n"+
			"  Write a short technical CONTRACT that every subtask must obey: the exact file paths to create, shared identifiers (IDs, CSS class names), and function/API signatures, so all subtasks integrate cleanly and never collide or diverge.\n"+
			"  Choose the best model for each agent in \"assignments\".\n"+
			"  Return ONLY:\n"+
			"  {\"plan\": [\"subtask 0\", ...], \"contract\": \"<shared paths, IDs, classes, signatures>\", \"assignments\": {\"<agent name>\": \"<model>\"}, \"dependencies\": {\"<subtask index>\": [<prerequisite indices>]}}.\n"+
			"  The \"dependencies\" field maps a subtask's index (as a string) to the list of indices it must wait for. Independent subtasks have no entry, so \"dependencies\" may be {}.\n"+
			"No markdown, no code fences, no extra text.",
		strings.Join(names, ", "), strings.Join(s.cfg.AvailableModels, ", "), task, len(names), max)

	if contextText := formatHistory(prior); contextText != "" {
		prompt += "\n\nConversation so far (context only — use it to plan, do not re-answer these messages):\n" + contextText
	}

	res, err := orch.Generate(ctx, "You are the orchestrator of a swarm of AI agents.", []llm.Turn{{Role: "user", Text: prompt, Images: images}}, nil)
	if err != nil {
		return orchestration{}, llm.Usage{}, err
	}
	o, err := parseOrchestration(res.Text)
	return o, res.Usage, err
}

// formatHistory renders prior conversation turns as a compact text block for
// the orchestrator, so a chat session's context is handed off to the swarm.
func formatHistory(history []llm.Turn) string {
	if len(history) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, t := range history {
		name := "User"
		if t.Role == "model" || t.Role == "assistant" {
			name = "Assistant"
		}
		sb.WriteString(name)
		sb.WriteString(": ")
		sb.WriteString(truncate(strings.TrimSpace(t.Text), 500))
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

func (s *Swarm) buildClient(a Agent, model string) (*llm.Gemini, error) {
	cfg := llm.Config{
		BaseURL:        a.BaseURL,
		APIKey:         a.APIKey,
		Model:          model,
		FallbackModels: downgradeFallbacks(model, s.cfg.AvailableModels),
		Proxy:          a.Proxy,
	}
	if s.cfg.TransportCache != nil {
		tr, err := s.cfg.TransportCache.Get(a.Proxy)
		if err != nil {
			return nil, err
		}
		cfg.Transport = tr
	}
	return llm.New(cfg)
}

// downgradeFallbacks orders the available models so that, when the primary is
// exhausted, the client retries lightweight models first (e.g. a "-lite" model)
// before any others. The primary itself is excluded.
func downgradeFallbacks(primary string, available []string) []string {
	seen := map[string]bool{primary: true}
	var lite, rest []string
	for _, m := range available {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		if strings.Contains(m, "lite") {
			lite = append(lite, m)
		} else {
			rest = append(rest, m)
		}
	}
	out := make([]string, 0, len(lite)+len(rest))
	out = append(out, lite...)
	out = append(out, rest...)
	return out
}

// runWorker runs the agent loop, trying the assigned model first and then
// falling back through the other available models if one is overloaded or
// unavailable, so a single saturated model cannot stall the whole run. Each
// client also downgrades in-request on 429 quota exhaustion (see llm.Gemini).
func (s *Swarm) runWorker(ctx context.Context, a Agent, model, systemExtra string, history []llm.Turn) (agent.Result, string, error) {
	var lastErr error
	for _, m := range modelChain(model, s.cfg.DefaultModel, s.cfg.AvailableModels) {
		client, err := s.buildClient(a, m)
		if err != nil {
			lastErr = err
			continue
		}
		ag := agent.New(agent.Config{
			LLM:           client,
			Workspace:     s.cfg.Workspace,
			Browser:       s.cfg.Browser,
			Desktop:       s.cfg.Desktop,
			Log:           s.workerLog(a, client),
			OnUsage:       s.usageLog(a, client),
			DisabledTools: agent.DisabledToolNames(s.cfg.DisabledModules),
			SystemExtra:   systemExtra,
		})
		res, err := ag.RunWithHistory(ctx, history)
		if err == nil {
			return res, client.Model(), nil
		}
		lastErr = err
	}
	return agent.Result{}, model, lastErr
}

// modelChain returns a deduped, ordered list of models to try: the assigned
// model first, then the default, then the rest of the available models.
func modelChain(assigned, def string, available []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range append([]string{assigned, def}, available...) {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// workerLog adapts an agent's tool-call log into swarm progress events, reading
// the client's current model so downgraded requests are attributed correctly.
func (s *Swarm) workerLog(a Agent, client *llm.Gemini) func(kind, tool, detail string) {
	return func(kind, tool, detail string) {
		e := Event{Time: time.Now(), Type: kind, Agent: a.Name, Model: client.Model(), Tool: tool, Detail: detail}
		if kind == "tool" {
			e.Direction = DirectionFor(tool)
		}
		s.emit(e)
	}
}

// DirectionFor classifies a tool call as a read (observing the world) or a
// write (mutating it), for the activity feed.
func DirectionFor(tool string) string {
	switch tool {
	case "read_file", "search_files", "list_files",
		"browser_navigate", "browser_snapshot", "browser_text", "browser_screenshot",
		"desktop_windows", "desktop_screenshot":
		return "read"
	case "write_file", "edit_file", "run_command",
		"desktop_launch", "desktop_focus", "desktop_move", "desktop_click",
		"desktop_type", "desktop_key", "desktop_scroll":
		return "write"
	default:
		return "other"
	}
}

// usageLog emits a per-call usage event so token spend is visible in real time.
func (s *Swarm) usageLog(a Agent, client *llm.Gemini) func(llm.Usage) {
	return func(u llm.Usage) {
		s.emit(Event{Time: time.Now(), Type: "usage", Agent: a.Name, Model: client.Model(), Usage: u})
	}
}

func resolveModel(a Agent, assignments map[string]string, defaultModel string) string {
	if a.ManualModel != "" {
		return a.ManualModel
	}
	if m, ok := assignments[a.Name]; ok && m != "" {
		return m
	}
	if m, ok := assignments[a.Slug]; ok && m != "" {
		return m
	}
	return defaultModel
}

// orchestration is the decoded orchestrator output: either a full subtask plan
// or, for trivial tasks, a direct answer that needs no workers at all.
type orchestration struct {
	Plan         []string          `json:"plan"`
	Contract     string            `json:"contract"`
	Assignments  map[string]string `json:"assignments"`
	Dependencies map[int][]int     `json:"dependencies"`
	Direct       bool              `json:"direct"`
	Answer       string            `json:"answer"`
}

func parseOrchestration(raw string) (orchestration, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return orchestration{}, fmt.Errorf("no JSON object found in orchestrator output")
	}

	var o orchestration
	if err := json.Unmarshal([]byte(s[start:end+1]), &o); err != nil {
		return orchestration{}, fmt.Errorf("decode orchestrator output: %w", err)
	}
	if o.Direct {
		if strings.TrimSpace(o.Answer) == "" {
			return orchestration{}, fmt.Errorf("direct answer is empty")
		}
		return o, nil
	}
	if len(o.Plan) == 0 {
		return orchestration{}, fmt.Errorf("empty plan")
	}
	if o.Assignments == nil {
		o.Assignments = map[string]string{}
	}
	return o, nil
}
