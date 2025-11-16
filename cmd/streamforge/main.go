package main

import (
	"StreamForge/internal/api"
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.temporal.io/sdk/client"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			log.Printf("error syncing logger: %v", err)
		}
	}(logger)

	configLoader := config.NewConfigLoader(logger)
	cfg, err := configLoader.Load("config.yaml")
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}

	// Create Temporal client
	temporalClient, err := client.Dial(client.Options{
		HostPort: "localhost:7233", // Default Temporal server address
	})
	if err != nil {
		logger.Fatal("Failed to create Temporal client", zap.Error(err))
	}
	defer temporalClient.Close()

	var storageImpl storage.Storage
	if cfg.Storage.Type != "local" {
		var err error
		storageImpl, err = storage.NewStorage(cfg.Storage)
		if err != nil {
			logger.Fatal("Failed to init storage", zap.Error(err))
		}
	}

	// Initialize pipeline components
	transcoder := pipeline.NewTranscoder(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.MaxWorkers, cfg.Pipeline.Retry)
	packager := pipeline.NewPackager(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.MaxWorkers, cfg.Pipeline.Retry)
	monitor := pipeline.NewMonitor(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.VMAF, cfg.Pipeline.Retry)

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

	server := api.NewServer(temporalWorkflow, cfg)

	// Start server in a goroutine
	go func() {
		logger.Info("Starting API server on :8081")
		server.Start()
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")
}
