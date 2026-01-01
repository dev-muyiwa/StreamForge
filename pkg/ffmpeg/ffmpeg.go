package ffmpeg

import (
	types "StreamForge/pkg"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type FFmpeg struct {
	pathToBinary string
}

func NewFFmpeg(pathToBinary string) *FFmpeg {
	return &FFmpeg{pathToBinary: pathToBinary}
}

func (f *FFmpeg) Transcode(ctx context.Context, inputFile string, config types.CodecConfig, epochTime int64) (string, error) {
	// Create output directory structure: outputs/epoch_time/resolution/
	outputDir := filepath.Join("./outputs", fmt.Sprintf("%d", epochTime), config.Resolution)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	// Use original filename for output
	outputFile := filepath.Join(outputDir, filepath.Base(inputFile))

	args := []string{
		"-i", inputFile,
		"-c:v", config.Codec,
		"-b:v", config.Bitrate,
		"-vf", fmt.Sprintf("scale=-2:%s:flags=lanczos", config.Resolution),
		"-y",
		outputFile,
	}
	return f.Exec(ctx, args)
}

// ApplyFilter applies a video filter to the input file
func (f *FFmpeg) ApplyFilter(ctx context.Context, inputFile, outputFile, filter string) (string, error) {
	// Ensure output directory exists
	outputDir := filepath.Dir(outputFile)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	args := []string{
		"-i", inputFile,
		"-vf", filter,
		"-c:a", "copy", // Don't re-encode audio
		"-y",
		outputFile,
	}

	return f.Exec(ctx, args)
}

func (f *FFmpeg) Exec(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, f.pathToBinary, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ffmpeg command failed: %v, output: %s", err, string(output))
	}

	return args[len(args)-1], nil
}
