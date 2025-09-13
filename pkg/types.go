package types

type CodecConfig struct {
	Codec      string `mapstructure:"codec" json:"codec"`
	Bitrate    string `mapstructure:"bitrate" json:"bitrate"`
	Resolution string `mapstructure:"resolution" json:"resolution"`
}

type PackageConfig struct {
	Format          string `mapstructure:"format" json:"format"`
	SegmentDuration int    `mapstructure:"segment_duration" json:"segment_duration"`
	LLHLS           bool   `mapstructure:"llhls" json:"llhls"`
	OutputPath      string `mapstructure:"output_path" json:"output_path"`
}

type VMAFConfig struct {
	Enabled  bool    `mapstructure:"enabled" json:"enabled"`
	MinScore float64 `mapstructure:"min_score" json:"min_score"`
}

type PipelineConfig struct {
	MaxWorkers int32       `mapstructure:"max_workers" json:"max_workers"`
	FFMpegPath string      `mapstructure:"ff_mpeg_path" json:"ff_mpeg_path"`
	Retry      RetryConfig `mapstructure:"retry" json:"retry"`
	VMAF       VMAFConfig  `mapstructure:"vmaf" json:"vmaf"`
}

type RetryConfig struct {
	MaxAttempts        int32   `mapstructure:"max_attempts" json:"max_attempts"`
	InitialIntervalSec float64 `mapstructure:"initial_interval_sec" json:"initial_interval_sec"`
	BackoffCoefficient float64 `mapstructure:"backoff_coefficient" json:"backoff_coefficient"`
}

type StorageConfig struct {
	Type   string       `mapstructure:"type" json:"type"`
	Local  LocalConfig  `mapstructure:"local" json:"local"`
	S3     S3Config     `mapstructure:"s3" json:"s3"`
	GCloud GCloudConfig `mapstructure:"gcloud" json:"gcloud"`
	R2     R2Config     `mapstructure:"r2" json:"r2"`
}

type LocalConfig struct {
	BasePath string `mapstructure:"base_path" json:"base_path"`
}

type S3Config struct {
	Bucket          string `mapstructure:"bucket" json:"bucket"`
	Region          string `mapstructure:"region" json:"region"`
	AccessKeyID     string `mapstructure:"access_key_id" json:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key" json:"secret_access_key"`
}

type GCloudConfig struct {
	Bucket    string `mapstructure:"bucket" json:"bucket"`
	ProjectID string `mapstructure:"project_id" json:"project_id"`
}

type R2Config struct {
	Bucket    string `mapstructure:"bucket" json:"bucket"`
	AccountID string `mapstructure:"account_id" json:"account_id"`
}

type PluginConfig struct {
	Name    string                 `mapstructure:"name" json:"name"`
	Enabled bool                   `mapstructure:"enabled" json:"enabled"`
	Config  map[string]interface{} `mapstructure:"config" json:"config"`
}

type LoggingConfig struct {
	Level    string `mapstructure:"level" json:"level"`
	Output   string `mapstructure:"output" json:"output"`
	FilePath string `mapstructure:"file_path" json:"file_path"`
}
