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

	// Note: Timeout middleware removed from global scope
	// Applied per-route instead (see setupRoutes)

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

	// Health check with short timeout
	s.router.With(middleware.Timeout(10*time.Second)).Get("/health", s.handleHealth)

	// Video processing endpoint - NO timeout (handles large file uploads)
	// The handler returns immediately with job ID, so timeout isn't needed
	s.router.Post("/process", processHandler.Handle)

	// Job status endpoints with reasonable timeout
	s.router.With(middleware.Timeout(30*time.Second)).Get("/jobs/{id}", jobsHandler.GetJob)

	// SSE streaming endpoint - NO timeout (long-lived connection)
	s.router.Get("/jobs/{id}/stream", streamHandler.StreamProgress)

	// Serve static HLS/DASH files from outputs directory
	// Longer timeout for large video segments
	fileServer := http.FileServer(http.Dir("./outputs"))
	s.router.With(middleware.Timeout(5*time.Minute)).Handle("/outputs/*", http.StripPrefix("/outputs", fileServer))
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
		Addr:    addr,
		Handler: s.router,
		// Long timeouts to support large file uploads
		ReadTimeout:       10 * time.Minute, // Reading large video files
		WriteTimeout:      10 * time.Minute, // Writing responses (including SSE streams)
		IdleTimeout:       2 * time.Minute,  // Keep-alive connections
		MaxHeaderBytes:    1 << 20,          // 1 MB max header size
		ReadHeaderTimeout: 30 * time.Second, // Prevent slowloris attacks
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
