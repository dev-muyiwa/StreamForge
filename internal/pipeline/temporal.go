package pipeline

import (
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline/storage"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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
}

// WorkflowInput represents the input for the video processing workflow
type WorkflowInput struct {
	File      io.Reader `json:"-"`
	Key       string    `json:"key"`
	Bucket    string    `json:"bucket"`
	EpochTime int64     `json:"epoch_time"`
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
	FilePath   string `json:"file_path"`
	Key        string `json:"key"`
	Bucket     string `json:"bucket"`
	EpochTime  int64  `json:"epoch_time"`
	Resolution string `json:"resolution,omitempty"`
}

// PackageInput represents input for package activity
type PackageInput struct {
	FilePaths []string `json:"file_paths"`
	Key       string   `json:"key"`
	Bucket    string   `json:"bucket"`
	EpochTime int64    `json:"epoch_time"`
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
) *TemporalWorkflow {
	return &TemporalWorkflow{
		client:     client,
		logger:     logger,
		config:     cfg,
		transcoder: transcoder,
		packager:   packager,
		monitor:    monitor,
		storage:    storage,
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
	)

	// Register workflow and activities
	tw.worker.RegisterWorkflow(VideoProcessingWorkflow)
	tw.worker.RegisterActivity(activities.SaveFileLocallyActivity)
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
	if input.File != nil {
		tw.logger.Info("Saving uploaded file locally", zap.String("key", input.Key), zap.Int64("epoch_time", input.EpochTime))
		localFilePath, err := tw.saveFileLocally(ctx, input.File, input.Key)
		if err != nil {
			tw.logger.Error("Failed to save file locally", zap.Error(err))
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
		return nil, fmt.Errorf("failed to start workflow: %w", err)
	}

	var result WorkflowOutput
	err = we.Get(ctx, &result)
	if err != nil {
		return nil, fmt.Errorf("workflow execution failed: %w", err)
	}

	return &result, nil
}

// saveFileLocally saves the uploaded file to local storage
func (tw *TemporalWorkflow) saveFileLocally(ctx context.Context, file io.Reader, key string) (string, error) {
	// Create local input directory if it doesn't exist
	inputDir := "./input"
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create input directory: %w", err)
	}

	// Create local file path
	localFilePath := filepath.Join(inputDir, key)

	err := Retry(ctx, tw.logger, tw.config.Pipeline.Retry, fmt.Sprintf("save file %s", key), func() error {
		// Create the local file
		outFile, err := os.Create(localFilePath)
		if err != nil {
			return err
		}
		defer outFile.Close()

		// Copy the uploaded file to local storage
		_, err = io.Copy(outFile, file)
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
		FilePath:  localFilePath,
		Key:       input.Key,
		EpochTime: input.EpochTime,
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
		FilePaths: transcodeOutputs,
		Key:       input.Key,
		EpochTime: input.EpochTime,
		Bucket:    input.Bucket,
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
	transcoder *Transcoder
	packager   *Packager
	monitor    *Monitor
	storage    storage.Storage
	config     *config.Config
	logger     *zap.Logger
}

// NewActivities creates a new Activities instance
func NewActivities(
	transcoder *Transcoder,
	packager *Packager,
	monitor *Monitor,
	storage storage.Storage,
	config *config.Config,
	logger *zap.Logger,
) *Activities {
	return &Activities{
		transcoder: transcoder,
		packager:   packager,
		monitor:    monitor,
		storage:    storage,
		config:     config,
		logger:     logger,
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

// TranscodeActivity handles video transcoding using the existing transcoder
func (a *Activities) TranscodeActivity(ctx context.Context, input ActivityInput) ([]TranscodeResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting transcoding activity", "input", input.FilePath, "epoch_time", input.EpochTime)

	// Use the actual transcoder to transcode the video
	transcodeResults, err := a.transcoder.Transcode(ctx, input.FilePath, a.config.Transcode, input.EpochTime)
	if err != nil {
		logger.Error("Transcoding failed", "error", err)
		// Return partial results if available
		if transcodeResults != nil && len(transcodeResults) > 0 {
			return transcodeResults, nil
		}
		return nil, fmt.Errorf("transcoding failed: %w", err)
	}

	logger.Info("Transcoding activity completed", "results", len(transcodeResults))
	return transcodeResults, nil
}

// VMAFValidationActivity handles VMAF quality validation using the existing monitor
func (a *Activities) VMAFValidationActivity(ctx context.Context, input ActivityInput) (VMAFResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting VMAF validation activity", "input", input.FilePath, "output", input.Key)

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

	logger.Info("VMAF validation activity completed", "score", vmafResult.VMAFScore)
	return *vmafResult, nil
}

// PackageActivity handles video packaging using the existing packager
func (a *Activities) PackageActivity(ctx context.Context, input PackageInput) ([]PackageResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting packaging activity", "inputs", len(input.FilePaths), "epoch_time", input.EpochTime)

	// Use the actual packager to package the videos
	packageResults, err := a.packager.Package(ctx, input.FilePaths, a.config.Package, input.EpochTime)
	if err != nil {
		logger.Error("Packaging failed", "error", err)
		// Return partial results if available
		if packageResults != nil && len(packageResults) > 0 {
			return packageResults, nil
		}
		return nil, fmt.Errorf("packaging failed: %w", err)
	}

	logger.Info("Packaging activity completed", "results", len(packageResults))
	return packageResults, nil
}

// StoreActivity handles storing the packaged results using the existing storage
func (a *Activities) StoreActivity(ctx context.Context, input ActivityInput) (StorageResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting storage activity", "key", input.Key, "file_path", input.FilePath)

	// Check if using local storage
	if a.config.Storage.Type == "local" || a.storage == nil {
		logger.Info("Using local storage - skipping upload")
		return StorageResult{
			Key:   input.FilePath,
			Error: nil,
		}, nil
	}

	// Use the actual storage to upload the file
	storageKey := fmt.Sprintf("%s/%s", input.Key, filepath.Base(input.FilePath))

	err := Retry(ctx, a.logger, a.config.Pipeline.Retry, fmt.Sprintf("store %s", input.FilePath), func() error {
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

	logger.Info("Storage activity completed", "key", storageKey)
	return StorageResult{
		Key:   storageKey,
		Error: nil,
	}, nil
}
