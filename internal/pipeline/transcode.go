package pipeline

import (
	types "StreamForge/pkg"
	"StreamForge/pkg/ffmpeg"
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
)

type TranscodeResult struct {
	InputFile  string
	OutputPath string
	Error      error
}

type Transcoder struct {
	ffmpeg     *ffmpeg.FFmpeg
	logger     *zap.Logger
	maxWorkers int32
	retry      types.RetryConfig
}

func NewTranscoder(ffmpegPath string, logger *zap.Logger, maxWorkers int32, retryConfig types.RetryConfig) *Transcoder {
	return &Transcoder{
		ffmpeg:     ffmpeg.NewFFmpeg(ffmpegPath),
		logger:     logger,
		maxWorkers: maxWorkers,
		retry:      retryConfig,
	}
}

func (t *Transcoder) Transcode(ctx context.Context, inputFile string, configs []types.CodecConfig) ([]TranscodeResult, error) {
	// Create outputs directory structure
	outputsDir := "./outputs"
	if err := os.MkdirAll(outputsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create outputs directory: %w", err)
	}

	workerSemaphore := make(chan struct{}, t.maxWorkers)
	results := make([]TranscodeResult, len(configs))
	var resultIdx int32
	var wg sync.WaitGroup
	var hasError uint32

	resultChan := make(chan TranscodeResult, len(configs))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, config := range configs {
		select {
		case <-ctx.Done():
			t.logger.Warn("transcoding cancelled", zap.Error(ctx.Err()))
			return results, ctx.Err()
		case workerSemaphore <- struct{}{}:
			wg.Add(1)
			go func(config types.CodecConfig) {
				defer wg.Done()
				defer func() { <-workerSemaphore }()

				var output string
				err := Retry(ctx, t.logger, t.retry, fmt.Sprintf("transcode %s to %s", inputFile, config.Codec), func() error {
					var err error
					output, err = t.ffmpeg.Transcode(ctx, inputFile, config)
					return err
				})
				if err != nil {
					t.logger.Error("transcoding failed",
						zap.String("inputFile", inputFile),
						zap.String("codec", config.Codec),
						zap.Error(err))
					atomic.StoreUint32(&hasError, 1)
					resultChan <- TranscodeResult{InputFile: inputFile, OutputPath: "", Error: err}
					return
				}

				t.logger.Info("transcoding succeeded",
					zap.String("inputFile", inputFile),
					zap.String("output", output))
				resultChan <- TranscodeResult{InputFile: inputFile, OutputPath: output}
			}(config)
		}
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for res := range resultChan {
		resIdx := atomic.AddInt32(&resultIdx, 1) - 1
		results[resIdx] = res
	}

	if atomic.LoadUint32(&hasError) == 1 {
		return results, context.Canceled
	}

	return results, nil
}
