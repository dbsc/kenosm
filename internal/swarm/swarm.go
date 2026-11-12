package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"kenosm/internal/agent"
	"kenosm/internal/browser"
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
	Workspace         *workspace.Workspace
	Browser           *browser.Browser // optional; enables the screenshot tool
	Emitter           Emitter          // optional progress-event sink
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

// Run orchestrates the swarm: the orchestrator plans the task and picks a model
// for each agent, then workers (with tools) build and test files in the shared
// workspace. Subtasks that the orchestrator marks as independent (via a
// "dependencies" map) run concurrently across agents, one subtask per agent at
// a time; dependent subtasks run after their prerequisites finish. Progress is
// emitted as Events via cfg.Emitter (if set).
func (s *Swarm) Run(ctx context.Context, task string, images []llm.Image) (*Result, error) {
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

	plan, contract, assignments, deps, orchUsage, err := s.plan(ctx, orch, task, images)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}
	res.Plan = plan
	res.Contract = contract
	res.Assignments = assignments
	res.Transcript = append(res.Transcript, TranscriptEntry{Agent: "orchestrator", Role: "planner", Text: strings.Join(plan, "\n")})
	orchModel := orch.Model()
	res.Usage = append(res.Usage, UsageRecord{Agent: "orchestrator", Model: orchModel, Usage: orchUsage})
	s.emit(Event{Time: time.Now(), Type: "usage", Agent: "orchestrator", Model: orchModel, Usage: orchUsage})

	s.emit(Event{Time: time.Now(), Type: "plan", Detail: fmt.Sprintf("%d subtasks planned", len(plan))})

	// If the orchestrator gave no dependency info, fall back to fully
	// sequential (each subtask depends on the previous) — preserves the
	// original "build on each other's work" behaviour.
	if deps == nil {
		deps = map[int][]int{}
		for i := 1; i < len(plan); i++ {
			deps[i] = []int{i - 1}
		}
	}

	n := len(plan)
	subResults := make([]SubtaskResult, n)
	subUsage := make([]UsageRecord, n)

	// The shared contract is injected into every subtask's system context so
	// agents agree on file paths, IDs, CSS classes and signatures up front.
	systemExtra := ""
	if contract != "" {
		systemExtra = "Technical contract for this task (shared file paths, IDs, CSS classes and function signatures — follow these exactly):\n" + contract
	}

	// One slot per agent: each key handles at most one subtask at a time, so
	// parallelism is bounded by (and spread across) the number of keys.
	slots := make(map[string]chan struct{}, len(agents))
	for _, a := range agents {
		slots[a.Slug] = make(chan struct{}, 1)
	}

	// done accumulates a running summary of completed waves (which files and
	// elements were created) so later subtasks reuse instead of duplicating.
	var done strings.Builder

	for waveIdx, wave := range computeWaves(n, deps) {
		var wg sync.WaitGroup
		for _, idx := range wave {
			a := agents[idx%len(agents)]
			model := resolveModel(a, assignments, s.cfg.DefaultModel)

			files, _ := s.cfg.Workspace.ListFiles()
			names := make([]string, 0, len(files))
			for _, f := range files {
				names = append(names, f.Path)
			}
			prompt := fmt.Sprintf("Overall task:\n%s\n\nFull plan:\n- %s\n\nCurrent workspace files: %s\n\nYour subtask (%d of %d):\n%s",
				task, strings.Join(plan, "\n- "), strings.Join(names, ", "), idx+1, n, plan[idx])
			if waveIdx > 0 {
				prompt += "\n\nWork completed in earlier waves (reuse these files and names, do not recreate them):\n" + done.String()
			}

			s.emit(Event{Time: time.Now(), Type: "subtask", Agent: a.Name, Model: model, Detail: fmt.Sprintf("subtask %d/%d: %s", idx+1, n, truncate(plan[idx], 140))})

			wg.Add(1)
			go func(i int, ag Agent, mdl, prmpt string) {
				defer wg.Done()
				slot := slots[ag.Slug]
				slot <- struct{}{}
				defer func() { <-slot }()

				agRes, usedModel, aerr := s.runWorker(ctx, ag, mdl, prmpt, systemExtra, images)

				sr := SubtaskResult{Task: plan[i], Agent: ag.Name, Model: usedModel, Files: agRes.Files}
				if aerr != nil {
					sr.Error = aerr.Error()
					s.emit(Event{Time: time.Now(), Type: "subtask_done", Agent: ag.Name, Model: usedModel, Detail: "error: " + truncate(aerr.Error(), 240)})
				} else {
					sr.Summary = agRes.Summary
					sr.ToolCalls = agRes.ToolCalls
					sr.Usage = agRes.Usage
					s.emit(Event{Time: time.Now(), Type: "subtask_done", Agent: ag.Name, Model: usedModel, Detail: truncate(agRes.Summary, 240)})
				}
				subResults[i] = sr
				subUsage[i] = UsageRecord{Agent: ag.Name, Model: usedModel, Usage: agRes.Usage}
			}(idx, a, model, prompt)
		}
		wg.Wait()

		// Summarize this wave for the next one: what each finished subtask
		// delivered and which files it touched.
		for _, idx := range wave {
			sr := subResults[idx]
			if sr.Error != "" {
				done.WriteString(fmt.Sprintf("- [%s] subtask %d FAILED: %s\n", sr.Agent, idx+1, truncate(sr.Error, 100)))
				continue
			}
			done.WriteString(fmt.Sprintf("- [%s] subtask %d (%s): %s (files: %s)\n",
				sr.Agent, idx+1, truncate(sr.Task, 60), truncate(sr.Summary, 120), strings.Join(sr.Files, ", ")))
		}
	}

	res.Subtasks = subResults
	res.Usage = append(res.Usage, subUsage...)

	files, _ := s.cfg.Workspace.ListFiles()
	res.Files = files

	var sb strings.Builder
	for _, sr := range res.Subtasks {
		sb.WriteString(fmt.Sprintf("[%s / %s] %s: %s\n", sr.Agent, sr.Model, sr.Task, sr.Summary))
	}
	res.Final = sb.String()

	return res, nil
}

// computeWaves groups subtask indices into dependency waves. A task with no
// prerequisites is in wave 0; a task whose prerequisites are in earlier waves
// lands in the first wave after them. Tasks within a wave are independent and
// may run concurrently. Cycles are broken defensively by treating a re-visited
// task as having no extra prerequisite.
func computeWaves(n int, deps map[int][]int) [][]int {
	level := make([]int, n)
	for i := range level {
		level[i] = -1
	}
	visiting := map[int]bool{}
	var compute func(i int) int
	compute = func(i int) int {
		if level[i] >= 0 {
			return level[i]
		}
		if visiting[i] {
			return 0
		}
		visiting[i] = true
		l := 0
		for _, d := range deps[i] {
			if d < 0 || d >= n || d == i {
				continue
			}
			if dl := compute(d); dl+1 > l {
				l = dl + 1
			}
		}
		delete(visiting, i)
		level[i] = l
		return l
	}

	maxL := 0
	for i := 0; i < n; i++ {
		if l := compute(i); l > maxL {
			maxL = l
		}
	}

	waves := make([][]int, maxL+1)
	for i := 0; i < n; i++ {
		waves[level[i]] = append(waves[level[i]], i)
	}
	return waves
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

func (s *Swarm) plan(ctx context.Context, orch *llm.Gemini, task string, images []llm.Image) ([]string, string, map[string]string, map[int][]int, llm.Usage, error) {
	names := make([]string, 0, len(s.cfg.Agents))
	for _, a := range s.cfg.Agents {
		names = append(names, a.Name)
	}
	// Ask for at least one subtask per agent so the round-robin actually uses
	// every configured agent (with few subtasks the later agents stay idle).
	lo, hi := len(names), 6
	if lo > hi {
		hi = lo
	}
	prompt := fmt.Sprintf(
		"You are the orchestrator of a swarm of AI agents.\n"+
			"Agents: %s\n"+
			"Available models: %s\n\n"+
			"Task:\n%s\n\n"+
			"Break the task into %d-%d concrete subtasks — at least one per agent, so every agent gets work — and choose the best model for each agent.\n"+
			"Subtasks that are independent (do not read each other's output) should run in parallel.\n"+
			"Also write a short technical CONTRACT that every subtask must obey: the exact file paths to create, shared identifiers (IDs, CSS class names), and function/API signatures, so all subtasks integrate cleanly and never collide or diverge.\n"+
			"Return ONLY a JSON object of the form:\n"+
			"{\"plan\": [\"subtask 0\", \"subtask 1\", ...], \"contract\": \"<shared paths, IDs, classes, signatures>\", \"assignments\": {\"<agent name>\": \"<model>\"}, \"dependencies\": {\"<subtask index>\": [<prerequisite indices>]}}.\n"+
			"The \"dependencies\" field maps a subtask's index (as a string) to the list of indices it must wait for. Independent subtasks have no entry, so \"dependencies\" may be {}.\n"+
			"No markdown, no code fences, no extra text.",
		strings.Join(names, ", "), strings.Join(s.cfg.AvailableModels, ", "), task, lo, hi)

	res, err := orch.Generate(ctx, "You are the orchestrator of a swarm of AI agents.", []llm.Turn{{Role: "user", Text: prompt, Images: images}}, nil)
	if err != nil {
		return nil, "", nil, nil, llm.Usage{}, err
	}
	plan, contract, assignments, deps, err := parseOrchestration(res.Text)
	return plan, contract, assignments, deps, res.Usage, err
}

func (s *Swarm) buildClient(a Agent, model string) (*llm.Gemini, error) {
	return llm.New(llm.Config{
		BaseURL:        a.BaseURL,
		APIKey:         a.APIKey,
		Model:          model,
		FallbackModels: downgradeFallbacks(model, s.cfg.AvailableModels),
		Proxy:          a.Proxy,
	})
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
func (s *Swarm) runWorker(ctx context.Context, a Agent, model, prompt, systemExtra string, images []llm.Image) (agent.Result, string, error) {
	var lastErr error
	for _, m := range modelChain(model, s.cfg.DefaultModel, s.cfg.AvailableModels) {
		client, err := s.buildClient(a, m)
		if err != nil {
			lastErr = err
			continue
		}
		ag := agent.New(agent.Config{
			LLM:         client,
			Workspace:   s.cfg.Workspace,
			Browser:     s.cfg.Browser,
			Log:         s.workerLog(a, client),
			OnUsage:     s.usageLog(a, client),
			SystemExtra: systemExtra,
		})
		res, err := ag.Run(ctx, prompt, images)
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
func (s *Swarm) workerLog(a Agent, client *llm.Gemini) func(kind, detail string) {
	return func(kind, detail string) {
		s.emit(Event{Time: time.Now(), Type: kind, Agent: a.Name, Model: client.Model(), Detail: detail})
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

func parseOrchestration(raw string) ([]string, string, map[string]string, map[int][]int, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, "", nil, nil, fmt.Errorf("no JSON object found in orchestrator output")
	}

	var parsed struct {
		Plan         []string          `json:"plan"`
		Contract     string            `json:"contract"`
		Assignments  map[string]string `json:"assignments"`
		Dependencies map[int][]int     `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &parsed); err != nil {
		return nil, "", nil, nil, fmt.Errorf("decode orchestrator output: %w", err)
	}
	if len(parsed.Plan) == 0 {
		return nil, "", nil, nil, fmt.Errorf("empty plan")
	}
	if parsed.Assignments == nil {
		parsed.Assignments = map[string]string{}
	}
	return parsed.Plan, parsed.Contract, parsed.Assignments, parsed.Dependencies, nil
}
