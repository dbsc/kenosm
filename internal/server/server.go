package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"kenosm/internal/handler"
)

// New builds the HTTP router wiring admin CRUD, the proxy gateway, the swarm
// orchestrator, the workspace API and the frontend together.
func New(admin *handler.Admin, gateway *handler.Gateway, swarm *handler.Swarm, ws *handler.Workspace, stats *handler.Stats, static http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

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
	r.Post("/api/v1/swarm/run/{runID}/stop", swarm.Stop)
	r.Get("/api/v1/models", swarm.Models)

	// Workspace.
	r.Get("/api/v1/workspace/files", ws.List)
	r.Get("/api/v1/workspace/files/*", ws.Read)
	r.Post("/api/v1/workspace/clear", ws.Clear)

	// Usage statistics.
	r.Get("/api/v1/stats", stats.Get)

	// Frontend.
	r.Handle("/*", static)

	return r
}
