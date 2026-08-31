package screenprovider

import (
	"fmt"
	"sync"
	"time"
)

const adbFallbackStickyDuration = 10 * time.Second
const frameServiceFreshFrameMaxAgeMs = 2000
const frameServiceStartupWaitTimeout = 5 * time.Second

type captureInfoProvider interface {
	LastCaptureInfo() CaptureInfo
}

type healthSource interface {
	Health() (*HealthResult, error)
}

type readySource interface {
	WaitUntilReady(timeout time.Duration) (*HealthResult, error)
}

// Fallback prefers frame_service and falls back to adb when the frame socket
// is unavailable or stale. After a successful adb capture, it stays on adb
// briefly so stable-screen polling does not bounce between unrelated sequence counters.
type Fallback struct {
	primary  Source
	fallback Source

	captureMu           sync.Mutex
	mu                  sync.Mutex
	preferFallbackUntil time.Time
}

func NewFallback(socketPath string) *Fallback {
	return NewFallbackFromSources(NewFrameService(socketPath), NewADB())
}

func NewFallbackFromSources(primary, fallback Source) *Fallback {
	return &Fallback{
		primary:  primary,
		fallback: fallback,
	}
}

func (c *Fallback) LatestFrame() (*FrameMetadata, []byte, CaptureInfo, error) {
	return c.captureWithFallback(false, func(source Source) (*FrameMetadata, []byte, error) {
		return source.LatestFrame()
	})
}

func (c *Fallback) LatestFrameWithFormat(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, CaptureInfo, error) {
	return c.captureWithFallback(false, func(source Source) (*FrameMetadata, []byte, error) {
		return source.LatestFrameWithFormat(format, quality, cropBlack, hint)
	})
}

func (c *Fallback) LatestFrameWithFormatWhenReady(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, CaptureInfo, error) {
	return c.captureWithFallback(true, func(source Source) (*FrameMetadata, []byte, error) {
		return source.LatestFrameWithFormat(format, quality, cropBlack, hint)
	})
}

func (c *Fallback) captureWithFallback(waitForReady bool, call func(Source) (*FrameMetadata, []byte, error)) (*FrameMetadata, []byte, CaptureInfo, error) {
	if c == nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("screen capture client not configured")
	}
	c.captureMu.Lock()
	defer c.captureMu.Unlock()

	var fallbackMeta *FrameMetadata
	var fallbackFrame []byte
	var fallbackInfo CaptureInfo
	var fallbackErr error
	fallbackTried := false
	if c.shouldPreferFallback() {
		fallbackMeta, fallbackFrame, fallbackInfo, fallbackErr = c.captureFromSource(c.fallback, false, call)
		fallbackTried = true
		if fallbackErr == nil {
			c.markFallbackPreferred()
			return fallbackMeta, fallbackFrame, fallbackInfo, nil
		}
	}

	primaryMeta, primaryFrame, primaryInfo, primaryErr := c.captureFromSource(c.primary, waitForReady, call)
	if primaryErr == nil {
		c.clearFallbackPreference()
		return primaryMeta, primaryFrame, primaryInfo, nil
	}

	if !fallbackTried {
		fallbackMeta, fallbackFrame, fallbackInfo, fallbackErr = c.captureFromSource(c.fallback, false, call)
		fallbackTried = true
	}
	if fallbackTried && fallbackErr == nil {
		c.markFallbackPreferred()
		return fallbackMeta, fallbackFrame, fallbackInfo, nil
	}

	return nil, nil, CaptureInfo{}, primaryErr
}

func (c *Fallback) captureFromSource(source Source, waitForReady bool, call func(Source) (*FrameMetadata, []byte, error)) (*FrameMetadata, []byte, CaptureInfo, error) {
	if source == nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("screen capture source not configured")
	}
	var health *HealthResult
	if rs, ok := source.(readySource); waitForReady && ok {
		var healthErr error
		health, healthErr = rs.WaitUntilReady(frameServiceStartupWaitTimeout)
		if healthErr != nil {
			return nil, nil, CaptureInfo{}, fmt.Errorf("frame service health: %w", healthErr)
		}
	} else if hs, ok := source.(healthSource); ok {
		var healthErr error
		health, healthErr = hs.Health()
		if healthErr != nil {
			return nil, nil, CaptureInfo{}, fmt.Errorf("frame service health: %w", healthErr)
		}
	}
	if !frameServiceCaptureAllowed(health, waitForReady) {
		state := "UNKNOWN"
		captureMode := ""
		var age uint64
		if health != nil {
			state = health.State
			captureMode = health.CaptureMode
			age = health.FrameAgeMs
		}
		return nil, nil, CaptureInfo{}, fmt.Errorf(
			"frame service: STALE_FRAME (state=%s capture_mode=%s frame_age_ms=%d)",
			state, captureMode, age,
		)
	}
	meta, frame, err := call(source)
	if err != nil {
		return nil, nil, CaptureInfo{}, err
	}
	if meta == nil {
		return nil, nil, CaptureInfo{}, fmt.Errorf("screen capture returned no metadata")
	}
	if meta.Stale {
		return nil, nil, CaptureInfo{}, fmt.Errorf("frame service: STALE_FRAME")
	}
	if health != nil && health.LatestSeq > 0 && meta.Seq < health.LatestSeq {
		return nil, nil, CaptureInfo{}, fmt.Errorf("frame service: STALE_FRAME (frame_seq=%d health_latest_seq=%d)", meta.Seq, health.LatestSeq)
	}
	return meta, frame, c.captureInfoForSource(source), nil
}

func frameServiceCaptureAllowed(health *HealthResult, waitForReady bool) bool {
	if health == nil {
		return true
	}
	if health.CaptureMode == "on_demand" {
		if health.State == "RUNNING" {
			return true
		}
		return waitForReady && (health.State == "STARTING" || health.State == "RECOVERING")
	}
	return health.State == "RUNNING" && health.FrameAgeMs <= frameServiceFreshFrameMaxAgeMs
}

func (c *Fallback) shouldPreferFallback() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.preferFallbackUntil)
}

func (c *Fallback) markFallbackPreferred() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.Before(c.preferFallbackUntil) {
		return
	}
	c.preferFallbackUntil = now.Add(adbFallbackStickyDuration)
}

func (c *Fallback) clearFallbackPreference() {
	c.mu.Lock()
	c.preferFallbackUntil = time.Time{}
	c.mu.Unlock()
}

func (c *Fallback) captureInfoForSource(source Source) CaptureInfo {
	if provider, ok := source.(captureInfoProvider); ok {
		if info := provider.LastCaptureInfo(); info.Backend != "" || info.ADBDevice != nil {
			return CloneCaptureInfo(info)
		}
	}
	switch source.(type) {
	case *FrameService:
		return CaptureInfo{Backend: "frame_service"}
	case *ADB:
		return CaptureInfo{Backend: "adb"}
	default:
		return CaptureInfo{}
	}
}
