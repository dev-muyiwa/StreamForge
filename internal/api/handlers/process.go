package handlers

import (
	"StreamForge/internal/job"
	"StreamForge/internal/pipeline"
	types "StreamForge/pkg"
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
	// Parse multipart form with generous limit for video files
	// 1 GB = 1024 MB = 1024 * 1024 * 1024 bytes
	err := r.ParseMultipartForm(1024 << 20) // 1 GB max
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

	// Read the entire file into memory before passing to goroutine
	// This prevents "file already closed" errors when the handler returns
	fileData, err := io.ReadAll(file)
	if err != nil {
		h.logger.Error("Failed to read file data", zap.Error(err))
		http.Error(w, "Failed to read file data", http.StatusInternalServerError)
		return
	}

	// Get key from form (or use default)
	key := r.FormValue("key")
	if key == "" {
		key = fmt.Sprintf("video_%d.mp4", time.Now().Unix())
	}

	bucket := "input"
	epochTime := time.Now().Unix()

	// Parse optional fields
	var resolutions []int
	var isLLHLSEnabled *bool
	var segmentDuration *int

	// Parse resolutions (JSON array)
	if resolutionsStr := r.FormValue("resolutions"); resolutionsStr != "" {
		if err := json.Unmarshal([]byte(resolutionsStr), &resolutions); err != nil {
			h.logger.Error("Failed to parse resolutions", zap.Error(err))
			http.Error(w, "Invalid resolutions format (expected JSON array)", http.StatusBadRequest)
			return
		}
		// Validate resolutions
		validResolutions := []int{144, 240, 360, 480, 540, 720, 1080}
		for _, res := range resolutions {
			valid := false
			for _, vr := range validResolutions {
				if res == vr {
					valid = true
					break
				}
			}
			if !valid {
				h.logger.Error("Invalid resolution", zap.Int("resolution", res))
				http.Error(w, fmt.Sprintf("Invalid resolution: %d (valid: 144, 240, 360, 480, 540, 720, 1080)", res), http.StatusBadRequest)
				return
			}
		}
	}

	// Parse is_ll_hls_enabled (boolean)
	if llhlsStr := r.FormValue("is_ll_hls_enabled"); llhlsStr != "" {
		llhlsEnabled := llhlsStr == "true" || llhlsStr == "1"
		isLLHLSEnabled = &llhlsEnabled
	}

	// Parse segment_duration_in_seconds (integer with max 20)
	if segmentStr := r.FormValue("segment_duration_in_seconds"); segmentStr != "" {
		var segmentDur int
		if _, err := fmt.Sscanf(segmentStr, "%d", &segmentDur); err != nil {
			h.logger.Error("Failed to parse segment_duration_in_seconds", zap.Error(err))
			http.Error(w, "Invalid segment_duration_in_seconds format (expected integer)", http.StatusBadRequest)
			return
		}
		if segmentDur > 20 {
			h.logger.Error("segment_duration_in_seconds exceeds maximum", zap.Int("value", segmentDur))
			http.Error(w, "segment_duration_in_seconds must be <= 20", http.StatusBadRequest)
			return
		}
		segmentDuration = &segmentDur
	}

	// Parse plugins (JSON array)
	var pluginConfigs []types.PluginConfig
	if pluginsStr := r.FormValue("plugins"); pluginsStr != "" {
		if err := json.Unmarshal([]byte(pluginsStr), &pluginConfigs); err != nil {
			h.logger.Error("Failed to parse plugins", zap.Error(err))
			http.Error(w, "Invalid plugins format (expected JSON array)", http.StatusBadRequest)
			return
		}
	}

	// Create workflow ID
	workflowID := fmt.Sprintf("video-processing-%s-%d", key, epochTime)

	// Create job in database
	createdJob, err := h.jobManager.CreateJob(r.Context(), key, bucket, workflowID)
	if err != nil {
		h.logger.Error("Failed to create job", zap.Error(err))
		http.Error(w, "Failed to create job", http.StatusInternalServerError)
		return
	}

	// Start workflow asynchronously with file data buffer and optional settings
	go h.processVideo(createdJob.ID, fileData, key, bucket, epochTime, resolutions, isLLHLSEnabled, segmentDuration, pluginConfigs)

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
func (h *ProcessHandler) processVideo(jobID uuid.UUID, fileData []byte, key, bucket string, epochTime int64, customResolutions []int, isLLHLSEnabled *bool, segmentDuration *int, pluginConfigs []types.PluginConfig) {
	ctx := context.Background()

	// Emit initial progress
	err := h.jobManager.EmitProgress(ctx, jobID, job.StageUploading, 5, "Starting video processing", nil)
	if err != nil {
		h.logger.Error("Failed to emit initial progress", zap.Error(err))
	}

	// Create workflow input
	workflowInput := pipeline.WorkflowInput{
		FileData:        fileData,
		JobID:           jobID,
		Key:             key,
		Bucket:          bucket,
		EpochTime:       epochTime,
		Resolutions:     customResolutions,
		IsLLHLSEnabled:  isLLHLSEnabled,
		SegmentDuration: segmentDuration,
		PluginConfigs:   pluginConfigs,
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
	resolutions, peakVMAFScore, streamURLs := h.extractMetadata(result, key, epochTime)

	// Update job metadata
	err = h.jobManager.UpdateJobMetadata(ctx, jobID, resolutions, peakVMAFScore, streamURLs)
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
		zap.Any("stream_urls", streamURLs),
	)
}

// extractMetadata extracts metadata from workflow results
func (h *ProcessHandler) extractMetadata(result *pipeline.WorkflowOutput, key string, epochTime int64) ([]string, float64, map[string]string) {
	resolutions := []string{}
	var peakVMAFScore float64
	streamURLs := make(map[string]string)

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

	// Generate stream URLs from package results
	if result != nil && len(result.PackageResults) > 0 {
		// Look for master playlist first (auto quality)
		for _, pr := range result.PackageResults {
			if pr.Error == nil && isMasterPlaylist(pr.OutputPath) {
				streamURLs["auto"] = generateStreamURL(pr.OutputPath, key, epochTime)
				break
			}
		}

		// Generate URLs for individual resolution playlists
		for _, pr := range result.PackageResults {
			if pr.Error == nil && isHLSPlaylist(pr.OutputPath) && !isMasterPlaylist(pr.OutputPath) {
				// Extract resolution from path
				resolution := extractResolutionFromPath(pr.OutputPath)
				if resolution != "" {
					streamURLs[resolution] = generateResolutionURL(pr.OutputPath, resolution, epochTime)
				}
			}
		}

		// If no master playlist but we have individual playlists, set first one as "auto"
		if _, hasAuto := streamURLs["auto"]; !hasAuto && len(streamURLs) > 0 {
			// Find the highest resolution and use it as auto
			for quality, url := range streamURLs {
				streamURLs["auto"] = url
				h.logger.Info("No master playlist found, using quality as auto",
					zap.String("quality", quality),
				)
				break
			}
		}
	}

	return resolutions, peakVMAFScore, streamURLs
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

// isMasterPlaylist checks if the path is the master HLS playlist
func isMasterPlaylist(path string) bool {
	return len(path) >= 11 && path[len(path)-11:] == "master.m3u8"
}

// isHLSPlaylist checks if the path is an HLS playlist (.m3u8)
func isHLSPlaylist(path string) bool {
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

	// Check if this is the master playlist
	if isMasterPlaylist(outputPath) {
		// Format: /outputs/{epochTime}/master.m3u8
		return fmt.Sprintf("/outputs/%d/master.m3u8", epochTime)
	}

	// For other files, extract filename
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

	// Format: /outputs/{epochTime}/{resolution}/package/{filename}
	// Or fallback to: /streams/{epochTime}/{key}/{filename}
	return fmt.Sprintf("/streams/%d/%s/%s", epochTime, key, filename)
}

// generateResolutionURL generates a URL for a specific resolution's playlist
func generateResolutionURL(outputPath, resolution string, epochTime int64) string {
	// Strip 'p' suffix if present (e.g., "720p" -> "720")
	resolutionPath := resolution
	if len(resolution) > 0 && resolution[len(resolution)-1] == 'p' {
		resolutionPath = resolution[:len(resolution)-1]
	}

	// Format: /outputs/{epochTime}/{resolution}/package/playlist.m3u8
	return fmt.Sprintf("/outputs/%d/%s/package/playlist.m3u8", epochTime, resolutionPath)
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
