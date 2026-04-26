package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

func NewServer(s store.Store, cfg *config.Config, logs LogFetcher, bus notify.Emitter) chi.Router {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	h := NewHandlers(s, cfg, logs, bus)

	r.Get("/health", h.HealthCheck)
	r.Get("/api/v1/readings/lookup", h.LookupReading)
	r.Post("/api/v1/readings/similar", h.FindSimilarReadings)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware(s, cfg.APIKey))

		r.Get("/health", h.HealthCheck)

		r.Get("/readings", h.ListReadings)
		r.Get("/readings/{id}", h.GetReading)

		r.Route("/tasks", func(r chi.Router) {
			r.Post("/", h.CreateTask)
			r.Get("/", h.ListTasks)
			r.Get("/{id}", h.GetTask)
			r.Delete("/{id}", h.DeleteTask)
			r.Post("/{id}/retry", h.RetryTask)
			r.Get("/{id}/logs", h.GetTaskLogs)
			r.Get("/{id}/output", h.GetTaskOutput)
			r.Get("/{id}/output.json", h.GetTaskOutputJSON)
		})
	})

	if cfg != nil && cfg.WebDir != "" {
		r.Get("/*", webAppHandler{dir: cfg.WebDir}.ServeHTTP)
	}

	return r
}
