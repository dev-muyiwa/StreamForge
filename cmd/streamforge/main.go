package main

import (
	"StreamForge/internal/api"
	"StreamForge/internal/config"
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	"log"

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

	var storageImpl storage.Storage
	if cfg.Storage.Type != "local" {
		var err error
		storageImpl, err = storage.NewStorage(cfg.Storage)
		if err != nil {
			logger.Fatal("Failed to init storage", zap.Error(err))
		}
	}

	pipeLine := pipeline.NewPipeline(logger, cfg.Pipeline.Retry)
	transcoder := pipeline.NewTranscoder(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.MaxWorkers, cfg.Pipeline.Retry)
	packager := pipeline.NewPackager(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.MaxWorkers, cfg.Pipeline.Retry)
	monitor := pipeline.NewMonitor(cfg.Pipeline.FFMpegPath, logger, cfg.Pipeline.VMAF, cfg.Pipeline.Retry)
	workflow := pipeline.NewWorkflow(pipeLine, transcoder, packager, monitor, storageImpl, logger, cfg)
	//client := sdk.NewClient(store, transcoder, packager, logger, workflow)
	//
	//outputs, err := client.TranscodeVideo(context.Background(), "input.mp4", cfg.Transcode)
	//if err != nil {
	//	panic(err)
	//}
	//
	//log.Printf("Transcoded files: %v", outputs)

	server := api.NewServer(storageImpl, transcoder, packager, workflow, cfg)
	server.Start()
}
