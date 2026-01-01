package plugin

import (
	"context"

	"github.com/google/uuid"
)

// Plugin defines the interface that all plugins must implement
type Plugin interface {
	// Name returns the unique plugin identifier
	Name() string

	// Execute processes the video file and returns the output file path
	Execute(ctx context.Context, input PluginInput) (PluginOutput, error)

	// Validate checks if the plugin configuration is valid
	Validate(config map[string]interface{}) error
}

// PluginInput contains the input parameters for plugin execution
type PluginInput struct {
	FilePath  string                 // Input video file path
	JobID     uuid.UUID              // Job ID for progress tracking
	EpochTime int64                  // Epoch time for output directory structure
	Config    map[string]interface{} // Plugin-specific configuration
}

// PluginOutput contains the results of plugin execution
type PluginOutput struct {
	FilePath string // Output video file path (can be same as input for in-place operations)
	Error    error  // Plugin execution error
}
