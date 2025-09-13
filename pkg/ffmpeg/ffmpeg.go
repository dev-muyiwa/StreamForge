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

func (f *FFmpeg) Transcode(ctx context.Context, inputFile string, config types.CodecConfig) (string, error) {
	// Create output directory structure: outputs/key/resolution/
	keyWithoutExt := filepath.Base(inputFile)
	keyWithoutExt = keyWithoutExt[:len(keyWithoutExt)-len(filepath.Ext(keyWithoutExt))] // Remove extension

	outputDir := filepath.Join("./outputs", keyWithoutExt, config.Resolution)
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

func (f *FFmpeg) Exec(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, f.pathToBinary, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ffmpeg command failed: %v, output: %s", err, string(output))
	}

	return args[len(args)-1], nil
}
