package pipeline

import (
	"StreamForge/internal/config"
	"StreamForge/internal/job"
	"StreamForge/internal/pipeline/storage"
	types "StreamForge/pkg"
	"StreamForge/pkg/ffmpeg"
	"StreamForge/pkg/plugin"
	"StreamForge/pkg/plugin/watermark"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.uber.org/zap"
)

// TemporalWorkflow manages the video processing pipeline using Temporal
type TemporalWorkflow struct {
	client     client.Client
	worker     worker.Worker
	logger     *zap.Logger
	config     *config.Config
	transcoder *Transcoder
	packager   *Packager
	monitor    *Monitor
	storage    storage.Storage
	jobManager *job.Manager
}

// WorkflowInput represents the input for the video processing workflow
type WorkflowInput struct {
	FileData        []byte               `json:"-"` // Byte slice instead of io.Reader to prevent "file already closed" errors
	JobID           uuid.UUID            `json:"job_id"`
	Key             string               `json:"key"`
	Bucket          string               `json:"bucket"`
	EpochTime       int64                `json:"epoch_time"`
	Resolutions     []int                `json:"resolutions,omitempty"`       // Optional: custom resolutions (144, 240, 360, 480, 540, 720, 1080)
	IsLLHLSEnabled  *bool                `json:"is_ll_hls_enabled,omitempty"` // Optional: enable/disable LL-HLS
	SegmentDuration *int                 `json:"segment_duration,omitempty"`  // Optional: segment duration in seconds (max 20)
	PluginConfigs   []types.PluginConfig `json:"plugin_configs,omitempty"`    // Optional: per-request plugin overrides
}

// WorkflowOutput represents the output of the video processing workflow
type WorkflowOutput struct {
	IngestResult     *IngestResult     `json:"ingest_result"`
	TranscodeResults []TranscodeResult `json:"transcode_results"`
	VMAFResults      []VMAFResult      `json:"vmaf_results"`
	PackageResults   []PackageResult   `json:"package_results"`
	StorageResults   []StorageResult   `json:"storage_results"`
	Duration         time.Duration     `json:"duration"`
}

// ActivityInput represents input for activities
type ActivityInput struct {
	JobID         uuid.UUID            `json:"job_id"`
	FilePath      string               `json:"file_path"`
	Key           string               `json:"key"`
	Bucket        string               `json:"bucket"`
	EpochTime     int64                `json:"epoch_time"`
	Resolution    string               `json:"resolution,omitempty"`
	Resolutions   []int                `json:"resolutions,omitempty"`    // Optional custom resolutions
	PluginConfigs []types.PluginConfig `json:"plugin_configs,omitempty"` // Optional plugin configurations
}

// PackageInput represents input for package activity
type PackageInput struct {
	JobID           uuid.UUID `json:"job_id"`
	FilePaths       []string  `json:"file_paths"`
	Key             string    `json:"key"`
	Bucket          string    `json:"bucket"`
	EpochTime       int64     `json:"epoch_time"`
	IsLLHLSEnabled  *bool     `json:"is_ll_hls_enabled,omitempty"`
	SegmentDuration *int      `json:"segment_duration,omitempty"`
}

// ActivityOutput represents output from activities
type ActivityOutput struct {
	FilePath string `json:"file_path"`
	Error    error  `json:"error,omitempty"`
}

// NewTemporalWorkflow creates a new Temporal workflow manager
func NewTemporalWorkflow(
	client client.Client,
	logger *zap.Logger,
	cfg *config.Config,
	transcoder *Transcoder,
	packager *Packager,
	monitor *Monitor,
	storage storage.Storage,
	jobManager *job.Manager,
) *TemporalWorkflow {
	return &TemporalWorkflow{
		client:     client,
		logger:     logger,
		config:     cfg,
		transcoder: transcoder,
		packager:   packager,
		monitor:    monitor,
		storage:    storage,
		jobManager: jobManager,
	}
}

// StartWorker starts the Temporal worker
func (tw *TemporalWorkflow) StartWorker() error {
	tw.worker = worker.New(tw.client, "video-processing", worker.Options{})

	// Create activities instance with dependencies
	activities := NewActivities(
		tw.transcoder,
		tw.packager,
		tw.monitor,
		tw.storage,
		tw.config,
		tw.logger,
		tw.jobManager,
	)

	// Register workflow and activities
	tw.worker.RegisterWorkflow(VideoProcessingWorkflow)
	tw.worker.RegisterActivity(activities.SaveFileLocallyActivity)
	tw.worker.RegisterActivity(activities.PluginActivity)
	tw.worker.RegisterActivity(activities.TranscodeActivity)
	tw.worker.RegisterActivity(activities.VMAFValidationActivity)
	tw.worker.RegisterActivity(activities.PackageActivity)
	tw.worker.RegisterActivity(activities.StoreActivity)

	return tw.worker.Start()
}

// StopWorker stops the Temporal worker
func (tw *TemporalWorkflow) StopWorker() {
	if tw.worker != nil {
		tw.worker.Stop()
	}
}

// ExecuteWorkflow executes the video processing workflow
func (tw *TemporalWorkflow) ExecuteWorkflow(ctx context.Context, input WorkflowInput) (*WorkflowOutput, error) {
	// Step 1: Save file locally before starting the workflow
	// (Temporal workflows can't accept io.Reader, so we need to save it first)
	if input.FileData != nil && len(input.FileData) > 0 {
		tw.logger.Info("Saving uploaded file locally",
			zap.String("job_id", input.JobID.String()),
			zap.String("key", input.Key),
			zap.Int64("epoch_time", input.EpochTime),
			zap.Int("file_size", len(input.FileData)))

		// Emit uploading progress
		if tw.jobManager != nil {
			tw.jobManager.EmitProgress(ctx, input.JobID, job.StageUploading, 10, "Uploading video file", nil)
		}

		localFilePath, err := tw.saveFileLocally(ctx, input.FileData, input.Key)
		if err != nil {
			tw.logger.Error("Failed to save file locally", zap.Error(err))
			if tw.jobManager != nil {
				tw.jobManager.EmitError(ctx, input.JobID, fmt.Sprintf("Failed to save file: %v", err))
			}
			return nil, fmt.Errorf("failed to save file locally: %w", err)
		}
		tw.logger.Info("File saved locally", zap.String("local_path", localFilePath))
	}

	// Step 2: Start the Temporal workflow
	workflowOptions := client.StartWorkflowOptions{
		ID:                       fmt.Sprintf("video-processing-%s-%d", input.Key, input.EpochTime),
		TaskQueue:                "video-processing",
		WorkflowExecutionTimeout: 60 * time.Minute, // Maximum time for entire workflow
		WorkflowRunTimeout:       60 * time.Minute, // Maximum time for workflow run
	}

	we, err := tw.client.ExecuteWorkflow(ctx, workflowOptions, VideoProcessingWorkflow, input)
	if err != nil {
		if tw.jobManager != nil {
			tw.jobManager.EmitError(ctx, input.JobID, fmt.Sprintf("Failed to start workflow: %v", err))
		}
		return nil, fmt.Errorf("failed to start workflow: %w", err)
	}

	var result WorkflowOutput
	err = we.Get(ctx, &result)
	if err != nil {
		if tw.jobManager != nil {
			tw.jobManager.EmitError(ctx, input.JobID, fmt.Sprintf("Workflow execution failed: %v", err))
		}
		return nil, fmt.Errorf("workflow execution failed: %w", err)
	}

	return &result, nil
}

// saveFileLocally saves the uploaded file to local storage
func (tw *TemporalWorkflow) saveFileLocally(ctx context.Context, fileData []byte, key string) (string, error) {
	// Create local input directory if it doesn't exist
	inputDir := "./input"
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create input directory: %w", err)
	}

	// Create local file path
	localFilePath := filepath.Join(inputDir, key)

	// Retry writing the file (not reading - we already have the data in memory)
	err := Retry(ctx, tw.logger, tw.config.Pipeline.Retry, fmt.Sprintf("save file %s", key), func() error {
		// Create the local file
		outFile, err := os.Create(localFilePath)
		if err != nil {
			return err
		}
		defer outFile.Close()

		// Write the file data to disk
		_, err = outFile.Write(fileData)
		return err
	})

	if err != nil {
		return "", fmt.Errorf("failed to save file locally: %w", err)
	}

	return localFilePath, nil
}

// VideoProcessingWorkflow is the main Temporal workflow
func VideoProcessingWorkflow(ctx workflow.Context, input WorkflowInput) (WorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	startTime := workflow.Now(ctx)

	logger.Info("Starting video processing workflow",
		"key", input.Key,
		"bucket", input.Bucket,
		"epoch_time", input.EpochTime)

	var result WorkflowOutput

	// Define activity options with proper timeouts
	activityOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute, // Maximum time for activity execution
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    3,
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, activityOptions)

	// Step 1: Save file locally
	saveFileInput := ActivityInput{
		JobID:     input.JobID,
		FilePath:  input.Key,
		Key:       input.Key,
		Bucket:    input.Bucket,
		EpochTime: input.EpochTime,
	}

	var saveFileOutput ActivityOutput
	err := workflow.ExecuteActivity(ctx, "SaveFileLocallyActivity", saveFileInput).Get(ctx, &saveFileOutput)
	if err != nil {
		return result, fmt.Errorf("failed to save file locally: %w", err)
	}

	localFilePath := saveFileOutput.FilePath
	logger.Info("File saved locally", "path", localFilePath)

	// Step 1.5: Apply plugins if enabled
	pluginInput := ActivityInput{
		JobID:         input.JobID,
		FilePath:      localFilePath,
		Key:           input.Key,
		EpochTime:     input.EpochTime,
		PluginConfigs: input.PluginConfigs,
	}

	var pluginOutput ActivityOutput
	err = workflow.ExecuteActivity(ctx, "PluginActivity", pluginInput).Get(ctx, &pluginOutput)
	if err != nil {
		return result, fmt.Errorf("plugin processing failed: %w", err)
	}

	// Update file path if plugins modified it
	if pluginOutput.FilePath != "" {
		localFilePath = pluginOutput.FilePath
		logger.Info("Plugins applied", "output_path", localFilePath)
	}

	// Step 2: Transcode video (longer timeout for video processing)
	transcodeOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Minute, // Longer timeout for transcoding
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    2, // Fewer retries for long-running activities
			InitialInterval:    30 * time.Second,
			BackoffCoefficient: 2.0,
		},
	}
	transcodeCtx := workflow.WithActivityOptions(ctx, transcodeOptions)

	transcodeInput := ActivityInput{
		JobID:       input.JobID,
		FilePath:    localFilePath,
		Key:         input.Key,
		EpochTime:   input.EpochTime,
		Resolutions: input.Resolutions, // Pass custom resolutions if provided
	}

	var transcodeResults []TranscodeResult
	err = workflow.ExecuteActivity(transcodeCtx, "TranscodeActivity", transcodeInput).Get(ctx, &transcodeResults)
	if err != nil {
		return result, fmt.Errorf("transcoding failed: %w", err)
	}

	result.TranscodeResults = transcodeResults
	logger.Info("Transcoding completed", "results", len(transcodeResults))

	// Step 3: VMAF validation (if enabled) - run in parallel
	vmafResults := make([]VMAFResult, len(transcodeResults))
	var vmafHasError bool

	// Note: We need to check VMAF config - assuming it's enabled by checking config
	// In a real implementation, this would be passed through WorkflowInput or ActivityInput
	if len(transcodeResults) > 0 {
		// VMAF validation options (medium timeout for quality analysis)
		vmafOptions := workflow.ActivityOptions{
			StartToCloseTimeout: 15 * time.Minute, // Medium timeout for VMAF
			RetryPolicy: &temporal.RetryPolicy{
				MaximumAttempts:    2,
				InitialInterval:    15 * time.Second,
				BackoffCoefficient: 2.0,
			},
		}
		vmafCtx := workflow.WithActivityOptions(ctx, vmafOptions)

		// Run VMAF validation in parallel using workflow.Go
		futures := make([]workflow.Future, 0, len(transcodeResults))
		for i, tr := range transcodeResults {
			if tr.Error != nil {
				vmafResults[i] = VMAFResult{
					InputFile:  localFilePath,
					OutputFile: tr.OutputPath,
					Error:      tr.Error,
				}
				continue
			}

			vmafInput := ActivityInput{
				JobID:     input.JobID,
				FilePath:  localFilePath,
				Key:       tr.OutputPath,
				EpochTime: input.EpochTime,
				Bucket:    input.Bucket,
			}

			// Capture index for closure
			idx := i
			future := workflow.ExecuteActivity(vmafCtx, "VMAFValidationActivity", vmafInput)
			futures = append(futures, future)

			// Use workflow.Go to handle results asynchronously
			workflow.Go(vmafCtx, func(gCtx workflow.Context) {
				var vmafResult VMAFResult
				err := future.Get(gCtx, &vmafResult)
				if err != nil {
					logger.Error("VMAF validation failed", "error", err, "index", idx)
					vmafHasError = true
					vmafResults[idx] = VMAFResult{
						InputFile:  localFilePath,
						OutputFile: transcodeResults[idx].OutputPath,
						Error:      err,
					}
				} else {
					vmafResults[idx] = vmafResult
				}
			})
		}

		// Wait for all VMAF validations to complete
		for _, future := range futures {
			var vmafResult VMAFResult
			_ = future.Get(vmafCtx, &vmafResult)
		}

		result.VMAFResults = vmafResults
		logger.Info("VMAF validation completed", "results", len(vmafResults), "has_error", vmafHasError)
	}

	// Step 4: Collect successful transcode outputs for packaging
	var transcodeOutputs []string
	for _, tr := range transcodeResults {
		if tr.Error == nil && !vmafHasError {
			transcodeOutputs = append(transcodeOutputs, tr.OutputPath)
		}
	}

	if len(transcodeOutputs) == 0 {
		logger.Error("No successful transcoding outputs or VMAF validation failed")
		return result, fmt.Errorf("no successful transcoding outputs")
	}

	// Step 5: Package all successful transcodes together
	packageInput := PackageInput{
		JobID:           input.JobID,
		FilePaths:       transcodeOutputs,
		Key:             input.Key,
		EpochTime:       input.EpochTime,
		Bucket:          input.Bucket,
		IsLLHLSEnabled:  input.IsLLHLSEnabled,  // Pass custom LL-HLS setting if provided
		SegmentDuration: input.SegmentDuration, // Pass custom segment duration if provided
	}

	// Package options (medium timeout for packaging)
	packageOptions := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute, // Medium timeout for packaging
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:    2,
			InitialInterval:    15 * time.Second,
			BackoffCoefficient: 2.0,
		},
	}
	packageCtx := workflow.WithActivityOptions(ctx, packageOptions)

	var packageResults []PackageResult
	err = workflow.ExecuteActivity(packageCtx, "PackageActivity", packageInput).Get(ctx, &packageResults)
	if err != nil {
		logger.Error("Packaging failed", "error", err)
		return result, fmt.Errorf("packaging failed: %w", err)
	}

	result.PackageResults = packageResults
	logger.Info("Packaging completed", "results", len(packageResults))

	// Step 6: Store results
	var storageResults []StorageResult
	for _, pr := range packageResults {
		if pr.Error != nil {
			storageResults = append(storageResults, StorageResult{Key: pr.OutputPath, Error: pr.Error})
			continue
		}

		storeInput := ActivityInput{
			JobID:     input.JobID,
			FilePath:  pr.OutputPath,
			Key:       input.Key,
			Bucket:    input.Bucket,
			EpochTime: input.EpochTime,
		}

		// Storage options (shorter timeout for storage operations)
		storageOptions := workflow.ActivityOptions{
			StartToCloseTimeout: 5 * time.Minute, // Shorter timeout for storage
			RetryPolicy: &temporal.RetryPolicy{
				MaximumAttempts:    3,
				InitialInterval:    5 * time.Second,
				BackoffCoefficient: 2.0,
			},
		}
		storageCtx := workflow.WithActivityOptions(ctx, storageOptions)

		var storageResult StorageResult
		err = workflow.ExecuteActivity(storageCtx, "StoreActivity", storeInput).Get(ctx, &storageResult)
		if err != nil {
			logger.Error("Storage failed", "error", err)
			storageResult = StorageResult{
				Key:   pr.OutputPath,
				Error: err,
			}
		}
		storageResults = append(storageResults, storageResult)
	}

	result.StorageResults = storageResults
	result.Duration = workflow.Now(ctx).Sub(startTime)

	logger.Info("Video processing workflow completed",
		"duration", result.Duration,
		"transcode_results", len(result.TranscodeResults),
		"vmaf_results", len(result.VMAFResults),
		"package_results", len(result.PackageResults),
		"storage_results", len(result.StorageResults))

	return result, nil
}

// Activities holds dependencies for Temporal activities
type Activities struct {
	transcoder     *Transcoder
	packager       *Packager
	monitor        *Monitor
	storage        storage.Storage
	config         *config.Config
	logger         *zap.Logger
	jobManager     *job.Manager
	pluginRegistry *plugin.Registry
}

// NewActivities creates a new Activities instance
func NewActivities(
	transcoder *Transcoder,
	packager *Packager,
	monitor *Monitor,
	storage storage.Storage,
	config *config.Config,
	logger *zap.Logger,
	jobManager *job.Manager,
) *Activities {
	// Initialize plugin registry
	pluginRegistry := plugin.NewRegistry()

	// Register watermark plugin
	ffmpegClient := ffmpeg.NewFFmpeg(config.Pipeline.FFMpegPath)
	watermarkPlugin := watermark.NewWatermarkPlugin(ffmpegClient, logger, config.Pipeline.Retry)
	if err := pluginRegistry.Register(watermarkPlugin); err != nil {
		logger.Error("Failed to register watermark plugin", zap.Error(err))
	}

	return &Activities{
		transcoder:     transcoder,
		packager:       packager,
		monitor:        monitor,
		storage:        storage,
		config:         config,
		logger:         logger,
		jobManager:     jobManager,
		pluginRegistry: pluginRegistry,
	}
}

// SaveFileLocallyActivity saves the uploaded file locally
func (a *Activities) SaveFileLocallyActivity(ctx context.Context, input ActivityInput) (ActivityOutput, error) {
	logger := activity.GetLogger(ctx)

	inputDir := "./input"
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return ActivityOutput{}, fmt.Errorf("failed to create input directory: %w", err)
	}

	localFilePath := filepath.Join(inputDir, input.Key)

	logger.Info("File already saved locally", "path", localFilePath)
	return ActivityOutput{FilePath: localFilePath}, nil
}

// PluginActivity applies all enabled plugins to the video
func (a *Activities) PluginActivity(ctx context.Context, input ActivityInput) (ActivityOutput, error) {
	logger := activity.GetLogger(ctx)

	// Merge global config with per-request overrides
	mergedConfigs := a.mergePluginConfigs(input.PluginConfigs)

	// Check if there are any enabled plugins
	if !a.hasEnabledPlugins(mergedConfigs) {
		logger.Info("No enabled plugins, skipping plugin processing")
		return ActivityOutput{FilePath: input.FilePath}, nil
	}

	logger.Info("Starting plugin activity", "input", input.FilePath, "plugins", len(mergedConfigs))

	// Create plugin processor
	processor := NewPluginProcessor(a.pluginRegistry, mergedConfigs, a.logger)

	// Process plugins
	return processor.ProcessPlugins(ctx, input)
}

// mergePluginConfigs merges global config with per-request overrides
func (a *Activities) mergePluginConfigs(requestConfigs []types.PluginConfig) []types.PluginConfig {
	// If no request configs provided, use global config
	if len(requestConfigs) == 0 {
		return a.config.Plugins
	}

	// Create a map of request configs by name for easy lookup
	requestMap := make(map[string]types.PluginConfig)
	for _, rc := range requestConfigs {
		requestMap[rc.Name] = rc
	}

	// Start with global config and override with request config if present
	merged := make([]types.PluginConfig, 0)
	processedNames := make(map[string]bool)

	// First, process global configs
	for _, globalConfig := range a.config.Plugins {
		if requestConfig, exists := requestMap[globalConfig.Name]; exists {
			// Request override exists, use it
			merged = append(merged, requestConfig)
			processedNames[globalConfig.Name] = true
		} else {
			// No override, use global config
			merged = append(merged, globalConfig)
			processedNames[globalConfig.Name] = true
		}
	}

	// Add any request configs that weren't in global config
	for _, requestConfig := range requestConfigs {
		if !processedNames[requestConfig.Name] {
			merged = append(merged, requestConfig)
		}
	}

	return merged
}

// hasEnabledPlugins checks if there are any enabled plugins in the config
func (a *Activities) hasEnabledPlugins(configs []types.PluginConfig) bool {
	for _, config := range configs {
		if config.Enabled {
			return true
		}
	}
	return false
}

// TranscodeActivity handles video transcoding using the existing transcoder
func (a *Activities) TranscodeActivity(ctx context.Context, input ActivityInput) ([]TranscodeResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting transcoding activity", "input", input.FilePath, "epoch_time", input.EpochTime, "custom_resolutions", input.Resolutions)

	// Emit progress: starting transcoding
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageTranscoding, 20, "Initializing video transcoding", nil)
	}

	// Determine which transcode configs to use
	transcodeConfigs := a.config.Transcode
	if len(input.Resolutions) > 0 {
		// Build custom transcode configs from provided resolutions
		transcodeConfigs = buildTranscodeConfigs(input.Resolutions)
		logger.Info("Using custom resolutions", "resolutions", input.Resolutions, "configs", len(transcodeConfigs))
	}

	totalResolutions := len(transcodeConfigs)

	// Use the actual transcoder to transcode the video
	transcodeResults, err := a.transcoder.Transcode(ctx, input.FilePath, transcodeConfigs, input.EpochTime)
	if err != nil {
		logger.Error("Transcoding failed", "error", err)
		// Return partial results if available
		if transcodeResults != nil && len(transcodeResults) > 0 {
			return transcodeResults, nil
		}
		return nil, fmt.Errorf("transcoding failed: %w", err)
	}

	// Emit detailed progress for each successful resolution
	if a.jobManager != nil {
		successfulCount := 0
		resolutions := []string{}

		for _, result := range transcodeResults {
			if result.Error == nil {
				successfulCount++
				// Extract resolution from codec config or output path
				resolution := extractResolution(result.OutputPath)
				if resolution != "" {
					resolutions = append(resolutions, resolution)
				}

				// Calculate progress: 20% start + 30% for transcoding
				progressPercent := 20 + int((float64(successfulCount)/float64(totalResolutions))*30)
				a.jobManager.EmitProgress(ctx, input.JobID, job.StageTranscoding, progressPercent,
					fmt.Sprintf("Transcoded resolution: %s", resolution),
					map[string]interface{}{
						"resolution": resolution,
						"completed":  successfulCount,
						"total":      totalResolutions,
					})
			}
		}

		// Final transcoding progress
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageTranscoding, 50,
			fmt.Sprintf("Transcoded %d resolutions: %v", successfulCount, resolutions),
			map[string]interface{}{
				"resolutions": resolutions,
			})
	}

	logger.Info("Transcoding activity completed", "results", len(transcodeResults))
	return transcodeResults, nil
}

// extractResolution extracts resolution from output path
func extractResolution(outputPath string) string {
	// Extract resolution from path like "outputs/123456/720/video.mp4"
	dir, _ := filepath.Split(outputPath)
	resolution := filepath.Base(filepath.Dir(dir))
	if resolution != "" && resolution != "." && resolution != ".." {
		return resolution + "p"
	}
	return ""
}

// VMAFValidationActivity handles VMAF quality validation using the existing monitor
func (a *Activities) VMAFValidationActivity(ctx context.Context, input ActivityInput) (VMAFResult, error) {
	logger := activity.GetLogger(ctx)

	resolution := extractResolution(input.Key)
	logger.Info("Starting VMAF validation activity", "input", input.FilePath, "output", input.Key, "resolution", resolution)

	// Emit progress: starting VMAF validation
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageValidation, 55,
			fmt.Sprintf("Analyzing quality for %s", resolution),
			map[string]interface{}{
				"resolution": resolution,
			})
	}

	// Use the actual monitor to validate VMAF quality
	vmafResult, err := a.monitor.ValidateVMAF(ctx, input.FilePath, input.Key)
	if err != nil {
		logger.Error("VMAF validation failed", "error", err)
		return VMAFResult{
			InputFile:  input.FilePath,
			OutputFile: input.Key,
			Error:      err,
		}, err
	}

	// Emit progress with VMAF score
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageValidation, 60,
			fmt.Sprintf("Quality validated for %s: %.2f VMAF", resolution, vmafResult.VMAFScore),
			map[string]interface{}{
				"resolution": resolution,
				"vmaf_score": fmt.Sprintf("%.2f", vmafResult.VMAFScore),
			})
	}

	logger.Info("VMAF validation activity completed", "score", vmafResult.VMAFScore, "resolution", resolution)
	return *vmafResult, nil
}

// PackageActivity handles video packaging using the existing packager
func (a *Activities) PackageActivity(ctx context.Context, input PackageInput) ([]PackageResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting packaging activity", "inputs", len(input.FilePaths), "epoch_time", input.EpochTime,
		"custom_llhls", input.IsLLHLSEnabled, "custom_segment_duration", input.SegmentDuration)

	// Emit progress: starting packaging
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StagePackaging, 65, "Preparing video packaging", nil)
	}

	// Determine which package configs to use
	packageConfigs := a.config.Package
	if input.IsLLHLSEnabled != nil || input.SegmentDuration != nil {
		// Build custom package configs from provided settings
		packageConfigs = buildPackageConfigs(a.config.Package, input.IsLLHLSEnabled, input.SegmentDuration)
		logger.Info("Using custom package settings", "configs", len(packageConfigs))
	}

	// Use the actual packager to package the videos
	packageResults, err := a.packager.Package(ctx, input.FilePaths, packageConfigs, input.EpochTime)
	if err != nil {
		logger.Error("Packaging failed", "error", err)
		// Return partial results if available
		if packageResults != nil && len(packageResults) > 0 {
			return packageResults, nil
		}
		return nil, fmt.Errorf("packaging failed: %w", err)
	}

	// Emit detailed progress for each successful package format
	if a.jobManager != nil {
		successfulCount := 0
		formats := []string{}

		for _, result := range packageResults {
			if result.Error == nil {
				successfulCount++
				// Extract format from config or result
				format := extractFormat(result.OutputPath)
				if format != "" {
					formats = append(formats, format)
				}

				// Calculate progress: 65% start + 20% for packaging (65-85%)
				progressPercent := 65 + int((float64(successfulCount)/float64(len(packageResults)))*20)
				a.jobManager.EmitProgress(ctx, input.JobID, job.StagePackaging, progressPercent,
					fmt.Sprintf("Created %s manifest", format),
					map[string]interface{}{
						"format":    format,
						"completed": successfulCount,
						"total":     len(packageResults),
					})
			}
		}

		// Final packaging progress
		a.jobManager.EmitProgress(ctx, input.JobID, job.StagePackaging, 85,
			fmt.Sprintf("Packaging completed: %v", formats),
			map[string]interface{}{
				"formats": formats,
			})
	}

	logger.Info("Packaging activity completed", "results", len(packageResults))
	return packageResults, nil
}

// extractFormat extracts packaging format from output path
func extractFormat(outputPath string) string {
	// Extract format from path like "outputs/123456/hls/master.m3u8" or "outputs/123456/dash/manifest.mpd"
	dir, _ := filepath.Split(outputPath)
	format := filepath.Base(filepath.Dir(dir))
	if format != "" && format != "." && format != ".." {
		return format
	}
	// Try to determine from file extension
	ext := filepath.Ext(outputPath)
	switch ext {
	case ".m3u8":
		return "HLS"
	case ".mpd":
		return "DASH"
	default:
		return "unknown"
	}
}

// StoreActivity handles storing the packaged results using the existing storage
func (a *Activities) StoreActivity(ctx context.Context, input ActivityInput) (StorageResult, error) {
	logger := activity.GetLogger(ctx)

	fileName := filepath.Base(input.FilePath)
	logger.Info("Starting storage activity", "key", input.Key, "file_path", input.FilePath, "file_name", fileName)

	// Emit progress: starting storage upload
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageStorage, 87,
			fmt.Sprintf("Preparing to upload: %s", fileName),
			map[string]interface{}{
				"file_name": fileName,
			})
	}

	// Check if using local storage
	if a.config.Storage.Type == "local" || a.storage == nil {
		logger.Info("Using local storage - skipping upload")
		if a.jobManager != nil {
			a.jobManager.EmitProgress(ctx, input.JobID, job.StageStorage, 95,
				"Files stored locally",
				map[string]interface{}{
					"storage_type": "local",
					"path":         input.FilePath,
				})
		}
		return StorageResult{
			Key:   input.FilePath,
			Error: nil,
		}, nil
	}

	// Use the actual storage to upload the file
	storageKey := fmt.Sprintf("%s/%s", input.Key, fileName)

	// Get file size for progress details
	fileInfo, err := os.Stat(input.FilePath)
	var fileSizeMB float64
	if err == nil {
		fileSizeMB = float64(fileInfo.Size()) / (1024 * 1024)
	}

	// Emit progress with file details
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageStorage, 90,
			fmt.Sprintf("Uploading %s (%.2f MB)", fileName, fileSizeMB),
			map[string]interface{}{
				"file_name":    fileName,
				"file_size_mb": fmt.Sprintf("%.2f", fileSizeMB),
				"storage_key":  storageKey,
			})
	}

	err = Retry(ctx, a.logger, a.config.Pipeline.Retry, fmt.Sprintf("store %s", input.FilePath), func() error {
		file, err := os.Open(input.FilePath)
		if err != nil {
			return err
		}
		defer file.Close()
		return a.storage.Upload(ctx, input.Bucket, storageKey, file)
	})

	if err != nil {
		logger.Error("Storage upload failed", "error", err, "key", storageKey)
		return StorageResult{
			Key:   storageKey,
			Error: err,
		}, err
	}

	// Emit progress: storage upload completed
	if a.jobManager != nil {
		a.jobManager.EmitProgress(ctx, input.JobID, job.StageStorage, 93,
			fmt.Sprintf("Uploaded: %s", fileName),
			map[string]interface{}{
				"file_name":   fileName,
				"storage_key": storageKey,
			})
	}

	logger.Info("Storage activity completed", "key", storageKey)
	return StorageResult{
		Key:   storageKey,
		Error: nil,
	}, nil
}

// buildTranscodeConfigs creates codec configurations from resolution values
func buildTranscodeConfigs(resolutions []int) []types.CodecConfig {
	// Define recommended bitrates for each resolution
	bitrateMap := map[int]string{
		144:  "100k",
		240:  "200k",
		360:  "250k",
		480:  "500k",
		540:  "800k",
		720:  "1000k",
		1080: "1500k",
	}

	configs := make([]types.CodecConfig, 0, len(resolutions))
	for _, res := range resolutions {
		bitrate, ok := bitrateMap[res]
		if !ok {
			// Default bitrate if resolution not in map
			bitrate = "500k"
		}

		configs = append(configs, types.CodecConfig{
			Codec:      "libx264",
			Bitrate:    bitrate,
			Resolution: fmt.Sprintf("%d", res),
		})
	}

	return configs
}

// buildPackageConfigs creates package configurations from optional settings
func buildPackageConfigs(defaultConfigs []types.PackageConfig, isLLHLSEnabled *bool, segmentDuration *int) []types.PackageConfig {
	configs := make([]types.PackageConfig, len(defaultConfigs))

	for i, cfg := range defaultConfigs {
		// Copy the default config
		configs[i] = cfg

		// Override LLHLS setting if provided
		if isLLHLSEnabled != nil {
			configs[i].LLHLS = *isLLHLSEnabled
		}

		// Override segment duration if provided
		if segmentDuration != nil {
			configs[i].SegmentDuration = *segmentDuration
		}
	}

	return configs
}
