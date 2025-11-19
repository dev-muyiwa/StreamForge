package job

import (
	"encoding/json"
	"sort"
	"strconv"
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

// StreamURLs is a custom map type that maintains order when marshaling to JSON
// Order: "auto" first, then resolutions from smallest to largest
type StreamURLs map[string]string

// MarshalJSON implements custom JSON marshaling to maintain order
func (s StreamURLs) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("{}"), nil
	}

	// Separate "auto" from resolution keys
	var resolutions []string
	autoURL := ""

	for key, url := range s {
		if key == "auto" {
			autoURL = url
		} else {
			resolutions = append(resolutions, key)
		}
	}

	// Sort resolutions by numeric value (360p, 480p, 720p, 1080p, etc.)
	sort.Slice(resolutions, func(i, j int) bool {
		// Extract numeric part (e.g., "720p" -> 720)
		numI, _ := strconv.Atoi(resolutions[i][:len(resolutions[i])-1])
		numJ, _ := strconv.Atoi(resolutions[j][:len(resolutions[j])-1])
		return numI < numJ
	})

	// Build ordered JSON manually
	result := "{"

	// Add "auto" first if it exists
	if autoURL != "" {
		result += `"auto":"` + autoURL + `"`
		if len(resolutions) > 0 {
			result += ","
		}
	}

	// Add resolutions in order
	for i, key := range resolutions {
		result += `"` + key + `":"` + s[key] + `"`
		if i < len(resolutions)-1 {
			result += ","
		}
	}

	result += "}"
	return []byte(result), nil
}

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
	StreamURLs    StreamURLs      `json:"stream_urls,omitempty"` // Ordered map: "auto" first, then 360p, 480p, 720p, 1080p, etc.
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
