package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/store"
)

func NewServer(s store.Store, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.SetHeader("Content-Type", "application/json"))

	h := NewHandlers(s, cfg)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.HealthCheck)

		r.Route("/tasks", func(r chi.Router) {
			r.Post("/", h.CreateTask)
			r.Get("/", h.ListTasks)
			r.Get("/{id}", h.GetTask)
			r.Delete("/{id}", h.DeleteTask)
			r.Get("/{id}/logs", h.GetTaskLogs)
		})
	})

	return r
}
