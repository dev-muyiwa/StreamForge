package api

import (
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	types "StreamForge/pkg"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type Server struct {
	storage    storage.Storage
	transcoder *pipeline.Transcoder
	packager   *pipeline.Packager
	workflow   *pipeline.Workflow
	cfg        *config.Config
}

func NewServer(storage storage.Storage, transcoder *pipeline.Transcoder, packager *pipeline.Packager, workflow *pipeline.Workflow, cfg *config.Config) *Server {
	return &Server{
		storage:    storage,
		transcoder: transcoder,
		packager:   packager,
		workflow:   workflow,
		cfg:        cfg,
	}
}

func (s *Server) Start() {
	//http.HandleFunc("/upload", s.handleUpload)
	//http.HandleFunc("/transcode", s.handleTranscode)
	//http.HandleFunc("/package", s.handlePackage)
	http.HandleFunc("/process", s.handleProcess)

	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		return
	}
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
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		InputFile string              `json:"input_file"`
		Configs   []types.CodecConfig `json:"configs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	epochTime := time.Now().Unix()
	results, err := s.transcoder.Transcode(r.Context(), req.InputFile, req.Configs, epochTime)
	if err != nil {
		http.Error(w, "Some transcoding jobs failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
	})
}

func (s *Server) handlePackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		InputFiles []string              `json:"input_files"`
		Configs    []types.PackageConfig `json:"configs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	epochTime := time.Now().Unix()
	results, err := s.packager.Package(r.Context(), req.InputFiles, req.Configs, epochTime)
	if err != nil {
		http.Error(w, "Some packaging jobs failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"results": results,
	})
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

	result, err := s.workflow.Run(r.Context(), file, bucket, key)
	if err != nil {
		http.Error(w, "Processing failed", http.StatusInternalServerError)
		return
	}
	err = json.NewEncoder(w).Encode(map[string]interface{}{
		"result": result,
	})
	if err != nil {
		return
	}
}
