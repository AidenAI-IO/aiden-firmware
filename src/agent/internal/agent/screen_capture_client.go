package agent

import (
	"fmt"
	"sync"
	"time"
)

const adbFallbackStickyDuration = 10 * time.Second

type screenCaptureSource interface {
	LatestFrame() (*frameMetadata, []byte, error)
	LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, error)
}

type screenshotCaptureInfoProvider interface {
	LastCaptureInfo() screenCaptureInfo
}

// ScreenCaptureClient prefers frame_service and falls back to adb when the
// frame socket is unavailable or stale. After a successful adb capture, it
// stays on adb briefly so stable-screen polling does not bounce between
// unrelated sequence counters.
type ScreenCaptureClient struct {
	primary  screenCaptureSource
	fallback screenCaptureSource

	captureMu           sync.Mutex
	mu                  sync.Mutex
	preferFallbackUntil time.Time
}

func NewScreenCaptureClient(socketPath string) *ScreenCaptureClient {
	return newScreenCaptureClient(NewFrameServiceClient(socketPath), NewADBScreenClient())
}

func newScreenCaptureClient(primary, fallback screenCaptureSource) *ScreenCaptureClient {
	return &ScreenCaptureClient{
		primary:  primary,
		fallback: fallback,
	}
}

func (c *ScreenCaptureClient) LatestFrame() (*frameMetadata, []byte, screenCaptureInfo, error) {
	return c.captureWithFallback(func(source screenCaptureSource) (*frameMetadata, []byte, error) {
		return source.LatestFrame()
	})
}

func (c *ScreenCaptureClient) LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, screenCaptureInfo, error) {
	return c.captureWithFallback(func(source screenCaptureSource) (*frameMetadata, []byte, error) {
		return source.LatestFrameWithFormat(format, quality)
	})
}

func (c *ScreenCaptureClient) captureWithFallback(call func(screenCaptureSource) (*frameMetadata, []byte, error)) (*frameMetadata, []byte, screenCaptureInfo, error) {
	if c == nil {
		return nil, nil, screenCaptureInfo{}, fmt.Errorf("screen capture client not configured")
	}
	c.captureMu.Lock()
	defer c.captureMu.Unlock()

	var fallbackMeta *frameMetadata
	var fallbackFrame []byte
	var fallbackInfo screenCaptureInfo
	var fallbackErr error
	fallbackTried := false
	if c.shouldPreferFallback() {
		fallbackMeta, fallbackFrame, fallbackInfo, fallbackErr = c.captureFromSource(c.fallback, call)
		fallbackTried = true
		if fallbackErr == nil {
			c.markFallbackPreferred()
			return fallbackMeta, fallbackFrame, fallbackInfo, nil
		}
	}

	primaryMeta, primaryFrame, primaryInfo, primaryErr := c.captureFromSource(c.primary, call)
	if primaryErr == nil {
		c.clearFallbackPreference()
		return primaryMeta, primaryFrame, primaryInfo, nil
	}

	if !fallbackTried {
		fallbackMeta, fallbackFrame, fallbackInfo, fallbackErr = c.captureFromSource(c.fallback, call)
		fallbackTried = true
	}
	if fallbackTried && fallbackErr == nil {
		c.markFallbackPreferred()
		return fallbackMeta, fallbackFrame, fallbackInfo, nil
	}

	return nil, nil, screenCaptureInfo{}, primaryErr
}

func (c *ScreenCaptureClient) captureFromSource(source screenCaptureSource, call func(screenCaptureSource) (*frameMetadata, []byte, error)) (*frameMetadata, []byte, screenCaptureInfo, error) {
	if source == nil {
		return nil, nil, screenCaptureInfo{}, fmt.Errorf("screen capture source not configured")
	}
	meta, frame, err := call(source)
	if err != nil {
		return nil, nil, screenCaptureInfo{}, err
	}
	if meta == nil {
		return nil, nil, screenCaptureInfo{}, fmt.Errorf("screen capture returned no metadata")
	}
	if meta.Stale {
		return nil, nil, screenCaptureInfo{}, fmt.Errorf("frame service: STALE_FRAME")
	}
	return meta, frame, c.captureInfoForSource(source), nil
}

func (c *ScreenCaptureClient) shouldPreferFallback() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Before(c.preferFallbackUntil)
}

func (c *ScreenCaptureClient) markFallbackPreferred() {
	c.mu.Lock()
	c.preferFallbackUntil = time.Now().Add(adbFallbackStickyDuration)
	c.mu.Unlock()
}

func (c *ScreenCaptureClient) clearFallbackPreference() {
	c.mu.Lock()
	c.preferFallbackUntil = time.Time{}
	c.mu.Unlock()
}

func (c *ScreenCaptureClient) captureInfoForSource(source screenCaptureSource) screenCaptureInfo {
	if provider, ok := source.(screenshotCaptureInfoProvider); ok {
		if info := provider.LastCaptureInfo(); info.Backend != "" || info.ADBDevice != nil {
			return cloneScreenCaptureInfo(info)
		}
	}
	switch source.(type) {
	case *FrameServiceClient:
		return screenCaptureInfo{Backend: "frame_service"}
	case *ADBScreenClient:
		return screenCaptureInfo{Backend: "adb"}
	default:
		return screenCaptureInfo{}
	}
}
