package job

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store handles database operations for jobs
type Store struct {
	db *pgxpool.Pool
}

// NewStore creates a new job store
func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// CreateJob creates a new job in the database
func (s *Store) CreateJob(ctx context.Context, key, bucket, workflowID string) (*Job, error) {
	job := &Job{
		ID:         uuid.New(),
		Key:        key,
		Bucket:     bucket,
		Status:     StatusPending,
		Progress:   0,
		WorkflowID: workflowID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	query := `
		INSERT INTO jobs (id, key, bucket, status, progress, workflow_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	_, err := s.db.Exec(ctx, query,
		job.ID, job.Key, job.Bucket, job.Status,
		job.Progress, job.WorkflowID,
		job.CreatedAt, job.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	return job, nil
}

// GetJob retrieves a job by ID
func (s *Store) GetJob(ctx context.Context, jobID uuid.UUID) (*Job, error) {
	query := `
		SELECT id, key, bucket, status, stage, progress, workflow_id,
		       error_message, result, created_at, updated_at, completed_at
		FROM jobs
		WHERE id = $1
	`

	var job Job
	var stage, errorMessage, workflowID *string
	var result []byte
	var completedAt *time.Time

	err := s.db.QueryRow(ctx, query, jobID).Scan(
		&job.ID, &job.Key, &job.Bucket, &job.Status, &stage, &job.Progress,
		&workflowID, &errorMessage, &result,
		&job.CreatedAt, &job.UpdatedAt, &completedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("job not found: %s", jobID)
		}
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	if stage != nil {
		job.Stage = JobStage(*stage)
	}
	if errorMessage != nil {
		job.ErrorMessage = *errorMessage
	}
	if workflowID != nil {
		job.WorkflowID = *workflowID
	}
	if result != nil {
		job.Result = result
	}
	job.CompletedAt = completedAt

	return &job, nil
}

// GetJobWithProgress retrieves a job with its latest progress events
func (s *Store) GetJobWithProgress(ctx context.Context, jobID uuid.UUID, limit int) (*JobWithProgress, error) {
	job, err := s.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 10 // Default limit
	}

	query := `
		SELECT id, job_id, stage, progress, message, details, created_at
		FROM progress_events
		WHERE job_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := s.db.Query(ctx, query, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get progress events: %w", err)
	}
	defer rows.Close()

	var events []ProgressEvent
	for rows.Next() {
		var event ProgressEvent
		var details []byte

		err := rows.Scan(
			&event.ID, &event.JobID, &event.Stage, &event.Progress,
			&event.Message, &details, &event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan progress event: %w", err)
		}

		if details != nil {
			event.Details = details
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating progress events: %w", err)
	}

	// Reverse the events to show oldest first
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return &JobWithProgress{
		Job:          *job,
		LatestEvents: events,
	}, nil
}

// UpdateJobStatus updates the job status and stage
func (s *Store) UpdateJobStatus(ctx context.Context, jobID uuid.UUID, status JobStatus, stage JobStage, progress int) error {
	query := `
		UPDATE jobs
		SET status = $2, stage = $3, progress = $4, updated_at = $5
		WHERE id = $1
	`

	_, err := s.db.Exec(ctx, query, jobID, status, stage, progress, time.Now())
	if err != nil {
		return fmt.Errorf("failed to update job status: %w", err)
	}

	return nil
}

// UpdateJobError updates the job with an error message
func (s *Store) UpdateJobError(ctx context.Context, jobID uuid.UUID, errorMessage string) error {
	query := `
		UPDATE jobs
		SET status = $2, error_message = $3, updated_at = $4, completed_at = $5
		WHERE id = $1
	`

	now := time.Now()
	_, err := s.db.Exec(ctx, query, jobID, StatusFailed, errorMessage, now, now)
	if err != nil {
		return fmt.Errorf("failed to update job error: %w", err)
	}

	return nil
}

// UpdateJobResult updates the job with the final result
func (s *Store) UpdateJobResult(ctx context.Context, jobID uuid.UUID, result interface{}) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	query := `
		UPDATE jobs
		SET status = $2, stage = $3, progress = $4, result = $5,
		    updated_at = $6, completed_at = $7
		WHERE id = $1
	`

	now := time.Now()
	_, err = s.db.Exec(ctx, query,
		jobID, StatusCompleted, StageCompleted, 100, resultJSON, now, now,
	)
	if err != nil {
		return fmt.Errorf("failed to update job result: %w", err)
	}

	return nil
}

// AddProgressEvent adds a progress event to the database
func (s *Store) AddProgressEvent(ctx context.Context, jobID uuid.UUID, stage JobStage, progress int, message string, details map[string]interface{}) error {
	var detailsJSON []byte
	var err error

	if details != nil {
		detailsJSON, err = json.Marshal(details)
		if err != nil {
			return fmt.Errorf("failed to marshal details: %w", err)
		}
	}

	query := `
		INSERT INTO progress_events (job_id, stage, progress, message, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`

	_, err = s.db.Exec(ctx, query, jobID, stage, progress, message, detailsJSON, time.Now())
	if err != nil {
		return fmt.Errorf("failed to add progress event: %w", err)
	}

	return nil
}

// CleanupOldJobs deletes jobs older than the specified duration
func (s *Store) CleanupOldJobs(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoffTime := time.Now().Add(-olderThan)

	query := `
		DELETE FROM jobs
		WHERE created_at < $1
	`

	result, err := s.db.Exec(ctx, query, cutoffTime)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old jobs: %w", err)
	}

	return result.RowsAffected(), nil
}
