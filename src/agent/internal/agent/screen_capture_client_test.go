package agent

import (
	"errors"
	"testing"
)

type fakeScreenCaptureSource struct {
	latestFrameCalls           int
	latestFrameWithFormatCalls int
	latestFrameFn              func() (*frameMetadata, []byte, error)
	latestFrameWithFormatFn    func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error)
	lastCaptureInfo            screenCaptureInfo
}

type fakeHealthyScreenCaptureSource struct {
	*fakeScreenCaptureSource
	health *FrameHealthResult
}

func (f *fakeHealthyScreenCaptureSource) Health() (*FrameHealthResult, error) {
	return f.health, nil
}

func (f *fakeScreenCaptureSource) LatestFrame() (*frameMetadata, []byte, error) {
	f.latestFrameCalls++
	if f.latestFrameFn == nil {
		return nil, nil, errors.New("LatestFrame not configured")
	}
	return f.latestFrameFn()
}

func (f *fakeScreenCaptureSource) LatestFrameWithFormat(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
	f.latestFrameWithFormatCalls++
	if f.latestFrameWithFormatFn == nil {
		return nil, nil, errors.New("LatestFrameWithFormat not configured")
	}
	return f.latestFrameWithFormatFn(format, quality, cropBlack, minimalWidth)
}

func (f *fakeScreenCaptureSource) LastCaptureInfo() screenCaptureInfo {
	return cloneScreenCaptureInfo(f.lastCaptureInfo)
}

func TestScreenCaptureClientFallsBackWhenPrimaryFails(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("jpeg"), nil
		},
		lastCaptureInfo: screenCaptureInfo{Backend: "adb"},
	}

	client := newScreenCaptureClient(primary, fallback)
	meta, frame, info, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality, true, 0)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if meta == nil || meta.PixelFormat != "jpeg" {
		t.Fatalf("unexpected fallback metadata: %#v", meta)
	}
	if string(frame) != "jpeg" {
		t.Fatalf("unexpected fallback payload: %q", string(frame))
	}
	if info.Backend != "adb" {
		t.Fatalf("capture backend = %q, want adb", info.Backend)
	}
	if primary.latestFrameWithFormatCalls != 1 {
		t.Fatalf("primary calls = %d, want 1", primary.latestFrameWithFormatCalls)
	}
	if fallback.latestFrameWithFormatCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.latestFrameWithFormatCalls)
	}
}

func TestScreenCaptureClientTreatsStalePrimaryFrameAsFailure(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameFn: func() (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 9, Width: 2, Height: 2, PixelFormat: "png", Stale: true}, []byte("stale"), nil
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameFn: func() (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 2, Height: 2, PixelFormat: "png"}, []byte("fresh"), nil
		},
		lastCaptureInfo: screenCaptureInfo{Backend: "adb"},
	}

	client := newScreenCaptureClient(primary, fallback)
	meta, frame, info, err := client.LatestFrame()
	if err != nil {
		t.Fatalf("LatestFrame() error = %v", err)
	}
	if meta == nil || meta.Stale {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if string(frame) != "fresh" {
		t.Fatalf("unexpected fallback payload: %q", string(frame))
	}
	if info.Backend != "adb" {
		t.Fatalf("capture backend = %q, want adb", info.Backend)
	}
}

func TestScreenCaptureClientTreatsOldRunningPrimaryFrameAsStale(t *testing.T) {
	primary := &fakeHealthyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
				return &frameMetadata{Seq: 3048, Width: 2, Height: 1, PixelFormat: "jpeg", Stale: false}, []byte("old"), nil
			},
		},
		health: &FrameHealthResult{State: "RUNNING", LatestSeq: 3048, FrameAgeMs: 10_000},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fresh"), nil
		},
		lastCaptureInfo: screenCaptureInfo{Backend: "adb"},
	}

	client := newScreenCaptureClient(primary, fallback)
	_, frame, info, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality, true, 0)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if string(frame) != "fresh" || info.Backend != "adb" {
		t.Fatalf("capture = %q via %q, want fresh adb fallback", string(frame), info.Backend)
	}
}

func TestScreenCaptureClientTreatsFrameOlderThanPreCaptureHealthAsStale(t *testing.T) {
	primary := &fakeHealthyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
				return &frameMetadata{Seq: 3048, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("cached"), nil
			},
		},
		health: &FrameHealthResult{State: "RUNNING", LatestSeq: 10663, FrameAgeMs: 139},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fresh"), nil
		},
		lastCaptureInfo: screenCaptureInfo{Backend: "adb"},
	}

	client := newScreenCaptureClient(primary, fallback)
	_, frame, info, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality, true, 0)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if string(frame) != "fresh" || info.Backend != "adb" {
		t.Fatalf("capture = %q via %q, want fresh adb fallback", string(frame), info.Backend)
	}
}

func TestScreenCaptureClientKeepsUsingFallbackBrieflyAfterSuccess(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameFn: func() (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: TIMEOUT")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameFn: func() (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 1, Height: 1, PixelFormat: "png"}, []byte("ok"), nil
		},
	}

	client := newScreenCaptureClient(primary, fallback)
	if _, _, _, err := client.LatestFrame(); err != nil {
		t.Fatalf("first LatestFrame() error = %v", err)
	}
	if _, _, _, err := client.LatestFrame(); err != nil {
		t.Fatalf("second LatestFrame() error = %v", err)
	}
	if primary.latestFrameCalls != 1 {
		t.Fatalf("primary calls = %d, want sticky fallback to keep it at 1", primary.latestFrameCalls)
	}
	if fallback.latestFrameCalls != 2 {
		t.Fatalf("fallback calls = %d, want 2", fallback.latestFrameCalls)
	}
}

func TestScreenCaptureClientReturnsPrimaryErrorWhenFallbackUnavailable(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("no connected adb device")
		},
	}

	client := newScreenCaptureClient(primary, fallback)
	_, _, _, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality, true, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "frame service: SERVICE_RECOVERING" {
		t.Fatalf("error = %q, want primary error", got)
	}
}

func TestScreenCaptureClientReportsFallbackCaptureInfo(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, minimalWidth int) (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("jpeg"), nil
		},
		lastCaptureInfo: screenCaptureInfo{
			Backend: "adb",
			ADBDevice: &adbDeviceInfo{
				Serial: "serial123",
				Name:   "Pixel 9",
				State:  "device",
			},
		},
	}

	client := newScreenCaptureClient(primary, fallback)
	_, _, info, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality, true, 0)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if info.Backend != "adb" {
		t.Fatalf("capture backend = %q, want adb", info.Backend)
	}
	if info.ADBDevice == nil {
		t.Fatal("expected adb device info")
	}
	if info.ADBDevice.Serial != "serial123" || info.ADBDevice.Name != "Pixel 9" || info.ADBDevice.State != "device" {
		t.Fatalf("unexpected adb device info: %#v", info.ADBDevice)
	}
}
