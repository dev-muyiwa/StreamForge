package pipeline

import (
	types "StreamForge/pkg"
	"StreamForge/pkg/ffmpeg"
	"context"
	"fmt"
	"go.uber.org/zap"
	"sync"
	"sync/atomic"
)

type Packager struct {
	ffmpeg     *ffmpeg.FFmpeg
	logger     *zap.Logger
	maxWorkers int32
	retry      types.RetryConfig
}

type PackageResult struct {
	InputFile  string
	OutputPath string
	Error      error
}

func NewPackager(ffmpegPath string, logger *zap.Logger, maxWorkers int32, retryConfig types.RetryConfig) *Packager {
	if maxWorkers <= 0 {
		maxWorkers = 10
	}
	return &Packager{
		ffmpeg:     ffmpeg.NewFFmpeg(ffmpegPath),
		logger:     logger,
		maxWorkers: maxWorkers,
		retry:      retryConfig,
	}
}

func (p *Packager) Package(ctx context.Context, inputFiles []string, configs []types.PackageConfig) ([]PackageResult, error) {
	workerSemaphore := make(chan struct{}, p.maxWorkers)
	results := make([]PackageResult, len(inputFiles)*len(configs))
	var resultIdx int32
	var wg sync.WaitGroup
	var hasError uint32

	resultChan := make(chan PackageResult, len(inputFiles)*len(configs))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, inputFile := range inputFiles {
		for _, config := range configs {
			select {
			case <-ctx.Done():
				p.logger.Warn("packaging cancelled", zap.Error(ctx.Err()))
				return results, ctx.Err()
			case workerSemaphore <- struct{}{}:
				wg.Add(1)
				go func(inputFile, output string, config types.PackageConfig) {
					defer wg.Done()
					defer func() { <-workerSemaphore }()

					var outputPath string
					err := Retry(ctx, p.logger, p.retry, fmt.Sprintf("package %s to %s", inputFile, config.Format), func() error {
						args := []string{
							"-i", inputFile,
							"-f", config.Format,
							"-hls_time", fmt.Sprintf("%d", config.SegmentDuration),
							"-hls_list_size", "0",
						}
						if config.Format == "hls" && config.LLHLS {
							args = append(args,
								"-hls_segment_type", "fmp4",
								"-hls_flags", "program_date_time+independent_segments",
								"-hls_playlist_type", "vod",
							)
						}
						if config.Format == "dash" {
							args = append(args, "-dash_segment_duration", fmt.Sprintf("%d", config.SegmentDuration))
						}
						args = append(args, "-y", config.OutputPath)
						var err error
						outputPath, err = p.ffmpeg.Exec(ctx, args)
						return err
					})
					if err != nil {
						p.logger.Error("Packaging failed after retries",
							zap.String("input", inputFile),
							zap.String("format", config.Format),
							zap.Error(err))
						atomic.StoreUint32(&hasError, 1)
						resultChan <- PackageResult{InputFile: inputFile, OutputPath: config.OutputPath, Error: err}
						return
					}

					p.logger.Info("packaging succeeded",
						zap.String("inputFile", inputFile),
						zap.String("outputPath", output))
					resultChan <- PackageResult{
						InputFile:  inputFile,
						OutputPath: outputPath,
						Error:      nil,
					}
				}(inputFile, config.OutputPath, config)
			}
		}
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for res := range resultChan {
		idx := atomic.AddInt32(&resultIdx, 1) - 1
		results[idx] = res
	}

	if atomic.LoadUint32(&hasError) == 1 {
		return results, fmt.Errorf("one or more packaging operations failed")
	}

	return results, nil
}
