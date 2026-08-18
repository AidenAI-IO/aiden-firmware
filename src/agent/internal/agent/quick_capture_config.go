package agent

import (
	"fmt"
	"strings"
)

// DefaultScreenMemoryTTL is the retention period for screen snapshots.
const DefaultScreenMemoryTTL = "90d"

// QuickCaptureConfig configures the standalone Screen Memory capture endpoint.
type QuickCaptureConfig struct {
	// Enabled turns the manual capture endpoint on. GPIO Wakeup is independent
	// and always retains its legacy behavior until a hardware trigger is chosen.
	Enabled *bool `toml:"enabled,omitempty"`

	// ScreenMemoryTTL is how long a captured memory lives. "forever" disables
	// expiry. Note that long-term memory is never reclaimed by storage cleanup,
	// so "forever" means these entries accumulate until deleted by hand.
	ScreenMemoryTTL string `toml:"screen_memory_ttl,omitempty"`
}

// EnabledOrDefault reports whether the manual Quick Capture endpoint is on.
// It defaults to on so a development build can be exercised without extra
// configuration.
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
	if ttl := strings.TrimSpace(c.ScreenMemoryTTL); ttl != "" && !strings.EqualFold(ttl, "forever") {
		if _, ok := parseRetentionDuration(ttl); !ok {
			return fmt.Errorf("invalid quick_capture.screen_memory_ttl: %q (expected a duration such as 90d, or forever)", c.ScreenMemoryTTL)
		}
	}
	return nil
}
