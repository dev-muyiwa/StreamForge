package handlers

import (
	"StreamForge/internal/job"
	"StreamForge/internal/pipeline"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ProcessHandler handles video upload and processing requests
type ProcessHandler struct {
	jobManager       *job.Manager
	temporalWorkflow *pipeline.TemporalWorkflow
	logger           *zap.Logger
}

// NewProcessHandler creates a new process handler
func NewProcessHandler(jobManager *job.Manager, temporalWorkflow *pipeline.TemporalWorkflow, logger *zap.Logger) *ProcessHandler {
	return &ProcessHandler{
		jobManager:       jobManager,
		temporalWorkflow: temporalWorkflow,
		logger:           logger,
	}
}

// Handle processes the video upload and returns a job ID immediately
func (h *ProcessHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// Parse multipart form
	err := r.ParseMultipartForm(100 << 20) // 100 MB max
	if err != nil {
		h.logger.Error("Failed to parse multipart form", zap.Error(err))
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("video")
	if err != nil {
		h.logger.Error("Failed to read video file", zap.Error(err))
		http.Error(w, "Failed to read video file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Get key from form (or use default)
	key := r.FormValue("key")
	if key == "" {
		key = fmt.Sprintf("video_%d.mp4", time.Now().Unix())
	}

	bucket := "input"
	epochTime := time.Now().Unix()

	// Create workflow ID
	workflowID := fmt.Sprintf("video-processing-%s-%d", key, epochTime)

	// Create job in database
	createdJob, err := h.jobManager.CreateJob(r.Context(), key, bucket, workflowID)
	if err != nil {
		h.logger.Error("Failed to create job", zap.Error(err))
		http.Error(w, "Failed to create job", http.StatusInternalServerError)
		return
	}

	// Start workflow asynchronously
	go h.processVideo(createdJob.ID, file, key, bucket, epochTime)

	// Return job ID immediately
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"job_id":  createdJob.ID,
		"message": "Video processing started",
	})

	h.logger.Info("Job created and processing started",
		zap.String("job_id", createdJob.ID.String()),
		zap.String("key", key),
	)
}

// processVideo executes the video processing workflow asynchronously
func (h *ProcessHandler) processVideo(jobID uuid.UUID, file io.Reader, key, bucket string, epochTime int64) {
	ctx := context.Background()

	// Emit initial progress
	err := h.jobManager.EmitProgress(ctx, jobID, job.StageUploading, 5, "Starting video processing", nil)
	if err != nil {
		h.logger.Error("Failed to emit initial progress", zap.Error(err))
	}

	// Create workflow input
	workflowInput := pipeline.WorkflowInput{
		File:      file,
		JobID:     jobID,
		Key:       key,
		Bucket:    bucket,
		EpochTime: epochTime,
	}

	// Execute Temporal workflow
	result, err := h.temporalWorkflow.ExecuteWorkflow(ctx, workflowInput)
	if err != nil {
		h.logger.Error("Workflow execution failed",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		h.jobManager.EmitError(ctx, jobID, fmt.Sprintf("Processing failed: %v", err))
		return
	}

	// Complete job with result
	err = h.jobManager.CompleteJob(ctx, jobID, result)
	if err != nil {
		h.logger.Error("Failed to complete job",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return
	}

	h.logger.Info("Job completed successfully",
		zap.String("job_id", jobID.String()),
	)
}
