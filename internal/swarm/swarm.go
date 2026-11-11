package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	Assignments map[string]string    `json:"assignments"`
	Subtasks    []SubtaskResult      `json:"subtasks"`
	Files       []workspace.FileInfo `json:"files"`
	Final       string               `json:"final"`
	Transcript  []TranscriptEntry    `json:"transcript"`
	Usage       []UsageRecord        `json:"usage"`
}

// Run orchestrates the swarm: the orchestrator plans the task and picks a model
// for each agent, then each worker (with tools) builds and tests files in the
// shared workspace, sequentially so they build on each other's work. Progress
// is emitted as Events via cfg.Emitter (if set).
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

	plan, assignments, orchUsage, err := s.plan(ctx, orch, task, images)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}
	res.Plan = plan
	res.Assignments = assignments
	res.Transcript = append(res.Transcript, TranscriptEntry{Agent: "orchestrator", Role: "planner", Text: strings.Join(plan, "\n")})
	res.Usage = append(res.Usage, UsageRecord{Agent: "orchestrator", Model: s.cfg.OrchestratorModel, Usage: orchUsage})
	s.emit(Event{Time: time.Now(), Type: "usage", Agent: "orchestrator", Model: s.cfg.OrchestratorModel, Usage: orchUsage})

	s.emit(Event{Time: time.Now(), Type: "plan", Detail: fmt.Sprintf("%d subtasks planned", len(plan))})

	for i, sub := range plan {
		a := agents[i%len(agents)]
		model := resolveModel(a, assignments, s.cfg.DefaultModel)

		files, _ := s.cfg.Workspace.ListFiles()
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Path)
		}

		prompt := fmt.Sprintf("Overall task:\n%s\n\nFull plan:\n- %s\n\nCurrent workspace files: %s\n\nYour subtask (%d of %d):\n%s",
			task, strings.Join(plan, "\n- "), strings.Join(names, ", "), i+1, len(plan), sub)

		s.emit(Event{Time: time.Now(), Type: "subtask", Agent: a.Name, Model: model, Detail: fmt.Sprintf("subtask %d/%d: %s", i+1, len(plan), truncate(sub, 140))})

		agRes, usedModel, aerr := s.runWorker(ctx, a, model, prompt, images)

		sr := SubtaskResult{Task: sub, Agent: a.Name, Model: usedModel}
		if aerr != nil {
			sr.Error = aerr.Error()
			s.emit(Event{Time: time.Now(), Type: "subtask_done", Agent: a.Name, Model: usedModel, Detail: "error: " + truncate(aerr.Error(), 240)})
		} else {
			sr.Summary = agRes.Summary
			sr.ToolCalls = agRes.ToolCalls
			sr.Usage = agRes.Usage
			s.emit(Event{Time: time.Now(), Type: "subtask_done", Agent: a.Name, Model: usedModel, Detail: truncate(agRes.Summary, 240)})
		}
		res.Subtasks = append(res.Subtasks, sr)
		res.Usage = append(res.Usage, UsageRecord{Agent: a.Name, Model: usedModel, Usage: agRes.Usage})
		s.emit(Event{Time: time.Now(), Type: "usage", Agent: a.Name, Model: usedModel, Usage: agRes.Usage})
	}

	files, _ := s.cfg.Workspace.ListFiles()
	res.Files = files

	var sb strings.Builder
	for _, sr := range res.Subtasks {
		sb.WriteString(fmt.Sprintf("[%s / %s] %s: %s\n", sr.Agent, sr.Model, sr.Task, sr.Summary))
	}
	res.Final = sb.String()

	return res, nil
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

func (s *Swarm) plan(ctx context.Context, orch *llm.Gemini, task string, images []llm.Image) ([]string, map[string]string, llm.Usage, error) {
	names := make([]string, 0, len(s.cfg.Agents))
	for _, a := range s.cfg.Agents {
		names = append(names, a.Name)
	}
	prompt := fmt.Sprintf(
		"You are the orchestrator of a swarm of AI agents.\n"+
			"Agents: %s\n"+
			"Available models: %s\n\n"+
			"Task:\n%s\n\n"+
			"Break the task into 3-6 concrete subtasks and choose the best model for each agent.\n"+
			"Return ONLY a JSON object of the form: {\"plan\": [\"...\"], \"assignments\": {\"<agent name>\": \"<model>\"}}.\n"+
			"No markdown, no code fences, no extra text.",
		strings.Join(names, ", "), strings.Join(s.cfg.AvailableModels, ", "), task)

	res, err := orch.Generate(ctx, "You are the orchestrator of a swarm of AI agents.", []llm.Turn{{Role: "user", Text: prompt, Images: images}}, nil)
	if err != nil {
		return nil, nil, llm.Usage{}, err
	}
	plan, assignments, err := parseOrchestration(res.Text)
	return plan, assignments, res.Usage, err
}

func (s *Swarm) buildClient(a Agent, model string) (*llm.Gemini, error) {
	return llm.New(llm.Config{
		BaseURL: a.BaseURL,
		APIKey:  a.APIKey,
		Model:   model,
		Proxy:   a.Proxy,
	})
}

// runWorker runs the agent loop with the assigned model, falling back to the
// default model if the assigned one fails (e.g. quota or availability).
func (s *Swarm) runWorker(ctx context.Context, a Agent, model, prompt string, images []llm.Image) (agent.Result, string, error) {
	client, err := s.buildClient(a, model)
	if err != nil {
		return agent.Result{}, model, err
	}
	ag := agent.New(agent.Config{LLM: client, Workspace: s.cfg.Workspace, Browser: s.cfg.Browser, Log: s.workerLog(a, model)})
	res, err := ag.Run(ctx, prompt, images)
	if err == nil {
		return res, model, nil
	}

	if model != s.cfg.DefaultModel {
		if fb, ferr := s.buildClient(a, s.cfg.DefaultModel); ferr == nil {
			fbAg := agent.New(agent.Config{LLM: fb, Workspace: s.cfg.Workspace, Browser: s.cfg.Browser, Log: s.workerLog(a, s.cfg.DefaultModel)})
			if fbRes, fberr := fbAg.Run(ctx, prompt, images); fberr == nil {
				return fbRes, s.cfg.DefaultModel, nil
			}
		}
	}
	return res, model, err
}

// workerLog adapts an agent's tool-call log into swarm progress events.
func (s *Swarm) workerLog(a Agent, model string) func(kind, detail string) {
	return func(kind, detail string) {
		s.emit(Event{Time: time.Now(), Type: kind, Agent: a.Name, Model: model, Detail: detail})
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

func parseOrchestration(raw string) ([]string, map[string]string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, nil, fmt.Errorf("no JSON object found in orchestrator output")
	}

	var parsed struct {
		Plan        []string          `json:"plan"`
		Assignments map[string]string `json:"assignments"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &parsed); err != nil {
		return nil, nil, fmt.Errorf("decode orchestrator output: %w", err)
	}
	if len(parsed.Plan) == 0 {
		return nil, nil, fmt.Errorf("empty plan")
	}
	if parsed.Assignments == nil {
		parsed.Assignments = map[string]string{}
	}
	return parsed.Plan, parsed.Assignments, nil
}
