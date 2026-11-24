package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kenosm/internal/llm"
	"kenosm/internal/repository"
	"kenosm/internal/swarm"
)

// Run modes for a turn. Single mode answers/works with one agent; swarm mode
// plans and fans out across the whole swarm, handing it the conversation so far.
const (
	modeSingle = "single"
	modeSwarm  = "swarm"
	modePolish = "polish"
)

// dbTimeout bounds chat-store writes so a slow DB cannot stall a request or a
// run's completion callback.
const dbTimeout = 5 * time.Second

// ListChats returns the chat list (summaries only, no messages).
func (h *Swarm) ListChats(w http.ResponseWriter, r *http.Request) {
	chats, err := h.chats.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

// CreateChat creates a new, empty chat in single-agent mode.
func (h *Swarm) CreateChat(w http.ResponseWriter, r *http.Request) {
	c, err := h.chats.Create(r.Context(), modeSingle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// GetChat returns one chat with all its messages.
func (h *Swarm) GetChat(w http.ResponseWriter, r *http.Request) {
	c, err := h.chats.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, repository.ErrChatNotFound) {
			writeError(w, http.StatusNotFound, "chat not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// DeleteChat removes a chat and all its messages.
func (h *Swarm) DeleteChat(w http.ResponseWriter, r *http.Request) {
	err := h.chats.Delete(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, repository.ErrChatNotFound) {
			writeError(w, http.StatusNotFound, "chat not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// SendMessage appends a user message to a chat and starts a turn (single or
// swarm, per the request's mode) with the conversation history as context. It
// returns a run ID so the caller can stream progress over SSE; when the run
// finishes the assistant's reply is appended to the chat.
func (h *Swarm) SendMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text   string `json:"text"`
		Mode   string `json:"mode"`
		Images []struct {
			MimeType string `json:"mime_type"`
			Data     string `json:"data"`
		} `json:"images"`
		ContextKB int    `json:"context_kb"` // context-window budget in KiB (0 = default)
		Reasoning string `json:"reasoning"`  // fast | normal | deep
		Refine    bool   `json:"refine"`     // rewrite the raw idea into a full task prompt first
		Agent     string `json:"agent"`      // service slug to run in single mode ("" = default/orchestrator picks)
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "message text is required")
		return
	}
	mode := in.Mode
	if mode != modeSingle && mode != modeSwarm && mode != modePolish {
		mode = modeSingle
	}
	images, err := parseImages(in.Images)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	chatID := chi.URLParam(r, "id")
	chat, err := h.chats.Get(r.Context(), chatID)
	if err != nil {
		if errors.Is(err, repository.ErrChatNotFound) {
			writeError(w, http.StatusNotFound, "chat not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	agents, err := h.loadAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if in.Agent != "" {
		var ok bool
		agents, ok = selectAgent(agents, in.Agent)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown agent: "+in.Agent)
			return
		}
		mode = modeSingle // a specific agent is always a single-agent turn
	}
	if h.runsDisabled() {
		writeError(w, http.StatusServiceUnavailable, "runs are disabled (MAX_CONCURRENT_RUNS=0)")
		return
	}
	if !h.tryAcquireRun() {
		writeError(w, http.StatusTooManyRequests, "too many concurrent runs")
		return
	}

	// Persist the user message; on the first message, also set the title (from
	// the message text) and remember the mode for the chat.
	userMsg, err := h.chats.AppendMessage(r.Context(), chatID, "user", text, mode)
	if err != nil {
		h.releaseRun()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(chat.Messages) == 0 {
		_ = h.chats.SetTitle(r.Context(), chatID, truncateText(text, 40))
	}
	_ = h.chats.SetMode(r.Context(), chatID, mode)

	// Prior turns are everything before the message we just appended.
	prior := chatHistoryToTurns(chat.Messages)

	// Prompt refinement: rewrite the raw idea into a detailed, self-contained
	// task before the swarm sees it (one orchestrator call, no workers).
	task := text
	refined := ""
	if in.Refine {
		refCtx, refCancel := context.WithTimeout(r.Context(), 3*time.Minute)
		p, perr := h.newSwarm(agents, nil, runOptions{}).GeneratePrompt(refCtx, text)
		refCancel()
		if perr != nil {
			h.releaseRun()
			writeError(w, http.StatusBadGateway, "prompt refinement failed: "+perr.Error())
			return
		}
		refined = p
		task = p
	}

	opts := runOptions{ContextBytes: in.ContextKB * 1024, ReasoningLevel: in.Reasoning}
	runID, state := h.runTurn(agents, task, images, prior, mode, opts, func(res *swarm.Result) {
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		_, _ = h.chats.AppendMessage(ctx, chatID, "assistant", res.Final, mode)
	}, func(errMsg string) {
		// Keep the conversation alternating even when a turn fails, and surface
		// the error in the chat itself.
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		_, _ = h.chats.AppendMessage(ctx, chatID, "assistant", "Error: "+errMsg, mode)
	}, func() {
		// A stopped turn keeps its context: the placeholder tells the next
		// message what happened, and the prior history (including this turn's
		// task) still feeds the next run — so edits continue where they left off.
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		_, _ = h.chats.AppendMessage(ctx, chatID, "assistant", "Stopped. — Context is preserved: send the next message and the run continues from where it left off.", mode)
	})

	if refined != "" {
		state.add(swarm.Event{Time: time.Now(), Type: "prompt_refined", Detail: refined})
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"run_id":     runID,
		"chat_id":    chatID,
		"message_id": userMsg.ID,
	})
}

// selectAgent returns the agents list narrowed to the single agent whose slug
// matches, so a chat can run a single-agent turn against a specific model. The
// second result reports whether a match was found.
func selectAgent(agents []swarm.Agent, slug string) ([]swarm.Agent, bool) {
	for _, a := range agents {
		if a.Slug == slug {
			return []swarm.Agent{a}, true
		}
	}
	return nil, false
}

// chatHistoryToTurns converts stored chat messages into LLM turns, mapping the
// assistant role to the model role Gemini expects in its history.
func chatHistoryToTurns(msgs []repository.ChatMessage) []llm.Turn {
	turns := make([]llm.Turn, 0, len(msgs))
	for _, m := range msgs {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		turns = append(turns, llm.Turn{Role: role, Text: m.Text})
	}
	return turns
}
