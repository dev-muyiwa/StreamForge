package job

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// JobStatus represents the current status of a job
type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
)

// JobStage represents the current processing stage
type JobStage string

const (
	StageUploading   JobStage = "uploading"
	StageTranscoding JobStage = "transcoding"
	StageValidation  JobStage = "validation"
	StagePackaging   JobStage = "packaging"
	StageStorage     JobStage = "storage"
	StageCompleted   JobStage = "completed"
)

// Job represents a video processing job
type Job struct {
	ID            uuid.UUID       `json:"id"`
	Key           string          `json:"key"`
	Bucket        string          `json:"bucket"`
	Status        JobStatus       `json:"status"`
	Stage         JobStage        `json:"stage,omitempty"`
	Progress      int             `json:"progress"`
	WorkflowID    string          `json:"workflow_id,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	Resolutions   []string        `json:"resolutions,omitempty"`
	PeakVMAFScore float64         `json:"peak_vmaf_score,omitempty"`
	StreamURL     string          `json:"stream_url,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}

// ProgressEvent represents a single progress update event
type ProgressEvent struct {
	ID        int64           `json:"id"`
	JobID     uuid.UUID       `json:"job_id"`
	Stage     JobStage        `json:"stage"`
	Progress  int             `json:"progress"`
	Message   string          `json:"message"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// JobWithProgress combines job info with recent progress events
type JobWithProgress struct {
	Job
	LatestEvents []ProgressEvent `json:"latest_events"`
}

// SSEMessage represents a server-sent event message
type SSEMessage struct {
	Event string      `json:"-"`
	Data  interface{} `json:"data"`
}

// ProgressUpdate represents a progress update to be broadcast via SSE
type ProgressUpdate struct {
	JobID     uuid.UUID              `json:"job_id"`
	Stage     JobStage               `json:"stage"`
	Progress  int                    `json:"progress"`
	Message   string                 `json:"message"`
	Details   map[string]interface{} `json:"details,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}
