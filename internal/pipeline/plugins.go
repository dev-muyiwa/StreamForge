package pipeline

import (
	types "StreamForge/pkg"
	"StreamForge/pkg/plugin"
	"context"
	"fmt"

	"go.uber.org/zap"
)

// PluginProcessor orchestrates plugin execution within the workflow
type PluginProcessor struct {
	registry *plugin.Registry
	configs  []types.PluginConfig
	logger   *zap.Logger
}

// NewPluginProcessor creates a new plugin processor
func NewPluginProcessor(registry *plugin.Registry, configs []types.PluginConfig, logger *zap.Logger) *PluginProcessor {
	return &PluginProcessor{
		registry: registry,
		configs:  configs,
		logger:   logger,
	}
}

// ProcessPlugins executes all enabled plugins sequentially
func (p *PluginProcessor) ProcessPlugins(ctx context.Context, input ActivityInput) (ActivityOutput, error) {
	currentFilePath := input.FilePath

	// Filter enabled plugins
	enabledPlugins := p.getEnabledPlugins()
	if len(enabledPlugins) == 0 {
		p.logger.Info("No enabled plugins to process")
		return ActivityOutput{FilePath: currentFilePath}, nil
	}

	p.logger.Info("Processing plugins", zap.Int("count", len(enabledPlugins)))

	// Execute plugins sequentially
	for _, pluginConfig := range enabledPlugins {
		pluginInstance, exists := p.registry.Get(pluginConfig.Name)
		if !exists {
			return ActivityOutput{}, fmt.Errorf("plugin %s not found in registry", pluginConfig.Name)
		}

		p.logger.Info("Executing plugin", zap.String("name", pluginConfig.Name))

		// Validate plugin config
		if err := pluginInstance.Validate(pluginConfig.Config); err != nil {
			return ActivityOutput{}, fmt.Errorf("plugin %s config validation failed: %w", pluginConfig.Name, err)
		}

		// Execute plugin
		pluginInput := plugin.PluginInput{
			FilePath:  currentFilePath,
			JobID:     input.JobID,
			EpochTime: input.EpochTime,
			Config:    pluginConfig.Config,
		}

		pluginOutput, err := pluginInstance.Execute(ctx, pluginInput)
		if err != nil {
			return ActivityOutput{}, fmt.Errorf("plugin %s execution failed: %w", pluginConfig.Name, err)
		}

		if pluginOutput.Error != nil {
			return ActivityOutput{}, fmt.Errorf("plugin %s returned error: %w", pluginConfig.Name, pluginOutput.Error)
		}

		// Update current file path for next plugin
		currentFilePath = pluginOutput.FilePath
		p.logger.Info("Plugin executed successfully", zap.String("name", pluginConfig.Name), zap.String("output_path", currentFilePath))
	}

	return ActivityOutput{FilePath: currentFilePath}, nil
}

// getEnabledPlugins filters and returns only enabled plugins from config
func (p *PluginProcessor) getEnabledPlugins() []types.PluginConfig {
	enabled := make([]types.PluginConfig, 0)
	for _, config := range p.configs {
		if config.Enabled {
			enabled = append(enabled, config)
		}
	}
	return enabled
}
