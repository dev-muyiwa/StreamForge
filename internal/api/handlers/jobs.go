package handlers

import (
	"StreamForge/internal/job"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// JobsHandler handles job status requests
type JobsHandler struct {
	jobManager *job.Manager
	logger     *zap.Logger
}

// NewJobsHandler creates a new jobs handler
func NewJobsHandler(jobManager *job.Manager, logger *zap.Logger) *JobsHandler {
	return &JobsHandler{
		jobManager: jobManager,
		logger:     logger,
	}
}

// GetJob returns the status of a job as JSON
func (h *JobsHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	jobIDStr := chi.URLParam(r, "id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		h.logger.Error("Invalid job ID", zap.String("job_id", jobIDStr), zap.Error(err))
		http.Error(w, "Invalid job ID", http.StatusBadRequest)
		return
	}

	// Get job with progress events
	jobWithProgress, err := h.jobManager.GetJobWithProgress(r.Context(), jobID, 10)
	if err != nil {
		h.logger.Error("Failed to get job", zap.String("job_id", jobID.String()), zap.Error(err))
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Return job status as JSON
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(jobWithProgress)

	h.logger.Info("Job status retrieved",
		zap.String("job_id", jobID.String()),
		zap.String("status", string(jobWithProgress.Status)),
	)
}
