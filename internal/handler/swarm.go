package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kenosm/internal/browser"
	"kenosm/internal/llm"
	"kenosm/internal/repository"
	"kenosm/internal/swarm"
	"kenosm/internal/workspace"
)

// runTimeout caps how long a single background swarm run may execute.
const runTimeout = 30 * time.Minute

// Swarm exposes the multi-agent orchestration endpoint. Agents are the enabled
// gateway services (each a Gemini endpoint + its SOCKS5 proxy).
type Swarm struct {
	repo              *repository.ServiceRepo
	usage             *repository.UsageRepo
	orchestratorModel string
	defaultModel      string
	availableModels   []string
	ws                *workspace.Workspace
	browser           *browser.Browser
	registry          *runRegistry
}

func NewSwarm(repo *repository.ServiceRepo, usage *repository.UsageRepo, orchestratorModel, defaultModel string, availableModels []string, ws *workspace.Workspace, br *browser.Browser) *Swarm {
	return &Swarm{
		repo:              repo,
		usage:             usage,
		orchestratorModel: orchestratorModel,
		defaultModel:      defaultModel,
		availableModels:   availableModels,
		ws:                ws,
		browser:           br,
		registry:          newRunRegistry(),
	}
}

// Run starts a swarm run in the background and returns its run ID immediately.
// Progress is then streamed via Events over Server-Sent Events.
func (h *Swarm) Run(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Task   string `json:"task"`
		Images []struct {
			MimeType string `json:"mime_type"`
			Data     string `json:"data"` // base64-encoded bytes, optionally a full data URL
		} `json:"images"`
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

	services, err := h.repo.ListWithSecrets(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(services) == 0 {
		writeError(w, http.StatusConflict, "no agents configured: register at least one service first")
		return
	}

	runID, state := h.registry.create()
	state.add(swarm.Event{Time: time.Now(), Type: "start", Detail: "run started"})

	// The emitter records every event to the live stream AND persists each usage
	// event to the DB immediately, so the stats panel updates while the run is
	// still executing (not just after it finishes).
	task := truncateText(in.Task, 200)
	emitter := func(e swarm.Event) {
		state.add(e)
		if e.Type == "usage" {
			h.insertUsage(runID, task, e)
		}
	}

	sw := swarm.New(swarm.Config{
		Agents:            swarm.AgentsFromServices(services),
		OrchestratorModel: h.orchestratorModel,
		DefaultModel:      h.defaultModel,
		AvailableModels:   h.availableModels,
		Workspace:         h.ws,
		Browser:           h.browser,
		Emitter:           emitter,
	})

	// Use a fresh context so the run survives the request returning, and wire
	// its cancel into the registry synchronously so Stop works even if called
	// immediately after the run starts.
	ctx, cancel := context.WithCancel(context.Background())
	state.setCancel(cancel)

	go func() {
		defer cancel()
		runCtx, cancelRun := context.WithTimeout(ctx, runTimeout)
		defer cancelRun()

		result, err := sw.Run(runCtx, in.Task, images)
		if err != nil {
			if state.isStopped() {
				return // the Stop handler already emitted a terminal "stopped" event
			}
			state.add(swarm.Event{Time: time.Now(), Type: "error", Error: err.Error()})
			return
		}
		state.add(swarm.Event{Time: time.Now(), Type: "done", Result: result})
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": runID})
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
