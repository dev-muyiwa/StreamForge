package main

import (
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.uber.org/zap"
)

func main() {
	// Initialize logger first
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	// Load configuration
	configLoader := config.NewConfigLoader(logger)
	cfg, err := configLoader.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create Temporal client
	temporalClient, err := client.Dial(client.Options{
		HostPort: "localhost:7233", // Default Temporal server address
	})
	if err != nil {
		logger.Fatal("Failed to create Temporal client", zap.Error(err))
	}
	defer temporalClient.Close()

	// Initialize pipeline components
	transcoder := pipeline.NewTranscoder(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.MaxWorkers, cfg.Pipeline.Retry)
	packager := pipeline.NewPackager(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.MaxWorkers, cfg.Pipeline.Retry)
	monitor := pipeline.NewMonitor(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.VMAF, cfg.Pipeline.Retry)

	// Initialize storage (if not local)
	var storageImpl storage.Storage
	if cfg.Storage.Type != "local" {
		storageImpl, err = storage.NewStorage(cfg.Storage)
		if err != nil {
			logger.Fatal("Failed to create storage", zap.Error(err))
		}
	}

	// Create Temporal workflow
	temporalWorkflow := pipeline.NewTemporalWorkflow(
		temporalClient,
		logger,
		cfg,
		transcoder,
		packager,
		monitor,
		storageImpl,
	)

	// Start Temporal worker
	err = temporalWorkflow.StartWorker()
	if err != nil {
		logger.Fatal("Failed to start Temporal worker", zap.Error(err))
	}
	defer temporalWorkflow.StopWorker()

	logger.Info("Temporal worker started successfully")

	// Example: Execute a workflow
	ctx := context.Background()
	workflowInput := pipeline.WorkflowInput{
		Key:       "video.mp4",
		Bucket:    "input",
		EpochTime: time.Now().Unix(),
	}

	logger.Info("Starting video processing workflow",
		zap.String("key", workflowInput.Key),
		zap.Int64("epoch_time", workflowInput.EpochTime))

	result, err := temporalWorkflow.ExecuteWorkflow(ctx, workflowInput)
	if err != nil {
		logger.Error("Workflow execution failed", zap.Error(err))
		return
	}

	logger.Info("Workflow completed successfully",
		zap.Duration("duration", result.Duration),
		zap.Int("transcode_results", len(result.TranscodeResults)),
		zap.Int("vmaf_results", len(result.VMAFResults)),
		zap.Int("package_results", len(result.PackageResults)),
		zap.Int("storage_results", len(result.StorageResults)))

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")
}
