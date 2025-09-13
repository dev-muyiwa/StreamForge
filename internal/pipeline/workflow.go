package pipeline

import (
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline/storage"
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
	"os"
	"sync"
	"time"
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
	startTime := time.Now()

	// Step 1: Ingest
	w.logger.Info("Starting ingest", zap.String("bucket", bucket), zap.String("key", key))
	ingestResult, err := w.pipeline.Ingest(ctx, inputFile, bucket, key)
	if err != nil {
		w.logger.Error("Ingest failed", zap.Error(err), zap.Duration("duration", time.Since(startTime)))
		result.IngestResult = ingestResult
		return result, fmt.Errorf("ingest failed: %w", err)
	}
	result.IngestResult = ingestResult
	w.logger.Info("Ingest completed", zap.String("bucket", bucket), zap.String("key", key), zap.Duration("duration", time.Since(startTime)))

	// Step 2: Transcode
	transcodeStart := time.Now()
	w.logger.Info("Starting transcode", zap.String("bucket", bucket), zap.String("key", key))
	transcodeResults, err := w.transcoder.Transcode(ctx, fmt.Sprintf("%s/%s", bucket, key), w.config.Transcode)
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

	// Step 5: Store outputs
	storeStart := time.Now()
	w.logger.Info("Starting storage upload")
	var wg sync.WaitGroup
	storageResults := make([]StorageResult, 0, len(packageResults))
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
	result.StorageResults = storageResults

	if hasError {
		return result, fmt.Errorf("some storage uploads failed")
	}
	return result, nil
}
