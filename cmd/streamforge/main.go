package main

import (
	"StreamForge/internal/api"
	"StreamForge/internal/config"
	"StreamForge/internal/job"
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

	// Initialize database connection
	ctx := context.Background()
	dbpool, err := pgxpool.New(ctx, cfg.Database.DSN)
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	}
	defer dbpool.Close()

	// Verify database connection
	err = dbpool.Ping(ctx)
	if err != nil {
		logger.Fatal("Failed to ping database", zap.Error(err))
	}
	logger.Info("Database connection established")

	// Run migrations
	err = runMigrations(ctx, dbpool, logger)
	if err != nil {
		logger.Fatal("Failed to run migrations", zap.Error(err))
	}

	// Initialize job store and manager
	jobStore := job.NewStore(dbpool)
	jobManager := job.NewManager(jobStore, logger)

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

	// Create Temporal workflow with job manager
	temporalWorkflow := pipeline.NewTemporalWorkflow(
		temporalClient,
		logger,
		cfg,
		transcoder,
		packager,
		monitor,
		storageImpl,
		jobManager,
	)

	// Start Temporal worker
	err = temporalWorkflow.StartWorker()
	if err != nil {
		logger.Fatal("Failed to start Temporal worker", zap.Error(err))
	}
	defer temporalWorkflow.StopWorker()

	logger.Info("Temporal worker started successfully")

	// Create server with job manager
	server := api.NewServer(temporalWorkflow, jobManager, cfg, logger)

	// Start cleanup job in background
	go runCleanupJob(ctx, jobManager, logger, 7*24*time.Hour, 24*time.Hour)

	// Start server in a goroutine
	go func() {
		logger.Info("Starting API server", zap.String("addr", ":8081"))
		if err := server.Start(":8081"); err != nil {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown server
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Server shutdown error", zap.Error(err))
	}

	logger.Info("Shutdown complete")
}

// runMigrations executes database migrations
func runMigrations(ctx context.Context, db *pgxpool.Pool, logger *zap.Logger) error {
	logger.Info("Running database migrations")

	// Read and execute migration files
	migrations := []string{
		"migrations/001_create_jobs.sql",
		"migrations/002_create_progress_events.sql",
		"migrations/003_add_job_metadata.sql",
	}

	for _, migrationFile := range migrations {
		logger.Info("Executing migration", zap.String("file", migrationFile))

		migrationSQL, err := os.ReadFile(migrationFile)
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", migrationFile, err)
		}

		_, err = db.Exec(ctx, string(migrationSQL))
		if err != nil {
			return fmt.Errorf("failed to execute migration %s: %w", migrationFile, err)
		}

		logger.Info("Migration executed successfully", zap.String("file", migrationFile))
	}

	logger.Info("All migrations completed successfully")
	return nil
}

// runCleanupJob periodically cleans up old jobs
func runCleanupJob(ctx context.Context, jobManager *job.Manager, logger *zap.Logger, retentionPeriod, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("Cleanup job started",
		zap.Duration("retention_period", retentionPeriod),
		zap.Duration("interval", interval),
	)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Cleanup job stopped")
			return
		case <-ticker.C:
			logger.Info("Running cleanup job")
			err := jobManager.CleanupOldJobs(context.Background(), retentionPeriod)
			if err != nil {
				logger.Error("Cleanup job failed", zap.Error(err))
			}
		}
	}
}
