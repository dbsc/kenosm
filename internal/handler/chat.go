package handler

import (
	"context"
	"encoding/json"
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
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "message text is required")
		return
	}
	mode := in.Mode
	if mode != modeSingle && mode != modeSwarm {
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

	runID, _ := h.runTurn(agents, text, images, prior, mode, func(res *swarm.Result) {
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
		// A stopped turn leaves no reply; record a placeholder so the next
		// message still alternates user/assistant.
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		defer cancel()
		_, _ = h.chats.AppendMessage(ctx, chatID, "assistant", "Stopped.", mode)
	})

	writeJSON(w, http.StatusAccepted, map[string]string{
		"run_id":     runID,
		"chat_id":    chatID,
		"message_id": userMsg.ID,
	})
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
