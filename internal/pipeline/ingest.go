package pipeline

import (
	types "StreamForge/pkg"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
)

type Pipeline struct {
	logger *zap.Logger
	retry  types.RetryConfig
}

type IngestResult struct {
	Key   string
	Error error
}

func NewPipeline(logger *zap.Logger, retryCfg types.RetryConfig) *Pipeline {
	return &Pipeline{logger: logger, retry: retryCfg}
}

func (p *Pipeline) Ingest(ctx context.Context, file io.Reader, bucket, key string) (*IngestResult, error) {
	// Create local input directory if it doesn't exist
	inputDir := "./input"
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		p.logger.Error("Failed to create input directory", zap.Error(err))
		return &IngestResult{Key: key, Error: err}, err
	}

	// Create local file path
	localFilePath := filepath.Join(inputDir, key)

	var err error
	err = Retry(ctx, p.logger, p.retry, fmt.Sprintf("ingest %s", key), func() error {
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
		p.logger.Error("Ingest failed after retries", zap.String("key", key), zap.Error(err))
		return &IngestResult{Key: key, Error: err}, err
	}

	p.logger.Info("Ingest completed", zap.String("key", key), zap.String("local_path", localFilePath))
	return &IngestResult{Key: localFilePath}, nil
}
