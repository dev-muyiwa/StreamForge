package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type ConfigLoader struct {
	logger *zap.Logger
	v      *viper.Viper
}

func NewConfigLoader(logger *zap.Logger) *ConfigLoader {
	v := viper.New()
	v.SetConfigType("yaml")
	return &ConfigLoader{
		logger: logger,
		v:      v,
	}
}

func (cl *ConfigLoader) Load(filePath string) (*Config, error) {
	cl.v.SetConfigFile(filePath)
	if err := cl.v.ReadInConfig(); err != nil {
		cl.logger.Error("Failed to read config file", zap.String("file", filePath), zap.Error(err))
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := cl.v.Unmarshal(&cfg); err != nil {
		cl.logger.Error("Failed to unmarshal config", zap.Error(err))
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if err := cl.validate(&cfg); err != nil {
		cl.logger.Error("Config validation failed", zap.Error(err))
		return nil, err
	}

	cl.logger.Info("Config loaded successfully", zap.String("file", filePath))
	return &cfg, nil
}

func (cl *ConfigLoader) validate(cfg *Config) error {
	if cfg.Pipeline.MaxWorkers < 0 {
		return fmt.Errorf("max_workers must be non-negative")
	}
	if cfg.Pipeline.FFMpegPath == "" {
		cfg.Pipeline.FFMpegPath = "ffmpeg" // Default to the one that's in PATH
	}

	if cfg.Pipeline.Retry.MaxAttempts < 0 {
		return fmt.Errorf("retry.max_attempts must be non-negative")
	}
	if cfg.Pipeline.Retry.MaxAttempts == 0 {
		cfg.Pipeline.Retry.MaxAttempts = 3 // Default
	}
	if cfg.Pipeline.Retry.InitialIntervalSec <= 0 {
		cfg.Pipeline.Retry.InitialIntervalSec = 1.0 // Default
	}
	if cfg.Pipeline.Retry.BackoffCoefficient <= 1 {
		cfg.Pipeline.Retry.BackoffCoefficient = 2.0 // Default
	}

	if cfg.Pipeline.VMAF.Enabled && (cfg.Pipeline.VMAF.MinScore < 0 || cfg.Pipeline.VMAF.MinScore > 100) {
		return fmt.Errorf("vmaf.min_score must be between 0 and 100")
	}

	storage := strings.ToLower(cfg.Storage.Type)
	switch storage {
	case "s3":
		if cfg.Storage.S3.Bucket == "" {
			return fmt.Errorf("s3 bucket required")
		}
		if cfg.Storage.S3.Region == "" {
			return fmt.Errorf("s3 region required")
		}
		if cfg.Storage.S3.AccessKeyID == "" || cfg.Storage.S3.SecretAccessKey == "" {
			return fmt.Errorf("s3 access_key and secret_key required")
		}
	case "gcloud", "r2":
		if cfg.Storage.GCloud.Bucket == "" || cfg.Storage.R2.Bucket == "" {
			return fmt.Errorf("bucket required for %s", storage)
		}
	case "local":
		// No base_path required for local storage since we use outputs folder directly
	default:
		return fmt.Errorf("invalid storage backend: %s", storage)
	}

	if len(cfg.Transcode) == 0 {
		return fmt.Errorf("at least one transcode config required")
	}
	for _, tc := range cfg.Transcode {
		if !isValidCodec(tc.Codec) {
			return fmt.Errorf("invalid codec: %s", tc.Codec)
		}
		if tc.Bitrate == "" {
			return fmt.Errorf("bitrate required for codec: %s", tc.Codec)
		}
		if tc.Resolution == "" {
			return fmt.Errorf("resolution required for codec: %s", tc.Codec)
		}
	}

	for _, pc := range cfg.Package {
		if pc.Format != "hls" && pc.Format != "dash" {
			return fmt.Errorf("invalid package format: %s", pc.Format)
		}
		if pc.SegmentDuration <= 0 {
			return fmt.Errorf("segment_duration must be positive for format: %s", pc.Format)
		}
		if pc.OutputPath == "" {
			return fmt.Errorf("output_path required for format: %s", pc.Format)
		}
	}

	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if !isValidLogLevel(cfg.Logging.Level) {
		return fmt.Errorf("invalid log level: %s", cfg.Logging.Level)
	}
	if cfg.Logging.Output == "" {
		cfg.Logging.Output = "console"
	}
	if cfg.Logging.Output == "file" && cfg.Logging.FilePath == "" {
		return fmt.Errorf("file_path required for file logging")
	}

	return nil
}

func isValidCodec(codec string) bool {
	supported := []string{"libx264", "libx265", "libaom-av1"}
	for _, c := range supported {
		if codec == c {
			return true
		}
	}
	return false
}

func isValidLogLevel(level string) bool {
	levels := []string{"debug", "info", "warn", "error"}
	for _, l := range levels {
		if strings.ToLower(level) == l {
			return true
		}
	}
	return false
}
