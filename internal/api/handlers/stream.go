package handlers

import (
	"StreamForge/internal/job"
	"context"
	"encoding/json"
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

	// Ensure we have a flusher (required for SSE)
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.logger.Error("Streaming not supported - ResponseWriter does not implement http.Flusher")
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Set all required SSE headers BEFORE writing any data
	h.setSSEHeaders(w)

	// CRITICAL: Write status code 200 explicitly before sending data
	// This prevents "ERR_INCOMPLETE_CHUNKED_ENCODING" errors
	w.WriteHeader(http.StatusOK)

	// Flush headers immediately to establish the SSE connection
	flusher.Flush()

	h.logger.Info("SSE stream established",
		zap.String("job_id", jobID.String()),
		zap.String("remote_addr", r.RemoteAddr),
	)

	// Subscribe to progress updates
	progressChan := h.jobManager.Subscribe(jobID)
	defer func() {
		h.jobManager.Unsubscribe(jobID, progressChan)
		h.logger.Info("SSE stream closed",
			zap.String("job_id", jobID.String()),
		)
	}()

	// Send initial job status immediately
	if err := h.sendInitialStatus(w, flusher, existingJob); err != nil {
		h.logger.Error("Failed to send initial status",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return
	}

	// Create context with timeout for cleanup
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Create heartbeat ticker (15 seconds to keep connection alive)
	// This prevents proxy/load balancer timeouts
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Main SSE event loop
	h.streamEventLoop(ctx, w, flusher, jobID, progressChan, heartbeat)
}

// setSSEHeaders configures all necessary headers for SSE
func (h *StreamHandler) setSSEHeaders(w http.ResponseWriter) {
	// Standard SSE headers
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")

	// Disable buffering for nginx and other reverse proxies
	// This is CRITICAL to prevent chunked encoding errors
	w.Header().Set("X-Accel-Buffering", "no")

	// CORS headers (adjust based on your security requirements)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Type")
}

// sendInitialStatus sends the current job status when client connects
func (h *StreamHandler) sendInitialStatus(w http.ResponseWriter, flusher http.Flusher, jobData *job.Job) error {
	data := map[string]interface{}{
		"job_id":   jobData.ID.String(),
		"status":   jobData.Status,
		"stage":    jobData.Stage,
		"progress": jobData.Progress,
		"message":  "Connected to progress stream",
	}

	if err := h.writeSSEEvent(w, flusher, "status", data); err != nil {
		return fmt.Errorf("failed to write initial status: %w", err)
	}

	return nil
}

// streamEventLoop handles the main SSE event streaming loop
func (h *StreamHandler) streamEventLoop(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	jobID uuid.UUID,
	progressChan <-chan job.ProgressUpdate,
	heartbeat *time.Ticker,
) {
	// Track if we've sent completion event
	jobCompleted := false

	for {
		select {
		case <-ctx.Done():
			// Client disconnected or context cancelled
			h.logger.Info("Client disconnected",
				zap.String("job_id", jobID.String()),
				zap.String("reason", ctx.Err().Error()),
			)
			return

		case update, ok := <-progressChan:
			if !ok {
				// Channel closed - job manager stopped broadcasting
				h.logger.Info("Progress channel closed",
					zap.String("job_id", jobID.String()),
				)
				return
			}

			// Send progress update to client
			if err := h.sendProgressUpdate(w, flusher, update); err != nil {
				h.logger.Error("Failed to send progress update",
					zap.String("job_id", jobID.String()),
					zap.Error(err),
				)
				return
			}

			// Check if job is complete or failed
			if h.isJobFinished(update) && !jobCompleted {
				jobCompleted = true

				// Send final completion event
				if err := h.sendCompletionEvent(w, flusher, update); err != nil {
					h.logger.Error("Failed to send completion event",
						zap.String("job_id", jobID.String()),
						zap.Error(err),
					)
				}

				// Give client time to receive the final event before closing
				time.Sleep(100 * time.Millisecond)

				h.logger.Info("Job finished, closing stream gracefully",
					zap.String("job_id", jobID.String()),
					zap.String("final_stage", string(update.Stage)),
					zap.Int("final_progress", update.Progress),
				)
				return
			}

		case <-heartbeat.C:
			// Send heartbeat comment to keep connection alive
			// SSE comments (lines starting with :) are ignored by clients
			if err := h.writeHeartbeat(w, flusher); err != nil {
				h.logger.Error("Failed to send heartbeat",
					zap.String("job_id", jobID.String()),
					zap.Error(err),
				)
				return
			}
		}
	}
}

// sendProgressUpdate sends a progress update event to the client
func (h *StreamHandler) sendProgressUpdate(w http.ResponseWriter, flusher http.Flusher, update job.ProgressUpdate) error {
	data := map[string]interface{}{
		"job_id":    update.JobID.String(),
		"stage":     update.Stage,
		"progress":  update.Progress,
		"message":   update.Message,
		"timestamp": update.Timestamp.Format(time.RFC3339),
	}

	// Include details if present
	if update.Details != nil && len(update.Details) > 0 {
		data["details"] = update.Details
	}

	return h.writeSSEEvent(w, flusher, "progress", data)
}

// sendCompletionEvent sends a final completion event
func (h *StreamHandler) sendCompletionEvent(w http.ResponseWriter, flusher http.Flusher, update job.ProgressUpdate) error {
	eventType := "complete"
	if update.Stage == "failed" {
		eventType = "error"
	}

	data := map[string]interface{}{
		"job_id":    update.JobID.String(),
		"stage":     update.Stage,
		"progress":  update.Progress,
		"message":   update.Message,
		"timestamp": update.Timestamp.Format(time.RFC3339),
	}

	if update.Details != nil {
		data["details"] = update.Details
	}

	return h.writeSSEEvent(w, flusher, eventType, data)
}

// writeSSEEvent writes a properly formatted SSE event
func (h *StreamHandler) writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data interface{}) error {
	// Marshal data to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	// Write SSE event in proper format:
	// event: <event-type>
	// data: <json-data>
	// <blank line>
	message := fmt.Sprintf("event: %s\ndata: %s\n\n", event, jsonData)

	// Write to response
	n, err := fmt.Fprint(w, message)
	if err != nil {
		return fmt.Errorf("failed to write SSE event: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("wrote 0 bytes")
	}

	// CRITICAL: Flush immediately to send data to client
	// Without this, data may be buffered and cause chunked encoding errors
	flusher.Flush()

	return nil
}

// writeHeartbeat sends a heartbeat comment to keep connection alive
func (h *StreamHandler) writeHeartbeat(w http.ResponseWriter, flusher http.Flusher) error {
	// SSE comments are lines starting with ":"
	// They keep the connection alive without triggering events
	n, err := fmt.Fprint(w, ": heartbeat\n\n")
	if err != nil {
		return fmt.Errorf("failed to write heartbeat: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("wrote 0 bytes for heartbeat")
	}

	// Flush to ensure heartbeat is sent
	flusher.Flush()

	return nil
}

// isJobFinished checks if a job has reached a terminal state
func (h *StreamHandler) isJobFinished(update job.ProgressUpdate) bool {
	// Job is finished if:
	// 1. Progress is 100% (completed)
	// 2. Stage is "failed"
	// 3. Stage is "completed"
	return update.Progress >= 100 ||
		update.Stage == "failed" ||
		update.Stage == job.StageCompleted
}
