package pipeline

import (
	"StreamForge/internal/pipeline/storage"
	types "StreamForge/pkg"
	"context"
	"fmt"
	"go.uber.org/zap"
	"io"
)

type Pipeline struct {
	storage storage.Storage
	logger  *zap.Logger
	retry   types.RetryConfig
}

type IngestResult struct {
	Key   string
	Error error
}

func NewPipeline(storage storage.Storage, logger *zap.Logger, retryCfg types.RetryConfig) *Pipeline {
	return &Pipeline{storage: storage, logger: logger, retry: retryCfg}
}

func (p *Pipeline) Ingest(ctx context.Context, file io.Reader, bucket, key string) (*IngestResult, error) {
	var err error
	err = Retry(ctx, p.logger, p.retry, fmt.Sprintf("ingest %s", key), func() error {
		return p.storage.Upload(ctx, bucket, key, file)
	})
	if err != nil {
		p.logger.Error("Ingest failed after retries", zap.String("key", key), zap.Error(err))
		return &IngestResult{Key: key, Error: err}, err
	}
	p.logger.Info("Ingest completed", zap.String("key", key))
	return &IngestResult{Key: key}, nil
}
