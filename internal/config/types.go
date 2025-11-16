package config

import (
	types "StreamForge/pkg"
)

type Config struct {
	Database  DatabaseConfig        `mapstructure:"database" json:"database"`
	Pipeline  types.PipelineConfig  `mapstructure:"pipeline" json:"pipeline"`
	Storage   types.StorageConfig   `mapstructure:"storage" json:"storage"`
	Transcode []types.CodecConfig   `mapstructure:"transcode" json:"transcode"`
	Package   []types.PackageConfig `mapstructure:"package" json:"package"`
	Plugins   []types.PluginConfig  `mapstructure:"plugins" json:"plugins"`
	Logging   types.LoggingConfig   `mapstructure:"logging" json:"logging"`
}

type DatabaseConfig struct {
	DSN string `mapstructure:"dsn" json:"dsn"`
}
