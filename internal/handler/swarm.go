package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"kenosm/internal/agent"
	"kenosm/internal/browser"
	"kenosm/internal/desktop"
	"kenosm/internal/llm"
	"kenosm/internal/pricing"
	"kenosm/internal/repository"
	"kenosm/internal/swarm"
	"kenosm/internal/transport"
	"kenosm/internal/workspace"
)

// runTimeout caps how long a single background swarm run may execute.
const runTimeout = 30 * time.Minute

// Swarm exposes the multi-agent orchestration endpoint. Agents are the enabled
// gateway services (each a Gemini endpoint + its SOCKS5 proxy).
type Swarm struct {
	repo               *repository.ServiceRepo
	usage              *repository.UsageRepo
	orchestratorModel  string
	defaultModel       string
	availableModels    []string
	ws                 *workspace.Workspace
	browser            *browser.Browser
	desktop            *desktop.Desktop
	registry           *runRegistry
	chats              *repository.ChatRepo
	prices             pricing.Table
	activity           *repository.ActivityRepo
	transportCache     *transport.Cache
	mu                 sync.Mutex      // guards disabledModules
	disabledModules    map[string]bool // tool modules disabled at runtime
	sem                chan struct{}   // bounds concurrent runs (spawning browsers + model calls)
	polishMaxRounds    int             // max self-review rounds in polish mode
	agentMaxIterations int             // max agent tool-call turns per subtask
}

func NewSwarm(repo *repository.ServiceRepo, usage *repository.UsageRepo, chats *repository.ChatRepo, orchestratorModel, defaultModel string, availableModels []string, ws *workspace.Workspace, br *browser.Browser, dsk *desktop.Desktop, prices pricing.Table, activity *repository.ActivityRepo, tc *transport.Cache, disabledModules []string, maxConcurrent, polishMaxRounds, agentMaxIterations int) *Swarm {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if polishMaxRounds <= 0 {
		polishMaxRounds = 3
	}
	if agentMaxIterations <= 0 {
		agentMaxIterations = 50
	}
	return &Swarm{
		repo:               repo,
		usage:              usage,
		orchestratorModel:  orchestratorModel,
		defaultModel:       defaultModel,
		availableModels:    availableModels,
		ws:                 ws,
		browser:            br,
		desktop:            dsk,
		registry:           newRunRegistry(),
		chats:              chats,
		prices:             prices,
		activity:           activity,
		transportCache:     tc,
		disabledModules:    moduleSet(disabledModules),
		sem:                make(chan struct{}, maxConcurrent),
		polishMaxRounds:    polishMaxRounds,
		agentMaxIterations: agentMaxIterations,
	}
}

func moduleSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			m[n] = true
		}
	}
	return m
}

// tryAcquireRun reserves a slot for a concurrent run, returning false when the
// server is already at its configured limit. It bounds the number of concurrent
// agent swarms (each spawning browsers and model calls) and, with the default
// limit of one, keeps two runs from stomping on the shared workspace.
func (h *Swarm) tryAcquireRun() bool {
	select {
	case h.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *Swarm) releaseRun() {
	<-h.sem
}

// Run starts a background run (defaulting to swarm mode) and returns its run ID
// immediately. The chat endpoints are the primary interface now; this remains
// for compatibility and direct API use.
func (h *Swarm) Run(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Task   string `json:"task"`
		Images []struct {
			MimeType string `json:"mime_type"`
			Data     string `json:"data"` // base64-encoded bytes, optionally a full data URL
		} `json:"images"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Task) == "" {
		writeError(w, http.StatusBadRequest, "task is required")
		return
	}

	images, err := parseImages(in.Images)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	agents, err := h.loadAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !h.tryAcquireRun() {
		writeError(w, http.StatusTooManyRequests, "too many concurrent runs")
		return
	}

	mode := in.Mode
	if mode != modeSingle && mode != modeSwarm && mode != modePolish {
		mode = modeSwarm
	}
	runID, _ := h.runTurn(agents, in.Task, images, nil, mode, nil, nil, nil)

	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

// loadAgents returns the swarm agents for the enabled services, or an error if
// none are configured.
func (h *Swarm) loadAgents(ctx context.Context) ([]swarm.Agent, error) {
	services, err := h.repo.ListWithSecrets(ctx)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no agents configured: register at least one service first")
	}
	return swarm.AgentsFromServices(services), nil
}

// newSwarm builds a swarm instance with the handler's runtime configuration,
// shared by runTurn and the single-turn prompt generator.
func (h *Swarm) newSwarm(agents []swarm.Agent, emitter swarm.Emitter) *swarm.Swarm {
	return swarm.New(swarm.Config{
		Agents:            agents,
		OrchestratorModel: h.orchestratorModel,
		DefaultModel:      h.defaultModel,
		AvailableModels:   h.availableModels,
		DisabledModules:   h.disabledModuleList(),
		Workspace:         h.ws,
		Browser:           h.browser,
		Desktop:           h.desktop,
		Emitter:           emitter,
		TransportCache:    h.transportCache,
		PolishMaxRounds:   h.polishMaxRounds,
		MaxIterations:     h.agentMaxIterations,
	})
}

// GeneratePrompt rewrites a rough idea into a detailed, self-contained task
// prompt for the swarm, using the orchestrator's model. It is a single LLM call
// (no workers, no browsers) and blocks until the model responds.
func (h *Swarm) GeneratePrompt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Idea string `json:"idea"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Idea) == "" {
		writeError(w, http.StatusBadRequest, "idea is required")
		return
	}

	agents, err := h.loadAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	prompt, err := h.newSwarm(agents, nil).GeneratePrompt(ctx, in.Idea)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompt": prompt})
}

// runTurn starts a single- or swarm-mode turn in the background and returns its
// run ID. The emitter records every event to the live stream and persists each
// usage event to the DB immediately, so stats update while the run executes.
// onDone, if set, is called with the finished result right before the terminal
// "done" event is emitted (used to persist the assistant message into a chat).
// onError/onStop receive the turn's failure/stop so a chat can keep its
// user/assistant alternation intact.
func (h *Swarm) runTurn(agents []swarm.Agent, task string, images []llm.Image, prior []llm.Turn, mode string, onDone func(*swarm.Result), onError func(string), onStop func()) (string, *runState) {
	runID, state := h.registry.create()
	state.add(swarm.Event{Time: time.Now(), Type: "start", Detail: "run started"})

	label := truncateText(task, 200)
	emitter := func(e swarm.Event) {
		if e.Type == "usage" {
			e.Cost = h.prices.Cost(e.Model, e.Usage.PromptTokens, e.Usage.CompletionTokens, e.Usage.ThoughtTokens)
		}
		state.add(e)
		// Persist stats/activity off the critical path: a slow database must not
		// stall the agents between tool calls. e is captured by value, so these
		// goroutines never race the run.
		if e.Type == "usage" {
			go h.insertUsage(runID, label, e)
		} else if e.Type == "tool" || e.Type == "subtask" || e.Type == "subtask_done" || e.Type == "direct" {
			go h.insertActivity(runID, e)
		}
	}

	sw := h.newSwarm(agents, emitter)

	// A fresh context lets the run outlive the HTTP request; its cancel is wired
	// into the registry synchronously so Stop works even if called immediately.
	ctx, cancel := context.WithCancel(context.Background())
	state.setCancel(cancel)

	go func() {
		defer cancel()
		defer h.releaseRun()
		runCtx, cancelRun := context.WithTimeout(ctx, runTimeout)
		defer cancelRun()

		var result *swarm.Result
		var err error
		switch mode {
		case modeSingle:
			history := append(append([]llm.Turn{}, prior...), llm.Turn{Role: "user", Text: task, Images: images})
			result, err = sw.RunSingle(runCtx, agents[0], history)
		case modePolish:
			result, err = sw.RunPolish(runCtx, task, images, prior)
		default:
			result, err = sw.Run(runCtx, task, images, prior)
		}
		if err != nil {
			if state.isStopped() {
				// The Stop handler already emitted a terminal "stopped" event;
				// persist a placeholder reply so the chat keeps alternating.
				if onStop != nil {
					onStop()
				}
				return
			}
			msg := err.Error()
			if onError != nil {
				onError(msg)
			}
			state.add(swarm.Event{Time: time.Now(), Type: "error", Error: msg})
			return
		}
		if onDone != nil {
			onDone(result)
		}
		state.add(swarm.Event{Time: time.Now(), Type: "done", Result: result})
	}()

	return runID, state
}

// Stop cancels a running swarm run. The run's context is canceled, the agent
// loop aborts, and a terminal "stopped" event is emitted.
func (h *Swarm) Stop(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	state := h.registry.get(runID)
	if state == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if state.stop() {
		state.add(swarm.Event{Time: time.Now(), Type: "stopped", Detail: "run stopped by user"})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
}

// parseImages decodes the request's inline images (base64 or data URLs) into
// llm.Image values for multimodal first-turn input.
func parseImages(in []struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}) ([]llm.Image, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]llm.Image, 0, len(in))
	for _, im := range in {
		data := im.Data
		if i := strings.Index(data, ","); i >= 0 { // strip "data:image/png;base64," prefix
			data = data[i+1:]
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("invalid image data: %v", err)
		}
		mime := im.MimeType
		if mime == "" {
			mime = "image/png"
		}
		out = append(out, llm.Image{MimeType: mime, Data: raw})
	}
	return out, nil
}

// insertUsage persists one usage event (orchestrator planning or a subtask) to
// the DB so stats are available live, during the run.
func (h *Swarm) insertUsage(runID, task string, e swarm.Event) {
	if h.usage == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.usage.Insert(ctx, repository.UsageEntry{
		RunID:            runID,
		Task:             task,
		Agent:            e.Agent,
		Model:            e.Model,
		PromptTokens:     e.Usage.PromptTokens,
		CompletionTokens: e.Usage.CompletionTokens,
		ThoughtTokens:    e.Usage.ThoughtTokens,
		TotalTokens:      e.Usage.TotalTokens,
		Cost:             e.Cost,
	})
}

// insertActivity persists one activity event (a read/write tool call, a subtask
// start/end, or a model's text answer) to the DB so the dashboard can replay the
// run's feed and timeline.
func (h *Swarm) insertActivity(runID string, e swarm.Event) {
	if h.activity == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	direction := e.Direction
	switch e.Type {
	case "subtask":
		direction = "task"
	case "subtask_done":
		direction = "task_done"
	case "direct":
		direction = "text"
	}

	_ = h.activity.Insert(ctx, repository.ActivityEntry{
		RunID:     runID,
		Agent:     e.Agent,
		Model:     e.Model,
		Direction: direction,
		Tool:      e.Tool,
		Detail:    e.Detail,
		Step:      e.Step,
		Wave:      e.Wave,
	})
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Events streams the progress of one run as Server-Sent Events. Each event is
// a JSON object on a `data:` line; the stream ends with a `done` or `error`
// event carrying the final result.
func (h *Swarm) Events(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	state := h.registry.get(runID)
	if state == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := state.subscribe()
	defer state.unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, flusher, e)
			if e.Type == "done" || e.Type == "error" || e.Type == "stopped" {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, e swarm.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func (h *Swarm) Models(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"orchestrator_model": h.orchestratorModel,
		"default_model":      h.defaultModel,
		"available_models":   h.availableModels,
	})
}

// moduleView is one tool module as exposed to the frontend, augmented with its
// runtime availability (browser/desktop may be absent) and current toggle.
type moduleView struct {
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	Tools     []string `json:"tools"`
	Optional  bool     `json:"optional"`
	Available bool     `json:"available"`
	Enabled   bool     `json:"enabled"`
}

// Modules returns the tool modules with their current enabled/available state.
func (h *Swarm) Modules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.moduleViews())
}

// SetModule enables or disables a tool module at runtime. The change applies to
// runs started afterwards and resets to the DISABLED_MODULES default on restart.
func (h *Swarm) SetModule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if !isKnownModule(name) {
		writeError(w, http.StatusBadRequest, "unknown module: "+name)
		return
	}
	h.mu.Lock()
	if in.Enabled {
		delete(h.disabledModules, name)
	} else {
		h.disabledModules[name] = true
	}
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, h.moduleViews())
}

func (h *Swarm) moduleViews() []moduleView {
	h.mu.Lock()
	defer h.mu.Unlock()
	views := make([]moduleView, 0, 4)
	for _, m := range agent.Modules() {
		available := true
		switch m.Name {
		case "browser":
			available = h.browser != nil
		case "desktop":
			available = h.desktop != nil
		}
		views = append(views, moduleView{
			Name:      m.Name,
			Label:     m.Label,
			Tools:     m.Tools,
			Optional:  m.Optional,
			Available: available,
			Enabled:   available && !h.disabledModules[m.Name],
		})
	}
	return views
}

func (h *Swarm) disabledModuleList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.disabledModules))
	for name := range h.disabledModules {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func isKnownModule(name string) bool {
	for _, m := range agent.Modules() {
		if m.Name == name {
			return true
		}
	}
	return false
}
