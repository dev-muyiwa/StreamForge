package api

import (
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline"
	types "StreamForge/pkg"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type Server struct {
	temporalWorkflow *pipeline.TemporalWorkflow
	cfg              *config.Config
}

func NewServer(temporalWorkflow *pipeline.TemporalWorkflow, cfg *config.Config) *Server {
	return &Server{
		temporalWorkflow: temporalWorkflow,
		cfg:              cfg,
	}
}

func (s *Server) Start() {
	// Add health check endpoint
	http.HandleFunc("/health", s.handleHealth)
	//http.HandleFunc("/upload", s.handleUpload)
	//http.HandleFunc("/transcode", s.handleTranscode)
	//http.HandleFunc("/package", s.handlePackage)
	http.HandleFunc("/process", s.handleProcess)

	err := http.ListenAndServe(":8081", nil)
	if err != nil {
		return
	}
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

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file, _, err := r.FormFile("video")
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create a logger for the pipeline
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	p := pipeline.NewPipeline(logger, types.RetryConfig{})
	result, err := p.Ingest(r.Context(), file, "input", "video.mp4")
	if err != nil {
		http.Error(w, "Upload failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"result": result,
	})
}

func (s *Server) handleTranscode(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Transcode endpoint deprecated - use /process instead", http.StatusGone)
}

func (s *Server) handlePackage(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Package endpoint deprecated - use /process instead", http.StatusGone)
}

func (s *Server) handleProcess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file, _, err := r.FormFile("video")
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// For input, we always use a local bucket/key structure for organization
	// The actual storage type only affects where outputs are stored
	bucket := "input" // Local input bucket
	key := r.FormValue("key")
	if key == "" {
		key = "video.mp4" // Default todo(make unique)
	}

	// Create workflow input
	workflowInput := pipeline.WorkflowInput{
		File:      file,
		Key:       key,
		Bucket:    bucket,
		EpochTime: time.Now().Unix(),
	}

	// Execute Temporal workflow
	result, err := s.temporalWorkflow.ExecuteWorkflow(r.Context(), workflowInput)
	if err != nil {
		http.Error(w, "Processing failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(map[string]interface{}{
		"result": result,
	})
	if err != nil {
		return
	}
}
