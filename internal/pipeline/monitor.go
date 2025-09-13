package pipeline

import (
	types "StreamForge/pkg"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.uber.org/zap"
)

type VMAFResult struct {
	InputFile  string
	OutputFile string
	VMAFScore  float64
	Error      error
}

type VMAFModel struct {
	PooledMetrics struct {
		VMAF struct {
			Mean float64 `json:"mean"`
		} `json:"vmaf"`
	} `json:"pooled_metrics"`
}
type Monitor struct {
	ffmpegPath string
	logger     *zap.Logger
	vmafConfig types.VMAFConfig
	retry      types.RetryConfig
	modelPath  string
}

func NewMonitor(ffmpegPath string, logger *zap.Logger, vmafConfig types.VMAFConfig, retryConfig types.RetryConfig) *Monitor {
	modelPath := filepath.Join("internal", "models", "vmaf_v0.6.1.json")
	return &Monitor{
		ffmpegPath: ffmpegPath,
		logger:     logger,
		vmafConfig: vmafConfig,
		retry:      retryConfig,
		modelPath:  modelPath,
	}
}

func (m *Monitor) ValidateVMAF(ctx context.Context, inputFile, outputFile string) (*VMAFResult, error) {
	if !m.vmafConfig.Enabled {
		return &VMAFResult{InputFile: inputFile, OutputFile: outputFile}, nil
	}

	if err := m.ensureModel(ctx); err != nil {
		m.logger.Error("Failed to ensure VMAF model", zap.Error(err))
		return &VMAFResult{InputFile: inputFile, OutputFile: outputFile, Error: err}, err
	}

	var vmafScore float64
	err := Retry(ctx, m.logger, m.retry, fmt.Sprintf("VMAF validation %s vs %s", inputFile, outputFile), func() error {
		// Extract resolution from output file path (e.g., outputs/1234567890/720/video.mp4 -> 720)
		resolution := filepath.Base(filepath.Dir(outputFile))

		// Create VMAF log file in the resolution folder
		vmafLogPath := filepath.Join(filepath.Dir(outputFile), "vmaf.json")

		// Convert path to forward slashes for FFmpeg compatibility
		vmafLogPathForFFmpeg := filepath.ToSlash(vmafLogPath)

		m.logger.Info("Running VMAF validation",
			zap.String("input", inputFile),
			zap.String("output", outputFile),
			zap.String("resolution", resolution),
			zap.String("vmaf_log", vmafLogPath),
			zap.String("vmaf_log_ffmpeg", vmafLogPathForFFmpeg))

		args := []string{
			"-i", outputFile,
			"-i", inputFile,
			"-filter_complex", fmt.Sprintf("[1:v]scale=-2:%s[scaled];[0:v][scaled]libvmaf=log_path=%s:log_fmt=json", resolution, vmafLogPathForFFmpeg),
			"-f", "null",
			"-",
		}
		cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("VMAF command failed: %v, output: %s", err, string(output))
		}

		// Check if VMAF log file was created
		if _, err := os.Stat(vmafLogPath); os.IsNotExist(err) {
			return fmt.Errorf("VMAF log file was not created: %s, FFmpeg output: %s", vmafLogPath, string(output))
		}

		vmafData, err := parseVMAFOutput(vmafLogPath)
		if err != nil {
			return fmt.Errorf("failed to parse VMAF output: %w", err)
		}
		vmafScore = vmafData.PooledMetrics.VMAF.Mean
		if vmafScore < m.vmafConfig.MinScore {
			return fmt.Errorf("VMAF score %f below threshold %f", vmafScore, m.vmafConfig.MinScore)
		}
		return nil
	})

	if err != nil {
		m.logger.Error("VMAF validation failed after retries",
			zap.String("input", inputFile),
			zap.String("output", outputFile),
			zap.Error(err))
		return &VMAFResult{InputFile: inputFile, OutputFile: outputFile, Error: err}, err
	}

	m.logger.Info("VMAF validation completed",
		zap.String("input", inputFile),
		zap.String("output", outputFile),
		zap.Float64("vmaf_score", vmafScore))
	return &VMAFResult{InputFile: inputFile, OutputFile: outputFile, VMAFScore: vmafScore}, nil
}

func (m *Monitor) ensureModel(ctx context.Context) error {
	// Using default VMAF model, no need to download
	m.logger.Info("Using default VMAF model")
	return nil
}

func parseVMAFOutput(logPath string) (VMAFModel, error) {
	var result VMAFModel
	data, err := os.ReadFile(logPath)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}
