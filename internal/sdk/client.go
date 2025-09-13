package sdk

import (
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	types "StreamForge/pkg"
	"context"
	"go.uber.org/zap"
	"io"
)

type Client struct {
	storage    storage.Storage
	transcoder *pipeline.Transcoder
	packager   *pipeline.Packager
	logger     *zap.Logger
	workflow   *pipeline.Workflow
}

func NewClient(storage storage.Storage, transcoder *pipeline.Transcoder, packager *pipeline.Packager, logger *zap.Logger, workflow *pipeline.Workflow) *Client {
	return &Client{
		storage:    storage,
		transcoder: transcoder,
		packager:   packager,
		logger:     logger,
		workflow:   workflow,
	}
}

func (c *Client) UploadVideo(ctx context.Context, file io.Reader, bucket, key string) (*pipeline.IngestResult, error) {
	p := pipeline.NewPipeline(c.storage, c.logger, types.RetryConfig{})
	return p.Ingest(ctx, file, bucket, key)
}

func (c *Client) TranscodeVideo(ctx context.Context, inputFile string, configs []types.CodecConfig) ([]pipeline.TranscodeResult, error) {
	return c.transcoder.Transcode(ctx, inputFile, configs)
}

func (c *Client) PackageVideo(ctx context.Context, inputFiles []string, configs []types.PackageConfig) ([]pipeline.PackageResult, error) {
	return c.packager.Package(ctx, inputFiles, configs)
}

func (c *Client) RunWorkflow(ctx context.Context, inputFile io.Reader, bucket, key string) (*pipeline.WorkflowResult, error) {
	return c.workflow.Run(ctx, inputFile, bucket, key)
}
