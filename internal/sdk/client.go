package sdk

import (
	"StreamForge/internal/pipeline"
	"StreamForge/internal/pipeline/storage"
	types "StreamForge/pkg"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Client struct {
	storage          storage.Storage
	transcoder       *pipeline.Transcoder
	packager         *pipeline.Packager
	logger           *zap.Logger
	temporalWorkflow *pipeline.TemporalWorkflow
}

func NewClient(storage storage.Storage, transcoder *pipeline.Transcoder, packager *pipeline.Packager, logger *zap.Logger, temporalWorkflow *pipeline.TemporalWorkflow) *Client {
	return &Client{
		storage:          storage,
		transcoder:       transcoder,
		packager:         packager,
		logger:           logger,
		temporalWorkflow: temporalWorkflow,
	}
}

func (c *Client) UploadVideo(ctx context.Context, file io.Reader, bucket, key string) (*pipeline.IngestResult, error) {
	p := pipeline.NewPipeline(c.logger, types.RetryConfig{})
	return p.Ingest(ctx, file, bucket, key)
}

func (c *Client) TranscodeVideo(ctx context.Context, inputFile string, configs []types.CodecConfig) ([]pipeline.TranscodeResult, error) {
	return c.transcoder.Transcode(ctx, inputFile, configs, time.Now().Unix())
}

func (c *Client) PackageVideo(ctx context.Context, inputFiles []string, configs []types.PackageConfig) ([]pipeline.PackageResult, error) {
	return c.packager.Package(ctx, inputFiles, configs, time.Now().Unix())
}

func (c *Client) RunWorkflow(ctx context.Context, inputFile io.Reader, bucket, key string) (*pipeline.WorkflowOutput, error) {
	// Read file data into memory
	fileData, err := io.ReadAll(inputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read file data: %w", err)
	}

	// Create workflow input
	workflowInput := pipeline.WorkflowInput{
		FileData:  fileData,
		JobID:     uuid.New(),
		Key:       key,
		Bucket:    bucket,
		EpochTime: time.Now().Unix(),
	}

	// Execute Temporal workflow
	return c.temporalWorkflow.ExecuteWorkflow(ctx, workflowInput)
}
