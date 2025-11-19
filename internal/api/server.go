package api

import (
	"StreamForge/internal/api/handlers"
	"StreamForge/internal/config"
	"StreamForge/internal/job"
	"StreamForge/internal/pipeline"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.uber.org/zap"
)

type Server struct {
	router           *chi.Mux
	temporalWorkflow *pipeline.TemporalWorkflow
	jobManager       *job.Manager
	cfg              *config.Config
	logger           *zap.Logger
	httpServer       *http.Server
}

func NewServer(temporalWorkflow *pipeline.TemporalWorkflow, jobManager *job.Manager, cfg *config.Config, logger *zap.Logger) *Server {
	s := &Server{
		temporalWorkflow: temporalWorkflow,
		jobManager:       jobManager,
		cfg:              cfg,
		logger:           logger,
	}

	// Setup router
	s.router = chi.NewRouter()
	s.setupMiddleware()
	s.setupRoutes()

	return s
}

func (s *Server) setupMiddleware() {
	// Recover from panics
	s.router.Use(middleware.Recoverer)

	// Request ID
	s.router.Use(middleware.RequestID)

	// Real IP
	s.router.Use(middleware.RealIP)

	// Request logger (using built-in logger)
	s.router.Use(middleware.Logger)

	// Timeout
	s.router.Use(middleware.Timeout(60 * time.Second))

	// CORS
	s.router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
}

func (s *Server) setupRoutes() {
	// Create handlers
	processHandler := handlers.NewProcessHandler(s.jobManager, s.temporalWorkflow, s.logger)
	jobsHandler := handlers.NewJobsHandler(s.jobManager, s.logger)
	streamHandler := handlers.NewStreamHandler(s.jobManager, s.logger)

	// Health check
	s.router.Get("/health", s.handleHealth)

	// Video processing endpoints
	s.router.Post("/process", processHandler.Handle)

	// Job status endpoints
	s.router.Get("/jobs/{id}", jobsHandler.GetJob)
	s.router.Get("/jobs/{id}/stream", streamHandler.StreamProgress)

	// Serve static HLS/DASH files from outputs directory
	fileServer := http.FileServer(http.Dir("./outputs"))
	s.router.Handle("/outputs/*", http.StripPrefix("/outputs", fileServer))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "healthy",
		"service": "streamforge",
		"version": "1.0.0",
	})
}

func (s *Server) Start(addr string) error {
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("Starting HTTP server", zap.String("addr", addr))
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down HTTP server")
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}
