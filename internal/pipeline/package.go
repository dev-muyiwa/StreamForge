package pipeline

import (
	types "StreamForge/pkg"
	"StreamForge/pkg/ffmpeg"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
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

func (p *Packager) Package(ctx context.Context, inputFiles []string, configs []types.PackageConfig, epochTime int64) ([]PackageResult, error) {
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
						// Create output directory structure: outputs/epoch_time/resolution/package/
						// Extract resolution from the input file path (e.g., from "outputs/1234567890/720/video.mp4")
						resolution := filepath.Base(filepath.Dir(inputFile))

						outputDir := filepath.Join("./outputs", fmt.Sprintf("%d", epochTime), resolution, "package")
						if err := os.MkdirAll(outputDir, 0755); err != nil {
							return fmt.Errorf("failed to create package output directory: %w", err)
						}

						// Create full output path
						fullOutputPath := filepath.Join(outputDir, config.OutputPath)

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
						args = append(args, "-y", fullOutputPath)
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

	// Create master HLS playlist if we have HLS format
	hasHLS := false
	for _, config := range configs {
		if config.Format == "hls" {
			hasHLS = true
			break
		}
	}

	if hasHLS {
		masterPlaylistPath, err := p.createMasterPlaylist(ctx, results, epochTime)
		if err != nil {
			p.logger.Error("Failed to create master playlist",
				zap.Error(err))
			// Don't fail the entire operation if master playlist creation fails
		} else {
			p.logger.Info("Master playlist created",
				zap.String("path", masterPlaylistPath))
			// Add master playlist to results
			results = append(results, PackageResult{
				InputFile:  "master",
				OutputPath: masterPlaylistPath,
				Error:      nil,
			})
		}
	}

	return results, nil
}

// ResolutionVariant represents a single resolution variant for the master playlist
type ResolutionVariant struct {
	Resolution   string
	Bandwidth    int
	Width        int
	Height       int
	PlaylistPath string
}

// createMasterPlaylist creates an HLS master playlist that references all resolution playlists
func (p *Packager) createMasterPlaylist(ctx context.Context, packageResults []PackageResult, epochTime int64) (string, error) {
	// Collect all resolution variants
	variants := make(map[string]*ResolutionVariant)

	for _, result := range packageResults {
		if result.Error != nil {
			continue
		}

		// Check if this is an HLS playlist
		if !strings.HasSuffix(result.OutputPath, ".m3u8") {
			continue
		}

		// Extract resolution from path (e.g., "outputs/1234567890/720/package/playlist.m3u8")
		resolution := extractResolutionFromPackagePath(result.OutputPath)
		if resolution == "" {
			p.logger.Warn("Could not extract resolution from path",
				zap.String("path", result.OutputPath))
			continue
		}

		// Parse resolution to get width and height
		height, err := strconv.Atoi(resolution)
		if err != nil {
			p.logger.Warn("Could not parse resolution",
				zap.String("resolution", resolution),
				zap.Error(err))
			continue
		}

		// Calculate width based on 16:9 aspect ratio
		width := (height * 16) / 9

		// Estimate bandwidth based on resolution (this is a rough estimate)
		bandwidth := estimateBandwidth(height)

		// Create relative path from master playlist to resolution playlist
		// Master is at: outputs/epochTime/master.m3u8
		// Resolution playlist is at: outputs/epochTime/720/package/playlist.m3u8
		// Relative path: 720/package/playlist.m3u8
		relativePath := fmt.Sprintf("%s/package/playlist.m3u8", resolution)

		variants[resolution] = &ResolutionVariant{
			Resolution:   resolution,
			Bandwidth:    bandwidth,
			Width:        width,
			Height:       height,
			PlaylistPath: relativePath,
		}
	}

	if len(variants) == 0 {
		return "", fmt.Errorf("no valid HLS playlists found to create master playlist")
	}

	// Sort variants by resolution (descending - highest first)
	sortedVariants := make([]*ResolutionVariant, 0, len(variants))
	for _, variant := range variants {
		sortedVariants = append(sortedVariants, variant)
	}
	sort.Slice(sortedVariants, func(i, j int) bool {
		return sortedVariants[i].Height > sortedVariants[j].Height
	})

	// Create master playlist content
	var masterContent strings.Builder
	masterContent.WriteString("#EXTM3U\n")
	masterContent.WriteString("#EXT-X-VERSION:3\n")

	for _, variant := range sortedVariants {
		masterContent.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d\n",
			variant.Bandwidth, variant.Width, variant.Height))
		masterContent.WriteString(variant.PlaylistPath + "\n")
	}

	// Write master playlist to file
	outputDir := filepath.Join("./outputs", fmt.Sprintf("%d", epochTime))
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create master playlist directory: %w", err)
	}

	masterPlaylistPath := filepath.Join(outputDir, "master.m3u8")
	if err := os.WriteFile(masterPlaylistPath, []byte(masterContent.String()), 0644); err != nil {
		return "", fmt.Errorf("failed to write master playlist: %w", err)
	}

	p.logger.Info("Master playlist created successfully",
		zap.String("path", masterPlaylistPath),
		zap.Int("variants", len(sortedVariants)))

	return masterPlaylistPath, nil
}

// extractResolutionFromPackagePath extracts resolution from package output path
func extractResolutionFromPackagePath(path string) string {
	// Path format: outputs/1234567890/720/package/playlist.m3u8
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		// Look for numeric resolution (e.g., "720", "1080")
		if _, err := strconv.Atoi(part); err == nil {
			// Check if next part is "package" to confirm this is the resolution
			if i+1 < len(parts) && parts[i+1] == "package" {
				return part
			}
		}
	}
	return ""
}

// estimateBandwidth estimates bandwidth based on resolution height
func estimateBandwidth(height int) int {
	// Rough estimates based on typical streaming bitrates
	switch {
	case height >= 2160: // 4K
		return 20000000 // 20 Mbps
	case height >= 1440: // 2K
		return 10000000 // 10 Mbps
	case height >= 1080: // 1080p
		return 5000000 // 5 Mbps
	case height >= 720: // 720p
		return 2500000 // 2.5 Mbps
	case height >= 480: // 480p
		return 1000000 // 1 Mbps
	case height >= 360: // 360p
		return 600000 // 600 Kbps
	default:
		return 400000 // 400 Kbps
	}
}
