package watermark

import (
	"fmt"
)

// WatermarkConfig holds the configuration for the watermark plugin
type WatermarkConfig struct {
	Type        string  `json:"type" mapstructure:"type"`                 // "text" or "image"
	Text        string  `json:"text" mapstructure:"text"`                 // Text watermark content
	Position    string  `json:"position" mapstructure:"position"`         // Position: top-left, top-right, bottom-left, bottom-right
	FontSize    int     `json:"font_size" mapstructure:"font_size"`       // Font size for text watermark
	FontColor   string  `json:"font_color" mapstructure:"font_color"`     // Font color for text watermark
	Alpha       float64 `json:"alpha" mapstructure:"alpha"`               // Opacity (0.0-1.0)
	Padding     int     `json:"padding" mapstructure:"padding"`           // Padding from edges in pixels
	ImagePath   string  `json:"image_path" mapstructure:"image_path"`     // Path to image watermark
	ImageWidth  int     `json:"image_width" mapstructure:"image_width"`   // Image width (0 = use scale)
	ImageHeight int     `json:"image_height" mapstructure:"image_height"` // Image height (0 = use scale)
	ImageScale  float64 `json:"image_scale" mapstructure:"image_scale"`   // Image scale factor (0.0-1.0)
}

// SetDefaults sets default values for missing configuration
func (c *WatermarkConfig) SetDefaults() {
	// Determine type based on what's provided
	if c.Type == "" {
		if c.ImagePath != "" {
			c.Type = "image"
		} else {
			c.Type = "text"
		}
	}

	// Text watermark defaults
	if c.Type == "text" {
		if c.Text == "" {
			c.Text = "StreamForge"
		}
		if c.FontSize == 0 {
			c.FontSize = 24
		}
		if c.FontColor == "" {
			c.FontColor = "white"
		}
	}

	// Image watermark defaults
	if c.Type == "image" {
		if c.ImageScale == 0 && c.ImageWidth == 0 && c.ImageHeight == 0 {
			c.ImageScale = 0.1 // Default to 10% of video size
		}
	}

	// Common defaults
	if c.Position == "" {
		c.Position = "bottom-right"
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
	// Validate type
	validTypes := map[string]bool{
		"text":  true,
		"image": true,
	}
	if !validTypes[c.Type] {
		return fmt.Errorf("invalid type: %s (must be one of: text, image)", c.Type)
	}

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

	// Type-specific validation
	if c.Type == "text" {
		if c.Text == "" {
			return fmt.Errorf("text is required for text watermark")
		}
		if c.FontSize <= 0 {
			return fmt.Errorf("font_size must be greater than 0, got: %d", c.FontSize)
		}
	} else if c.Type == "image" {
		if c.ImagePath == "" {
			return fmt.Errorf("image_path is required for image watermark")
		}
		if c.ImageScale < 0.0 || c.ImageScale > 1.0 {
			if c.ImageScale != 0 { // 0 is allowed if width/height are specified
				return fmt.Errorf("image_scale must be between 0.0 and 1.0, got: %.2f", c.ImageScale)
			}
		}
		if c.ImageWidth < 0 {
			return fmt.Errorf("image_width must be greater than or equal to 0, got: %d", c.ImageWidth)
		}
		if c.ImageHeight < 0 {
			return fmt.Errorf("image_height must be greater than or equal to 0, got: %d", c.ImageHeight)
		}
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

// GetPositionExpression returns the FFmpeg position expression for text watermark
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

// GetImagePositionExpression returns the FFmpeg overlay position expression for image watermark
func (c *WatermarkConfig) GetImagePositionExpression() (x, y string) {
	switch c.Position {
	case "top-left":
		x = fmt.Sprintf("%d", c.Padding)
		y = fmt.Sprintf("%d", c.Padding)
	case "top-right":
		x = fmt.Sprintf("(main_w-overlay_w-%d)", c.Padding)
		y = fmt.Sprintf("%d", c.Padding)
	case "bottom-left":
		x = fmt.Sprintf("%d", c.Padding)
		y = fmt.Sprintf("(main_h-overlay_h-%d)", c.Padding)
	case "bottom-right":
		x = fmt.Sprintf("(main_w-overlay_w-%d)", c.Padding)
		y = fmt.Sprintf("(main_h-overlay_h-%d)", c.Padding)
	}
	return x, y
}
