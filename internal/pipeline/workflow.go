package pipeline

import (
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline/storage"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Workflow struct {
	pipeline   *Pipeline
	transcoder *Transcoder
	packager   *Packager
	monitor    *Monitor
	storage    storage.Storage
	logger     *zap.Logger
	config     *config.Config
}

type WorkflowResult struct {
	IngestResult     *IngestResult
	TranscodeResults []TranscodeResult
	VMAFResults      []VMAFResult
	PackageResults   []PackageResult
	StorageResults   []StorageResult
}

type StorageResult struct {
	Key   string
	Error error
}

func NewWorkflow(pipeline *Pipeline, transcoder *Transcoder, packager *Packager, monitor *Monitor, storage storage.Storage, logger *zap.Logger, cfg *config.Config) *Workflow {
	return &Workflow{
		pipeline:   pipeline,
		transcoder: transcoder,
		packager:   packager,
		monitor:    monitor,
		storage:    storage,
		logger:     logger,
		config:     cfg,
	}
}

func (w *Workflow) Run(ctx context.Context, inputFile io.Reader, bucket, key string) (*WorkflowResult, error) {
	result := &WorkflowResult{}

	// Step 1: Save uploaded file locally
	w.logger.Info("Saving uploaded file locally", zap.String("key", key))
	localFilePath, err := w.saveFileLocally(ctx, inputFile, key)
	if err != nil {
		w.logger.Error("Failed to save file locally", zap.Error(err))
		return result, fmt.Errorf("failed to save file locally: %w", err)
	}
	w.logger.Info("File saved locally", zap.String("local_path", localFilePath))

	// Step 2: Transcode - Use local file path
	transcodeStart := time.Now()
	w.logger.Info("Starting transcode", zap.String("local_path", localFilePath))
	transcodeResults, err := w.transcoder.Transcode(ctx, localFilePath, w.config.Transcode)
	if err != nil {
		w.logger.Error("Some transcoding jobs failed", zap.Error(err), zap.Duration("duration", time.Since(transcodeStart)))
	}
	result.TranscodeResults = transcodeResults

	// Step 3: Validate VMAF
	vmafStart := time.Now()
	vmafResults := make([]VMAFResult, len(transcodeResults))
	var vmafHasError bool
	if w.config.Pipeline.VMAF.Enabled {
		var wg sync.WaitGroup
		vmafChan := make(chan VMAFResult, len(transcodeResults))
		for i, tr := range transcodeResults {
			if tr.Error != nil {
				vmafResults[i] = VMAFResult{InputFile: key, OutputFile: tr.OutputPath, Error: tr.Error}
				continue
			}
			wg.Add(1)
			go func(i int, outputPath string) {
				defer wg.Done()
				vmafResult, err := w.monitor.ValidateVMAF(ctx, key, outputPath)
				if err != nil {
					vmafHasError = true
				}
				vmafChan <- *vmafResult
			}(i, tr.OutputPath)
		}
		go func() {
			wg.Wait()
			close(vmafChan)
		}()
		for vmafResult := range vmafChan {
			vmafResults = append(vmafResults, vmafResult)
		}
	}
	result.VMAFResults = vmafResults
	w.logger.Info("VMAF validation completed", zap.Duration("duration", time.Since(vmafStart)))

	// Collect transcoded output files
	var transcodeOutputs []string
	for _, tr := range transcodeResults {
		if tr.Error == nil && !vmafHasError {
			transcodeOutputs = append(transcodeOutputs, tr.OutputPath)
		}
	}
	if len(transcodeOutputs) == 0 {
		w.logger.Error("No successful transcoding outputs or VMAF validation failed", zap.Duration("duration", time.Since(transcodeStart)))
		return result, fmt.Errorf("no successful transcoding outputs")
	}

	// Step 4: Package
	packageStart := time.Now()
	w.logger.Info("Starting packaging", zap.Int("input_count", len(transcodeOutputs)))
	packageResults, err := w.packager.Package(ctx, transcodeOutputs, w.config.Package)
	if err != nil {
		w.logger.Error("Some packaging jobs failed", zap.Error(err), zap.Duration("duration", time.Since(packageStart)))
	}
	result.PackageResults = packageResults

	// Step 5: Store outputs (skip if local storage)
	storageResults := make([]StorageResult, 0, len(packageResults))

	if w.config.Storage.Type == "local" || w.storage == nil {
		w.logger.Info("Skipping storage upload - using local storage")
		// For local storage, just record the local paths as successful
		for _, pr := range packageResults {
			if pr.Error != nil {
				storageResults = append(storageResults, StorageResult{Key: pr.OutputPath, Error: pr.Error})
			} else {
				storageResults = append(storageResults, StorageResult{Key: pr.OutputPath})
			}
		}
	} else {
		// Upload to cloud storage
		storeStart := time.Now()
		w.logger.Info("Starting storage upload")
		var wg sync.WaitGroup
		resultChan := make(chan StorageResult, len(packageResults))
		hasError := false

		for _, pr := range packageResults {
			if pr.Error != nil {
				storageResults = append(storageResults, StorageResult{Key: pr.OutputPath, Error: pr.Error})
				continue
			}
			wg.Add(1)
			go func(outputPath string) {
				defer wg.Done()
				var storageKey string
				err := Retry(ctx, w.logger, w.config.Pipeline.Retry, fmt.Sprintf("store %s", outputPath), func() error {
					file, err := os.Open(outputPath)
					if err != nil {
						return err
					}
					defer file.Close()
					storageKey = fmt.Sprintf("%s/%s", key, outputPath)
					return w.storage.Upload(ctx, bucket, storageKey, file)
				})
				if err != nil {
					w.logger.Error("Storage upload failed after retries", zap.String("key", storageKey), zap.Error(err), zap.Duration("duration", time.Since(storeStart)))
					resultChan <- StorageResult{Key: storageKey, Error: err}
					hasError = true
					return
				}
				w.logger.Info("Storage upload completed", zap.String("key", storageKey), zap.Duration("duration", time.Since(storeStart)))
				resultChan <- StorageResult{Key: storageKey}
			}(pr.OutputPath)
		}

		go func() {
			wg.Wait()
			close(resultChan)
		}()

		for res := range resultChan {
			storageResults = append(storageResults, res)
		}

		if hasError {
			return result, fmt.Errorf("some storage uploads failed")
		}
	}

	result.StorageResults = storageResults
	return result, nil
}

// saveFileLocally saves the uploaded file to local storage
func (w *Workflow) saveFileLocally(ctx context.Context, file io.Reader, key string) (string, error) {
	// Create local input directory if it doesn't exist
	inputDir := "./input"
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create input directory: %w", err)
	}

	// Create local file path
	localFilePath := filepath.Join(inputDir, key)

	err := Retry(ctx, w.logger, w.config.Pipeline.Retry, fmt.Sprintf("save file %s", key), func() error {
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
