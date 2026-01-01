package watermark

import (
	"fmt"
)

// WatermarkConfig holds the configuration for the watermark plugin
type WatermarkConfig struct {
	Text      string  `json:"text" mapstructure:"text"`
	Position  string  `json:"position" mapstructure:"position"`
	FontSize  int     `json:"font_size" mapstructure:"font_size"`
	FontColor string  `json:"font_color" mapstructure:"font_color"`
	Alpha     float64 `json:"alpha" mapstructure:"alpha"`
	Padding   int     `json:"padding" mapstructure:"padding"`
}

// SetDefaults sets default values for missing configuration
func (c *WatermarkConfig) SetDefaults() {
	if c.Text == "" {
		c.Text = "StreamForge"
	}
	if c.Position == "" {
		c.Position = "bottom-right"
	}
	if c.FontSize == 0 {
		c.FontSize = 24
	}
	if c.FontColor == "" {
		c.FontColor = "white"
	}
	if c.Alpha == 0 {
		c.Alpha = 0.8
	}
	if c.Padding == 0 {
		c.Padding = 10
	}
}

// Validate checks if the configuration is valid
func (c *WatermarkConfig) Validate() error {
	// Validate position
	validPositions := map[string]bool{
		"top-left":     true,
		"top-right":    true,
		"bottom-left":  true,
		"bottom-right": true,
	}
	if !validPositions[c.Position] {
		return fmt.Errorf("invalid position: %s (must be one of: top-left, top-right, bottom-left, bottom-right)", c.Position)
	}

	// Validate font size
	if c.FontSize <= 0 {
		return fmt.Errorf("font_size must be greater than 0, got: %d", c.FontSize)
	}

	// Validate alpha
	if c.Alpha < 0.0 || c.Alpha > 1.0 {
		return fmt.Errorf("alpha must be between 0.0 and 1.0, got: %.2f", c.Alpha)
	}

	// Validate padding
	if c.Padding < 0 {
		return fmt.Errorf("padding must be greater than or equal to 0, got: %d", c.Padding)
	}

	return nil
}

// GetPositionExpression returns the FFmpeg position expression based on the position setting
func (c *WatermarkConfig) GetPositionExpression() (x, y string) {
	switch c.Position {
	case "top-left":
		x = fmt.Sprintf("%d", c.Padding)
		y = fmt.Sprintf("%d", c.Padding)
	case "top-right":
		x = fmt.Sprintf("(w-tw-%d)", c.Padding)
		y = fmt.Sprintf("%d", c.Padding)
	case "bottom-left":
		x = fmt.Sprintf("%d", c.Padding)
		y = fmt.Sprintf("(h-th-%d)", c.Padding)
	case "bottom-right":
		x = fmt.Sprintf("(w-tw-%d)", c.Padding)
		y = fmt.Sprintf("(h-th-%d)", c.Padding)
	}
	return x, y
}
