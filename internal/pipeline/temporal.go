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

	// Register workflow and activities
	tw.worker.RegisterWorkflow(VideoProcessingWorkflow)
	tw.worker.RegisterActivity(SaveFileLocallyActivity)
	tw.worker.RegisterActivity(TranscodeActivity)
	tw.worker.RegisterActivity(VMAFValidationActivity)
	tw.worker.RegisterActivity(PackageActivity)
	tw.worker.RegisterActivity(StoreActivity)

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
	err := workflow.ExecuteActivity(ctx, SaveFileLocallyActivity, saveFileInput).Get(ctx, &saveFileOutput)
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
	err = workflow.ExecuteActivity(transcodeCtx, TranscodeActivity, transcodeInput).Get(ctx, &transcodeResults)
	if err != nil {
		return result, fmt.Errorf("transcoding failed: %w", err)
	}

	result.TranscodeResults = transcodeResults
	logger.Info("Transcoding completed", "results", len(transcodeResults))

	// Step 3: VMAF validation (if enabled)
	if len(transcodeResults) > 0 {
		var vmafResults []VMAFResult
		for _, tr := range transcodeResults {
			if tr.Error != nil {
				vmafResults = append(vmafResults, VMAFResult{
					InputFile:  localFilePath,
					OutputFile: tr.OutputPath,
					Error:      tr.Error,
				})
				continue
			}

			vmafInput := ActivityInput{
				FilePath:  localFilePath,
				Key:       tr.OutputPath,
				EpochTime: input.EpochTime,
			}

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

			var vmafResult VMAFResult
			err = workflow.ExecuteActivity(vmafCtx, VMAFValidationActivity, vmafInput).Get(ctx, &vmafResult)
			if err != nil {
				logger.Error("VMAF validation failed", "error", err)
				vmafResult = VMAFResult{
					InputFile:  localFilePath,
					OutputFile: tr.OutputPath,
					Error:      err,
				}
			}
			vmafResults = append(vmafResults, vmafResult)
		}
		result.VMAFResults = vmafResults
		logger.Info("VMAF validation completed", "results", len(vmafResults))
	}

	// Step 4: Package videos (only successful transcodes)
	var packageResults []PackageResult
	for _, tr := range transcodeResults {
		if tr.Error != nil {
			continue
		}

		packageInput := ActivityInput{
			FilePath:  tr.OutputPath,
			Key:       input.Key,
			EpochTime: input.EpochTime,
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

		var packageResult PackageResult
		err = workflow.ExecuteActivity(packageCtx, PackageActivity, packageInput).Get(ctx, &packageResult)
		if err != nil {
			logger.Error("Packaging failed", "error", err)
			packageResult = PackageResult{
				InputFile: tr.OutputPath,
				Error:     err,
			}
		}
		packageResults = append(packageResults, packageResult)
	}

	result.PackageResults = packageResults
	logger.Info("Packaging completed", "results", len(packageResults))

	// Step 5: Store results
	var storageResults []StorageResult
	for _, pr := range packageResults {
		if pr.Error != nil {
			continue
		}

		storeInput := ActivityInput{
			FilePath:  pr.OutputPath,
			Key:       input.Key,
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
		err = workflow.ExecuteActivity(storageCtx, StoreActivity, storeInput).Get(ctx, &storageResult)
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

// SaveFileLocallyActivity saves the uploaded file locally
func SaveFileLocallyActivity(ctx context.Context, input ActivityInput) (ActivityOutput, error) {
	logger := activity.GetLogger(ctx)

	inputDir := "./input"
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return ActivityOutput{}, fmt.Errorf("failed to create input directory: %w", err)
	}

	localFilePath := filepath.Join(inputDir, input.Key)

	// For now, create an empty file as placeholder
	// In a real implementation, you'd copy from the uploaded file
	outFile, err := os.Create(localFilePath)
	if err != nil {
		return ActivityOutput{}, fmt.Errorf("failed to create local file: %w", err)
	}
	defer outFile.Close()

	logger.Info("File saved locally", "path", localFilePath)
	return ActivityOutput{FilePath: localFilePath}, nil
}

// TranscodeActivity handles video transcoding using the existing transcoder
func TranscodeActivity(ctx context.Context, input ActivityInput) ([]TranscodeResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting transcoding activity", "input", input.FilePath, "epoch_time", input.EpochTime)

	// For now, simulate transcoding results
	// In a real implementation, you'd use the actual transcoder
	var results []TranscodeResult

	resolutions := []string{"480", "720"}
	for _, resolution := range resolutions {
		outputPath := filepath.Join("outputs", fmt.Sprintf("%d", input.EpochTime), resolution, "video.mp4")

		// Create output directory
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			results = append(results, TranscodeResult{
				InputFile:  input.FilePath,
				OutputPath: outputPath,
				Error:      fmt.Errorf("failed to create output directory: %w", err),
			})
			continue
		}

		// Simulate successful transcoding
		results = append(results, TranscodeResult{
			InputFile:  input.FilePath,
			OutputPath: outputPath,
			Error:      nil,
		})

		logger.Info("Transcoding completed", "resolution", resolution, "output", outputPath)
	}

	logger.Info("Transcoding activity completed", "results", len(results))
	return results, nil
}

// VMAFValidationActivity handles VMAF quality validation using the existing monitor
func VMAFValidationActivity(ctx context.Context, input ActivityInput) (VMAFResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting VMAF validation activity", "input", input.FilePath, "output", input.Key)

	// For now, simulate VMAF validation
	// In a real implementation, you'd use the actual monitor
	vmafScore := 95.0

	logger.Info("VMAF validation activity completed", "score", vmafScore)
	return VMAFResult{
		InputFile:  input.FilePath,
		OutputFile: input.Key,
		VMAFScore:  vmafScore,
		Error:      nil,
	}, nil
}

// PackageActivity handles video packaging using the existing packager
func PackageActivity(ctx context.Context, input ActivityInput) (PackageResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting packaging activity", "input", input.FilePath, "epoch_time", input.EpochTime)

	// Create package output directory
	packageDir := filepath.Join(filepath.Dir(input.FilePath), "package")
	if err := os.MkdirAll(packageDir, 0755); err != nil {
		return PackageResult{}, fmt.Errorf("failed to create package directory: %w", err)
	}

	outputPath := filepath.Join(packageDir, "playlist.m3u8")

	logger.Info("Packaging activity completed", "output", outputPath)

	return PackageResult{
		InputFile:  input.FilePath,
		OutputPath: outputPath,
		Error:      nil,
	}, nil
}

// StoreActivity handles storing the packaged results using the existing storage
func StoreActivity(ctx context.Context, input ActivityInput) (StorageResult, error) {
	logger := activity.GetLogger(ctx)

	logger.Info("Starting storage activity", "key", input.Key, "file_path", input.FilePath)

	// For now, simulate storage
	// In a real implementation, you'd use the actual storage

	logger.Info("Storage activity completed", "key", input.Key)
	return StorageResult{
		Key:   input.Key,
		Error: nil,
	}, nil
}
