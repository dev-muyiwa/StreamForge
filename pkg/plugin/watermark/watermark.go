package watermark

import (
	types "StreamForge/pkg"
	"StreamForge/pkg/ffmpeg"
	"StreamForge/pkg/plugin"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitchellh/mapstructure"
	"go.uber.org/zap"
)

// WatermarkPlugin implements the Plugin interface for adding text watermarks
type WatermarkPlugin struct {
	ffmpeg *ffmpeg.FFmpeg
	logger *zap.Logger
	retry  types.RetryConfig
}

// NewWatermarkPlugin creates a new watermark plugin instance
func NewWatermarkPlugin(ffmpegClient *ffmpeg.FFmpeg, logger *zap.Logger, retry types.RetryConfig) *WatermarkPlugin {
	return &WatermarkPlugin{
		ffmpeg: ffmpegClient,
		logger: logger,
		retry:  retry,
	}
}

// Name returns the unique identifier for this plugin
func (p *WatermarkPlugin) Name() string {
	return "watermark"
}

// Validate checks if the plugin configuration is valid
func (p *WatermarkPlugin) Validate(config map[string]interface{}) error {
	var wmConfig WatermarkConfig
	if err := mapstructure.Decode(config, &wmConfig); err != nil {
		return fmt.Errorf("failed to decode watermark config: %w", err)
	}

	wmConfig.SetDefaults()
	return wmConfig.Validate()
}

// Execute applies a watermark to the video file
func (p *WatermarkPlugin) Execute(ctx context.Context, input plugin.PluginInput) (plugin.PluginOutput, error) {
	// Validate input file exists
	if _, err := os.Stat(input.FilePath); err != nil {
		return plugin.PluginOutput{}, fmt.Errorf("input file not found: %w", err)
	}

	// Parse configuration
	var config WatermarkConfig
	if err := mapstructure.Decode(input.Config, &config); err != nil {
		return plugin.PluginOutput{}, fmt.Errorf("failed to decode watermark config: %w", err)
	}

	// Set defaults and validate
	config.SetDefaults()
	if err := config.Validate(); err != nil {
		return plugin.PluginOutput{}, fmt.Errorf("invalid watermark config: %w", err)
	}

	// Create output directory
	outputDir := filepath.Join("./plugins", "watermark", fmt.Sprintf("%d", input.EpochTime))
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return plugin.PluginOutput{}, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create output file path
	outputFile := filepath.Join(outputDir, filepath.Base(input.FilePath))

	// Apply watermark based on type
	var result string
	var err error

	if config.Type == "text" {
		p.logger.Info("Applying text watermark",
			zap.String("text", config.Text),
			zap.String("position", config.Position),
			zap.Int("font_size", config.FontSize),
			zap.String("font_color", config.FontColor),
			zap.Float64("alpha", config.Alpha))

		result, err = p.applyTextWatermark(ctx, input.FilePath, outputFile, config)
	} else if config.Type == "image" {
		p.logger.Info("Applying image watermark",
			zap.String("image_path", config.ImagePath),
			zap.String("position", config.Position),
			zap.Float64("alpha", config.Alpha))

		result, err = p.applyImageWatermark(ctx, input.FilePath, outputFile, config)
	} else {
		return plugin.PluginOutput{}, fmt.Errorf("unsupported watermark type: %s", config.Type)
	}

	if err != nil {
		return plugin.PluginOutput{Error: err}, fmt.Errorf("failed to apply watermark: %w", err)
	}

	p.logger.Info("Watermark applied successfully", zap.String("output_file", result))

	return plugin.PluginOutput{FilePath: result}, nil
}

// buildWatermarkFilter constructs the FFmpeg drawtext filter
func (p *WatermarkPlugin) buildWatermarkFilter(config WatermarkConfig) (string, error) {
	// Escape text for FFmpeg
	text := strings.ReplaceAll(config.Text, "'", "\\'")
	text = strings.ReplaceAll(text, ":", "\\:")

	// Get position expression
	x, y := config.GetPositionExpression()

	// Build color with alpha channel
	color := fmt.Sprintf("%s@%.2f", config.FontColor, config.Alpha)

	// Construct drawtext filter
	filter := fmt.Sprintf(
		"drawtext=text='%s':fontsize=%d:fontcolor=%s:x=%s:y=%s",
		text,
		config.FontSize,
		color,
		x,
		y,
	)

	return filter, nil
}

// applyTextWatermark applies a text watermark using FFmpeg drawtext filter
func (p *WatermarkPlugin) applyTextWatermark(ctx context.Context, inputFile, outputFile string, config WatermarkConfig) (string, error) {
	// Build FFmpeg filter
	filter, err := p.buildWatermarkFilter(config)
	if err != nil {
		return "", fmt.Errorf("failed to build text watermark filter: %w", err)
	}

	p.logger.Info("Applying text watermark filter", zap.String("filter", filter), zap.String("output", outputFile))

	// Apply watermark using FFmpeg with retry logic
	var result string
	err = Retry(ctx, p.logger, p.retry, "apply text watermark", func() error {
		var retryErr error
		result, retryErr = p.ffmpeg.ApplyFilter(ctx, inputFile, outputFile, filter)
		return retryErr
	})

	return result, err
}

// applyImageWatermark applies an image watermark using FFmpeg overlay filter
func (p *WatermarkPlugin) applyImageWatermark(ctx context.Context, inputFile, outputFile string, config WatermarkConfig) (string, error) {
	// Validate image file exists
	if _, err := os.Stat(config.ImagePath); err != nil {
		return "", fmt.Errorf("watermark image not found: %w", err)
	}

	// Build FFmpeg overlay filter
	filter, err := p.buildImageOverlayFilter(config)
	if err != nil {
		return "", fmt.Errorf("failed to build image overlay filter: %w", err)
	}

	p.logger.Info("Applying image overlay filter", zap.String("filter", filter), zap.String("output", outputFile))

	// Apply watermark using FFmpeg with retry logic
	var result string
	err = Retry(ctx, p.logger, p.retry, "apply image watermark", func() error {
		var retryErr error
		result, retryErr = p.ffmpeg.ApplyImageOverlay(ctx, inputFile, config.ImagePath, outputFile, filter)
		return retryErr
	})

	return result, err
}

// buildImageOverlayFilter constructs the FFmpeg overlay filter for image watermark
func (p *WatermarkPlugin) buildImageOverlayFilter(config WatermarkConfig) (string, error) {
	// Get position expression for overlay
	x, y := config.GetImagePositionExpression()

	// Build scale filter if needed
	var scaleFilter string
	if config.ImageWidth > 0 && config.ImageHeight > 0 {
		// Use specific dimensions
		scaleFilter = fmt.Sprintf("scale=%d:%d", config.ImageWidth, config.ImageHeight)
	} else if config.ImageWidth > 0 {
		// Scale with specified width, maintain aspect ratio
		scaleFilter = fmt.Sprintf("scale=%d:-1", config.ImageWidth)
	} else if config.ImageHeight > 0 {
		// Scale with specified height, maintain aspect ratio
		scaleFilter = fmt.Sprintf("scale=-1:%d", config.ImageHeight)
	} else if config.ImageScale > 0 {
		// Scale as percentage of main video
		scaleFilter = fmt.Sprintf("scale=iw*%.2f:ih*%.2f", config.ImageScale, config.ImageScale)
	}

	// Build alpha filter for transparency
	var alphaFilter string
	if config.Alpha < 1.0 {
		alphaFilter = fmt.Sprintf("format=rgba,colorchannelmixer=aa=%.2f", config.Alpha)
	}

	// Combine filters
	var overlayInput string
	if scaleFilter != "" && alphaFilter != "" {
		overlayInput = fmt.Sprintf("[1:v]%s,%s[wm]", scaleFilter, alphaFilter)
	} else if scaleFilter != "" {
		overlayInput = fmt.Sprintf("[1:v]%s[wm]", scaleFilter)
	} else if alphaFilter != "" {
		overlayInput = fmt.Sprintf("[1:v]%s[wm]", alphaFilter)
	} else {
		overlayInput = "[1:v]"
	}

	// Build final overlay filter
	var filter string
	if overlayInput != "[1:v]" {
		filter = fmt.Sprintf("%s;[0:v][wm]overlay=%s:%s", overlayInput, x, y)
	} else {
		filter = fmt.Sprintf("overlay=%s:%s", x, y)
	}

	return filter, nil
}

// Retry executes a function with retry logic
func Retry(ctx context.Context, logger *zap.Logger, config types.RetryConfig, operation string, fn func() error) error {
	var lastErr error
	interval := config.InitialIntervalSec

	for attempt := int32(0); attempt < config.MaxAttempts; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			logger.Warn("Operation failed, retrying",
				zap.String("operation", operation),
				zap.Int32("attempt", attempt+1),
				zap.Int32("max_attempts", config.MaxAttempts),
				zap.Error(err))

			if attempt < config.MaxAttempts-1 {
				// Wait before retry
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-waitFor(interval):
					interval *= config.BackoffCoefficient
				}
			}
		} else {
			return nil
		}
	}

	return fmt.Errorf("operation %s failed after %d attempts: %w", operation, config.MaxAttempts, lastErr)
}

// waitFor creates a timer channel for the given duration
func waitFor(seconds float64) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		// Convert seconds to milliseconds for more precision
		ms := int64(seconds * 1000)
		if ms > 0 {
			// Use a simple sleep approach
			// In production, you might want to use time.After
			<-make(chan struct{})
		}
		close(ch)
	}()
	return ch
}
