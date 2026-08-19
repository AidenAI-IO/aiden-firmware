package agent

import (
	"fmt"
	"strings"
)

// DefaultScreenMemoryTTL is the retention period for screen snapshots.
const DefaultScreenMemoryTTL = "90d"

// QuickCaptureConfig configures GPIO-triggered Screen Memory capture.
type QuickCaptureConfig struct {
	// Enabled turns Quick Capture on. Legacy GPIO Wakeup remains independent.
	Enabled *bool `toml:"enabled,omitempty"`

	// ScreenMemoryTTL is how long a captured memory lives. "forever" disables
	// expiry. Note that long-term memory is never reclaimed by storage cleanup,
	// so "forever" means these entries accumulate until deleted by hand.
	ScreenMemoryTTL string `toml:"screen_memory_ttl,omitempty"`

	// GPIOPin enables an independent falling-edge trigger. Zero disables it.
	// GPIO 3 (physical pin 38) is the verified spare pin on Luckfox Pico Zero.
	GPIOPin int `toml:"gpio_pin,omitempty"`
}

// EnabledOrDefault reports whether Quick Capture is on. With GPIOPin left at
// zero, the default does not start any watcher.
func (c QuickCaptureConfig) EnabledOrDefault() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return true
}

func (c QuickCaptureConfig) ScreenMemoryTTLOrDefault() string {
	if ttl := strings.TrimSpace(c.ScreenMemoryTTL); ttl != "" {
		return ttl
	}
	return DefaultScreenMemoryTTL
}

// Validate rejects settings that would silently misbehave.
func (c QuickCaptureConfig) Validate() error {
	if c.GPIOPin < 0 {
		return fmt.Errorf("quick_capture.gpio_pin must be >= 0, got %d", c.GPIOPin)
	}
	if c.GPIOPin == 32 || c.GPIOPin == 33 {
		return fmt.Errorf("quick_capture.gpio_pin %d is reserved for legacy wakeup", c.GPIOPin)
	}
	if ttl := strings.TrimSpace(c.ScreenMemoryTTL); ttl != "" && !strings.EqualFold(ttl, "forever") {
		if _, ok := parseRetentionDuration(ttl); !ok {
			return fmt.Errorf("invalid quick_capture.screen_memory_ttl: %q (expected a duration such as 90d, or forever)", c.ScreenMemoryTTL)
		}
	}
	return nil
}
