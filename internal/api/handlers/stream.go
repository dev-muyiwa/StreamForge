package handlers

import (
	"StreamForge/internal/job"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// StreamHandler handles SSE streaming for job progress
type StreamHandler struct {
	jobManager *job.Manager
	logger     *zap.Logger
}

// NewStreamHandler creates a new stream handler
func NewStreamHandler(jobManager *job.Manager, logger *zap.Logger) *StreamHandler {
	return &StreamHandler{
		jobManager: jobManager,
		logger:     logger,
	}
}

// StreamProgress streams job progress via Server-Sent Events
func (h *StreamHandler) StreamProgress(w http.ResponseWriter, r *http.Request) {
	jobIDStr := chi.URLParam(r, "id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		h.logger.Error("Invalid job ID", zap.String("job_id", jobIDStr), zap.Error(err))
		http.Error(w, "Invalid job ID", http.StatusBadRequest)
		return
	}

	// Verify job exists
	existingJob, err := h.jobManager.GetJob(r.Context(), jobID)
	if err != nil {
		h.logger.Error("Job not found", zap.String("job_id", jobID.String()), zap.Error(err))
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Flush headers
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.logger.Error("Streaming not supported")
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to progress updates
	progressChan := h.jobManager.Subscribe(jobID)
	defer h.jobManager.Unsubscribe(jobID, progressChan)

	h.logger.Info("Client connected to SSE stream",
		zap.String("job_id", jobID.String()),
	)

	// Send initial job status
	initialData := fmt.Sprintf(`event: status
data: {"job_id":"%s","status":"%s","stage":"%s","progress":%d}

`, jobID.String(), existingJob.Status, existingJob.Stage, existingJob.Progress)

	fmt.Fprint(w, initialData)
	flusher.Flush()

	// Create a ticker for heartbeat
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	// Stream progress updates
	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			h.logger.Info("Client disconnected from SSE stream",
				zap.String("job_id", jobID.String()),
			)
			return

		case update, ok := <-progressChan:
			if !ok {
				// Channel closed
				return
			}

			// Format and send SSE message
			message, err := job.FormatSSEMessage(update)
			if err != nil {
				h.logger.Error("Failed to format SSE message",
					zap.String("job_id", jobID.String()),
					zap.Error(err),
				)
				continue
			}

			fmt.Fprint(w, message)
			flusher.Flush()

			// Close connection if job is completed or failed
			if update.Progress >= 100 || update.Stage == "failed" {
				h.logger.Info("Job finished, closing SSE stream",
					zap.String("job_id", jobID.String()),
					zap.String("stage", string(update.Stage)),
				)
				// Send a final "done" event
				fmt.Fprint(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}

		case <-heartbeat.C:
			// Send heartbeat to keep connection alive
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
