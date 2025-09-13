package ffmpeg

import (
	types "StreamForge/pkg"
	"context"
	"fmt"
	"os/exec"
)

type FFmpeg struct {
	pathToBinary string
}

func NewFFmpeg(pathToBinary string) *FFmpeg {
	return &FFmpeg{pathToBinary: pathToBinary}
}

func (f *FFmpeg) Transcode(ctx context.Context, inputFile string, config types.CodecConfig) (string, error) {
	args := []string{
		"-i", inputFile,
		"-c:v", config.Codec,
		"-b:v", config.Bitrate,
		"-vf", fmt.Sprintf("scale=-2:%s:flags=lanczos", config.Resolution),
		"-y",
		config.OutputFile,
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
