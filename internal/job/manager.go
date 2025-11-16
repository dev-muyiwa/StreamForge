package job

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SSEClient represents an active SSE connection
type SSEClient struct {
	JobID   uuid.UUID
	Channel chan ProgressUpdate
}

// Manager handles job lifecycle and SSE broadcasting
type Manager struct {
	store     *Store
	logger    *zap.Logger
	clients   map[uuid.UUID][]chan ProgressUpdate
	clientsMu sync.RWMutex
}

// NewManager creates a new job manager
func NewManager(store *Store, logger *zap.Logger) *Manager {
	return &Manager{
		store:   store,
		logger:  logger,
		clients: make(map[uuid.UUID][]chan ProgressUpdate),
	}
}

// CreateJob creates a new job
func (m *Manager) CreateJob(ctx context.Context, key, bucket, workflowID string) (*Job, error) {
	job, err := m.store.CreateJob(ctx, key, bucket, workflowID)
	if err != nil {
		return nil, err
	}

	m.logger.Info("Job created",
		zap.String("job_id", job.ID.String()),
		zap.String("workflow_id", workflowID),
	)

	return job, nil
}

// GetJob retrieves a job by ID
func (m *Manager) GetJob(ctx context.Context, jobID uuid.UUID) (*Job, error) {
	return m.store.GetJob(ctx, jobID)
}

// GetJobWithProgress retrieves a job with its progress events
func (m *Manager) GetJobWithProgress(ctx context.Context, jobID uuid.UUID, limit int) (*JobWithProgress, error) {
	return m.store.GetJobWithProgress(ctx, jobID, limit)
}

// UpdateJobMetadata updates job metadata (resolutions, VMAF score, stream URL)
func (m *Manager) UpdateJobMetadata(ctx context.Context, jobID uuid.UUID, resolutions []string, peakVMAFScore float64, streamURL string) error {
	err := m.store.UpdateJobMetadata(ctx, jobID, resolutions, peakVMAFScore, streamURL)
	if err != nil {
		m.logger.Error("Failed to update job metadata",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return err
	}

	m.logger.Info("Job metadata updated",
		zap.String("job_id", jobID.String()),
		zap.Int("resolutions", len(resolutions)),
		zap.Float64("peak_vmaf", peakVMAFScore),
		zap.String("stream_url", streamURL),
	)

	return nil
}

// EmitProgress emits a progress update for a job
func (m *Manager) EmitProgress(ctx context.Context, jobID uuid.UUID, stage JobStage, progress int, message string, details map[string]interface{}) error {
	// Update job status in database
	status := StatusProcessing
	if progress >= 100 {
		status = StatusCompleted
	}

	err := m.store.UpdateJobStatus(ctx, jobID, status, stage, progress)
	if err != nil {
		m.logger.Error("Failed to update job status",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return err
	}

	// Add progress event to database
	err = m.store.AddProgressEvent(ctx, jobID, stage, progress, message, details)
	if err != nil {
		m.logger.Error("Failed to add progress event",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return err
	}

	// Broadcast to SSE clients
	update := ProgressUpdate{
		JobID:     jobID,
		Stage:     stage,
		Progress:  progress,
		Message:   message,
		Details:   details,
		Timestamp: time.Now(),
	}

	m.broadcastUpdate(jobID, update)

	m.logger.Info("Progress emitted",
		zap.String("job_id", jobID.String()),
		zap.String("stage", string(stage)),
		zap.Int("progress", progress),
		zap.String("message", message),
	)

	return nil
}

// EmitError emits an error for a job
func (m *Manager) EmitError(ctx context.Context, jobID uuid.UUID, errorMessage string) error {
	err := m.store.UpdateJobError(ctx, jobID, errorMessage)
	if err != nil {
		m.logger.Error("Failed to update job error",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return err
	}

	// Broadcast error to SSE clients
	update := ProgressUpdate{
		JobID:     jobID,
		Stage:     "failed",
		Progress:  0,
		Message:   errorMessage,
		Timestamp: time.Now(),
	}

	m.broadcastUpdate(jobID, update)

	m.logger.Error("Job failed",
		zap.String("job_id", jobID.String()),
		zap.String("error", errorMessage),
	)

	return nil
}

// CompleteJob marks a job as completed with result
func (m *Manager) CompleteJob(ctx context.Context, jobID uuid.UUID, result interface{}) error {
	err := m.store.UpdateJobResult(ctx, jobID, result)
	if err != nil {
		m.logger.Error("Failed to complete job",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		return err
	}

	// Emit final progress event
	err = m.EmitProgress(ctx, jobID, StageCompleted, 100, "Processing completed successfully", nil)
	if err != nil {
		return err
	}

	m.logger.Info("Job completed",
		zap.String("job_id", jobID.String()),
	)

	return nil
}

// Subscribe adds an SSE client for a job
func (m *Manager) Subscribe(jobID uuid.UUID) chan ProgressUpdate {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()

	ch := make(chan ProgressUpdate, 10)
	m.clients[jobID] = append(m.clients[jobID], ch)

	m.logger.Info("Client subscribed",
		zap.String("job_id", jobID.String()),
		zap.Int("total_clients", len(m.clients[jobID])),
	)

	return ch
}

// Unsubscribe removes an SSE client
func (m *Manager) Unsubscribe(jobID uuid.UUID, ch chan ProgressUpdate) {
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()

	clients := m.clients[jobID]
	for i, client := range clients {
		if client == ch {
			// Remove client from slice
			m.clients[jobID] = append(clients[:i], clients[i+1:]...)
			close(ch)
			break
		}
	}

	// Remove job entry if no more clients
	if len(m.clients[jobID]) == 0 {
		delete(m.clients, jobID)
	}

	m.logger.Info("Client unsubscribed",
		zap.String("job_id", jobID.String()),
		zap.Int("remaining_clients", len(m.clients[jobID])),
	)
}

// broadcastUpdate broadcasts a progress update to all subscribers
func (m *Manager) broadcastUpdate(jobID uuid.UUID, update ProgressUpdate) {
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()

	clients := m.clients[jobID]
	if len(clients) == 0 {
		return
	}

	m.logger.Debug("Broadcasting update",
		zap.String("job_id", jobID.String()),
		zap.Int("client_count", len(clients)),
	)

	for _, ch := range clients {
		select {
		case ch <- update:
			// Successfully sent
		default:
			// Channel full, skip this update
			m.logger.Warn("Client channel full, skipping update",
				zap.String("job_id", jobID.String()),
			)
		}
	}
}

// CleanupOldJobs cleans up jobs older than the specified duration
func (m *Manager) CleanupOldJobs(ctx context.Context, olderThan time.Duration) error {
	count, err := m.store.CleanupOldJobs(ctx, olderThan)
	if err != nil {
		m.logger.Error("Failed to cleanup old jobs", zap.Error(err))
		return err
	}

	m.logger.Info("Cleaned up old jobs",
		zap.Int64("count", count),
		zap.Duration("older_than", olderThan),
	)

	return nil
}

// FormatSSEMessage formats a progress update as an SSE message
func FormatSSEMessage(update ProgressUpdate) (string, error) {
	data, err := json.Marshal(update)
	if err != nil {
		return "", fmt.Errorf("failed to marshal update: %w", err)
	}

	return fmt.Sprintf("event: progress\ndata: %s\n\n", data), nil
}
