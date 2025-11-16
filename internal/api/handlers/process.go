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

	// Extract metadata from workflow results
	resolutions, peakVMAFScore, streamURL := h.extractMetadata(result, key, epochTime)

	// Update job metadata
	err = h.jobManager.UpdateJobMetadata(ctx, jobID, resolutions, peakVMAFScore, streamURL)
	if err != nil {
		h.logger.Error("Failed to update job metadata",
			zap.String("job_id", jobID.String()),
			zap.Error(err),
		)
		// Don't return - continue to complete the job even if metadata update fails
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
		zap.Int("resolutions", len(resolutions)),
		zap.Float64("peak_vmaf", peakVMAFScore),
		zap.String("stream_url", streamURL),
	)
}

// extractMetadata extracts metadata from workflow results
func (h *ProcessHandler) extractMetadata(result *pipeline.WorkflowOutput, key string, epochTime int64) ([]string, float64, string) {
	resolutions := []string{}
	var peakVMAFScore float64
	streamURL := ""

	// Extract resolutions from transcode results
	if result != nil && len(result.TranscodeResults) > 0 {
		for _, tr := range result.TranscodeResults {
			if tr.Error == nil {
				resolution := extractResolutionFromPath(tr.OutputPath)
				if resolution != "" && !contains(resolutions, resolution) {
					resolutions = append(resolutions, resolution)
				}
			}
		}
	}

	// Calculate peak VMAF score (max across all resolutions)
	if result != nil && len(result.VMAFResults) > 0 {
		for _, vr := range result.VMAFResults {
			if vr.Error == nil && vr.VMAFScore > peakVMAFScore {
				peakVMAFScore = vr.VMAFScore
			}
		}
	}

	// Generate stream URL from package results
	if result != nil && len(result.PackageResults) > 0 {
		// Find HLS master manifest first, then DASH, or use first available
		for _, pr := range result.PackageResults {
			if pr.Error == nil {
				// Prefer HLS master manifest
				if isHLSMasterManifest(pr.OutputPath) {
					streamURL = generateStreamURL(pr.OutputPath, key, epochTime)
					break
				}
				// Fallback to DASH manifest
				if isDASHManifest(pr.OutputPath) && streamURL == "" {
					streamURL = generateStreamURL(pr.OutputPath, key, epochTime)
				}
			}
		}
		// If no HLS or DASH found, use first successful package result
		if streamURL == "" {
			for _, pr := range result.PackageResults {
				if pr.Error == nil {
					streamURL = generateStreamURL(pr.OutputPath, key, epochTime)
					break
				}
			}
		}
	}

	return resolutions, peakVMAFScore, streamURL
}

// extractResolutionFromPath extracts resolution from file path
func extractResolutionFromPath(outputPath string) string {
	// Extract resolution from path like "outputs/123456/720/video.mp4"
	// This assumes the resolution is the directory name
	parts := []rune(outputPath)
	var segments []string
	currentSegment := ""

	for _, char := range parts {
		if char == '/' || char == '\\' {
			if currentSegment != "" {
				segments = append(segments, currentSegment)
				currentSegment = ""
			}
		} else {
			currentSegment += string(char)
		}
	}
	if currentSegment != "" {
		segments = append(segments, currentSegment)
	}

	// Look for a numeric directory (resolution)
	for i := len(segments) - 1; i >= 0; i-- {
		segment := segments[i]
		// Check if segment is numeric (like "720", "1080")
		isNumeric := true
		for _, char := range segment {
			if char < '0' || char > '9' {
				isNumeric = false
				break
			}
		}
		if isNumeric && len(segment) >= 3 {
			return segment + "p"
		}
	}
	return ""
}

// isHLSMasterManifest checks if the path is an HLS master manifest
func isHLSMasterManifest(path string) bool {
	return len(path) >= 5 && path[len(path)-5:] == ".m3u8"
}

// isDASHManifest checks if the path is a DASH manifest
func isDASHManifest(path string) bool {
	return len(path) >= 4 && path[len(path)-4:] == ".mpd"
}

// generateStreamURL generates a streaming URL from the output path
func generateStreamURL(outputPath, key string, epochTime int64) string {
	// For local storage, generate a relative URL
	// In production, this would be an absolute URL to your CDN or storage service
	// Format: /streams/{epochTime}/{key}/{filename}
	filename := ""
	parts := []rune(outputPath)
	lastSlash := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == '/' || parts[i] == '\\' {
			lastSlash = i
			break
		}
	}
	if lastSlash >= 0 {
		filename = string(parts[lastSlash+1:])
	} else {
		filename = outputPath
	}

	return fmt.Sprintf("/streams/%d/%s/%s", epochTime, key, filename)
}

// contains checks if a string slice contains a specific string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
