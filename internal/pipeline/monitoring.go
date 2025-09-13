package pipeline

import (
	types "StreamForge/pkg"
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"os"
	"os/exec"
)

type VMAFResult struct {
	InputFile  string
	OutputFile string
	VMAFScore  float64
	Error      error
}

type Monitor struct {
	ffmpegPath string
	logger     *zap.Logger
	vmafConfig types.VMAFConfig
	retry      types.RetryConfig
}

func NewMonitor(ffmpegPath string, logger *zap.Logger, vmafConfig types.VMAFConfig, retryConfig types.RetryConfig) *Monitor {
	return &Monitor{
		ffmpegPath: ffmpegPath,
		logger:     logger,
		vmafConfig: vmafConfig,
		retry:      retryConfig,
	}
}

func (m *Monitor) ValidateVMAF(ctx context.Context, inputFile, outputFile string) (*VMAFResult, error) {
	if !m.vmafConfig.Enabled {
		return &VMAFResult{InputFile: inputFile, OutputFile: outputFile}, nil
	}

	var vmafScore float64
	err := Retry(ctx, m.logger, m.retry, fmt.Sprintf("VMAF validation %s vs %s", inputFile, outputFile), func() error {
		args := []string{
			"-i", outputFile,
			"-i", inputFile,
			"-filter_complex", fmt.Sprintf("[0:v][1:v]libvmaf=model_path=%s:log_path=vmaf.json:log_fmt=json", m.vmafConfig.ModelPath),
			"-f", "null",
			"-",
		}
		cmd := exec.CommandContext(ctx, m.ffmpegPath, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("VMAF command failed: %v, output: %s", err, string(output))
		}

		vmafData, err := parseVMAFOutput("vmaf.json")
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

func parseVMAFOutput(logPath string) (struct {
	PooledMetrics struct {
		VMAF struct {
			Mean float64 `json:"mean"`
		} `json:"vmaf"`
	} `json:"pooled_metrics"`
}, error) {
	var result struct {
		PooledMetrics struct {
			VMAF struct {
				Mean float64 `json:"mean"`
			} `json:"vmaf"`
		} `json:"pooled_metrics"`
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}
