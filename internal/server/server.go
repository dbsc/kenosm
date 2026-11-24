package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"kenosm/internal/handler"
)

// New builds the HTTP router wiring admin CRUD, the proxy gateway, the swarm
// orchestrator, the workspace API and the frontend together. When apiKey is
// non-empty, every /api/v1/* route and the /proxy/* gateway require it.
func New(admin *handler.Admin, gateway *handler.Gateway, swarm *handler.Swarm, ws *handler.Workspace, stats *handler.Stats, activity *handler.Activity, stt *handler.Transcribe, voice *handler.Voice, static http.Handler, apiKey string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Everything that mutates state or spends the configured API keys sits behind
	// the auth wall. The static frontend and the health check stay open.
	r.Group(func(r chi.Router) {
		if apiKey != "" {
			r.Use(apiKeyAuth(apiKey))
		}

		r.Route("/api/v1/services", func(r chi.Router) {
			r.Post("/", admin.Create)
			r.Get("/", admin.List)
			r.Get("/{id}", admin.Get)
			r.Put("/{id}", admin.Update)
			r.Delete("/{id}", admin.Delete)
		})

		// Proxy gateway: /proxy/{slug} and everything under it.
		r.Handle("/proxy/{slug}", gateway)
		r.Handle("/proxy/{slug}/*", gateway)

		// Swarm orchestration.
		r.Post("/api/v1/swarm/run", swarm.Run)
		r.Get("/api/v1/swarm/run/{runID}/events", swarm.Events)
		r.Get("/api/v1/swarm/run/{runID}/browsers", swarm.Browsers)
		r.Get("/api/v1/swarm/latest-browsers", swarm.LatestBrowsers)
		r.Post("/api/v1/swarm/run/{runID}/stop", swarm.Stop)
		r.Get("/api/v1/models", swarm.Models)

		// Voice input: start/stop a server-side microphone capture (works in
		// the native WebKitGTK app, where getUserMedia is denied), and
		// transcribe a pre-recorded clip via STT_CMD.
		r.Post("/api/v1/transcribe", stt.Transcribe)
		r.Post("/api/v1/voice/start", voice.Start)
		r.Post("/api/v1/voice/stop", voice.Stop)

		// Prompt generator: rewrite a rough idea into a ready-to-run task prompt.
		r.Post("/api/v1/prompt/generate", swarm.GeneratePrompt)

		// Tool modules (toggleable groups of agent tools).
		r.Get("/api/v1/tools/modules", swarm.Modules)
		r.Put("/api/v1/tools/modules", swarm.SetModule)

		// Chat sessions (persistent conversations, single-agent or swarm turns).
		r.Get("/api/v1/chats", swarm.ListChats)
		r.Post("/api/v1/chats", swarm.CreateChat)
		r.Get("/api/v1/chats/{id}", swarm.GetChat)
		r.Delete("/api/v1/chats/{id}", swarm.DeleteChat)
		r.Post("/api/v1/chats/{id}/messages", swarm.SendMessage)

		// Workspace.
		r.Get("/api/v1/workspace/files", ws.List)
		r.Get("/api/v1/workspace/files/*", ws.Read)
		r.Get("/api/v1/workspace/tree", ws.Tree)
		r.Get("/api/v1/workspace/root", ws.Root)
		r.Post("/api/v1/workspace/root", ws.SetRoot)
		r.Post("/api/v1/workspace/clear", ws.Clear)
		r.Post("/api/v1/workspace/upload", ws.Upload)
		r.Get("/api/v1/fs/dirs", ws.ListDirs)
		r.Get("/api/v1/fs/home", ws.Home)

		// Usage statistics.
		r.Get("/api/v1/stats", stats.Get)

		// Activity feed (what agents read/wrote) and per-run parallel timeline.
		r.Get("/api/v1/activity", activity.List)
		r.Get("/api/v1/runs/{runID}/timeline", activity.Timeline)
	})

	// Frontend.
	r.Handle("/*", static)

	return r
}

// apiKeyAuth returns middleware that requires the configured bearer key. It is
// only applied when a key is configured, so an empty key keeps the service open
// (development mode).
func apiKeyAuth(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authorized(r, key) {
				unauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authorized reports whether the request carries the key via the Authorization
// bearer header, the X-API-Key header, or the "key" query parameter (the last is
// used by the frontend's EventSource, which cannot set request headers).
func authorized(r *http.Request, key string) bool {
	for _, got := range []string{bearerKey(r), r.Header.Get("X-API-Key"), r.URL.Query().Get("key")} {
		if subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

func bearerKey(r *http.Request) string {
	const prefix = "Bearer "
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}
