package screenprovider

import (
	"errors"
	"testing"
	"time"
)

type fakeScreenCaptureSource struct {
	latestFrameCalls           int
	latestFrameWithFormatCalls int
	latestFrameFn              func() (*FrameMetadata, []byte, error)
	latestFrameWithFormatFn    func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error)
	lastCaptureInfo            CaptureInfo
}

type fakeHealthyScreenCaptureSource struct {
	*fakeScreenCaptureSource
	health *HealthResult
}

type fakeReadyScreenCaptureSource struct {
	*fakeScreenCaptureSource
	health      *HealthResult
	waitErr     error
	waitCalls   int
	waitTimeout time.Duration
}

func (f *fakeHealthyScreenCaptureSource) Health() (*HealthResult, error) {
	return f.health, nil
}

func (f *fakeReadyScreenCaptureSource) WaitUntilReady(timeout time.Duration) (*HealthResult, error) {
	f.waitCalls++
	f.waitTimeout = timeout
	return f.health, f.waitErr
}

func (f *fakeScreenCaptureSource) LatestFrame() (*FrameMetadata, []byte, error) {
	f.latestFrameCalls++
	if f.latestFrameFn == nil {
		return nil, nil, errors.New("LatestFrame not configured")
	}
	return f.latestFrameFn()
}

func (f *fakeScreenCaptureSource) LatestFrameWithFormat(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
	f.latestFrameWithFormatCalls++
	if f.latestFrameWithFormatFn == nil {
		return nil, nil, errors.New("LatestFrameWithFormat not configured")
	}
	return f.latestFrameWithFormatFn(format, quality, cropBlack, hint)
}

func (f *fakeScreenCaptureSource) LastCaptureInfo() CaptureInfo {
	return CloneCaptureInfo(f.lastCaptureInfo)
}

func TestScreenCaptureClientWaitsForFrameServiceBeforeCapture(t *testing.T) {
	primary := &fakeReadyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 7, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("jpeg"), nil
			},
		},
		health: &HealthResult{State: "RUNNING", CaptureMode: "buffered", LatestSeq: 7, FrameAgeMs: 10},
	}

	client := NewFallbackFromSources(primary, nil)
	meta, frame, _, err := client.LatestFrameWithFormatWhenReady("jpeg", DefaultJPEGQuality, false, CropHint{})
	if err != nil {
		t.Fatalf("LatestFrameWithFormatWhenReady() error = %v", err)
	}
	if primary.waitCalls != 1 || primary.waitTimeout != frameServiceStartupWaitTimeout {
		t.Fatalf("ready wait calls=%d timeout=%s, want 1 and %s", primary.waitCalls, primary.waitTimeout, frameServiceStartupWaitTimeout)
	}
	if primary.latestFrameWithFormatCalls != 1 || meta.Seq != 7 || string(frame) != "jpeg" {
		t.Fatalf("unexpected capture: calls=%d meta=%#v frame=%q", primary.latestFrameWithFormatCalls, meta, frame)
	}
}

func TestScreenCaptureClientCapturesOnDemandFrameFromStartingStateWhenReady(t *testing.T) {
	primary := &fakeReadyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fresh"), nil
			},
		},
		health: &HealthResult{State: "STARTING", CaptureMode: "on_demand", LatestSeq: 0},
	}

	client := NewFallbackFromSources(primary, nil)
	meta, frame, _, err := client.LatestFrameWithFormatWhenReady("jpeg", DefaultJPEGQuality, false, CropHint{})
	if err != nil {
		t.Fatalf("LatestFrameWithFormatWhenReady() error = %v", err)
	}
	if meta.Seq != 1 || string(frame) != "fresh" || primary.latestFrameWithFormatCalls != 1 {
		t.Fatalf("unexpected capture: calls=%d meta=%#v frame=%q", primary.latestFrameWithFormatCalls, meta, frame)
	}
}

func TestScreenCaptureClientFastPathRejectsStartingOnDemandFrame(t *testing.T) {
	primary := &fakeHealthyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 1}, []byte("unexpected"), nil
			},
		},
		health: &HealthResult{State: "STARTING", CaptureMode: "on_demand", LatestSeq: 0},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fallback"), nil
		},
		lastCaptureInfo: CaptureInfo{Backend: "adb"},
	}

	client := NewFallbackFromSources(primary, fallback)
	_, frame, info, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, false, CropHint{})
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if primary.latestFrameWithFormatCalls != 0 || string(frame) != "fallback" || info.Backend != "adb" {
		t.Fatalf("primary calls=%d frame=%q backend=%q, want fast adb fallback", primary.latestFrameWithFormatCalls, frame, info.Backend)
	}
}

func TestScreenCaptureClientDoesNotCaptureWhenFrameServiceReadyWaitFails(t *testing.T) {
	primary := &fakeReadyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 7}, []byte("unexpected"), nil
			},
		},
		waitErr: errors.New("startup timeout"),
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return nil, nil, errors.New("no connected adb device")
		},
	}

	client := NewFallbackFromSources(primary, fallback)
	_, _, _, err := client.LatestFrameWithFormatWhenReady("jpeg", DefaultJPEGQuality, false, CropHint{})
	if err == nil || err.Error() != "frame service health: startup timeout" {
		t.Fatalf("error = %v, want wrapped startup timeout", err)
	}
	if primary.latestFrameWithFormatCalls != 0 {
		t.Fatalf("capture calls = %d, want 0 before readiness", primary.latestFrameWithFormatCalls)
	}
}

func TestScreenCaptureClientFallsBackWhenPrimaryFails(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("jpeg"), nil
		},
		lastCaptureInfo: CaptureInfo{Backend: "adb"},
	}

	client := NewFallbackFromSources(primary, fallback)
	meta, frame, info, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, true, CropHint{})
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
		latestFrameFn: func() (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 9, Width: 2, Height: 2, PixelFormat: "png", Stale: true}, []byte("stale"), nil
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameFn: func() (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 2, PixelFormat: "png"}, []byte("fresh"), nil
		},
		lastCaptureInfo: CaptureInfo{Backend: "adb"},
	}

	client := NewFallbackFromSources(primary, fallback)
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
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 3048, Width: 2, Height: 1, PixelFormat: "jpeg", Stale: false}, []byte("old"), nil
			},
		},
		health: &HealthResult{State: "RUNNING", LatestSeq: 3048, FrameAgeMs: 10_000},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fresh"), nil
		},
		lastCaptureInfo: CaptureInfo{Backend: "adb"},
	}

	client := NewFallbackFromSources(primary, fallback)
	_, frame, info, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, true, CropHint{})
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if string(frame) != "fresh" || info.Backend != "adb" {
		t.Fatalf("capture = %q via %q, want fresh adb fallback", string(frame), info.Backend)
	}
}

func TestScreenCaptureClientRequestsFreshOnDemandFrameAfterLongIdle(t *testing.T) {
	primary := &fakeHealthyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 3049, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fresh"), nil
			},
		},
		health: &HealthResult{
			State:       "RUNNING",
			CaptureMode: "on_demand",
			LatestSeq:   3048,
			FrameAgeMs:  10_000,
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fallback"), nil
		},
	}

	client := NewFallbackFromSources(primary, fallback)
	meta, frame, info, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, true, CropHint{})
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if meta.Seq != 3049 || string(frame) != "fresh" || info.Backend != "" {
		t.Fatalf("capture = seq %d payload %q backend %q, want fresh primary", meta.Seq, string(frame), info.Backend)
	}
	if primary.latestFrameWithFormatCalls != 1 {
		t.Fatalf("primary calls = %d, want 1", primary.latestFrameWithFormatCalls)
	}
	if fallback.latestFrameWithFormatCalls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.latestFrameWithFormatCalls)
	}
}

func TestScreenCaptureClientTreatsFrameOlderThanPreCaptureHealthAsStale(t *testing.T) {
	primary := &fakeHealthyScreenCaptureSource{
		fakeScreenCaptureSource: &fakeScreenCaptureSource{
			latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
				return &FrameMetadata{Seq: 3048, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("cached"), nil
			},
		},
		health: &HealthResult{State: "RUNNING", LatestSeq: 10663, FrameAgeMs: 139},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("fresh"), nil
		},
		lastCaptureInfo: CaptureInfo{Backend: "adb"},
	}

	client := NewFallbackFromSources(primary, fallback)
	_, frame, info, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, true, CropHint{})
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if string(frame) != "fresh" || info.Backend != "adb" {
		t.Fatalf("capture = %q via %q, want fresh adb fallback", string(frame), info.Backend)
	}
}

func TestScreenCaptureClientKeepsUsingFallbackBrieflyAfterSuccess(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameFn: func() (*FrameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: TIMEOUT")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameFn: func() (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 1, Height: 1, PixelFormat: "png"}, []byte("ok"), nil
		},
	}

	client := NewFallbackFromSources(primary, fallback)
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

func TestScreenCaptureClientRetriesPrimaryAfterFallbackStickyDurationExpires(t *testing.T) {
	primaryAvailable := false
	primary := &fakeScreenCaptureSource{
		latestFrameFn: func() (*FrameMetadata, []byte, error) {
			if !primaryAvailable {
				return nil, nil, errors.New("frame service: TIMEOUT")
			}
			return &FrameMetadata{Seq: 2, Width: 1, Height: 1, PixelFormat: "png"}, []byte("primary"), nil
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameFn: func() (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 1, Height: 1, PixelFormat: "png"}, []byte("fallback"), nil
		},
	}

	client := NewFallbackFromSources(primary, fallback)
	if _, _, _, err := client.LatestFrame(); err != nil {
		t.Fatalf("first LatestFrame() error = %v", err)
	}

	client.mu.Lock()
	stickyDeadline := client.preferFallbackUntil
	client.mu.Unlock()
	if stickyDeadline.IsZero() {
		t.Fatal("expected fallback sticky deadline")
	}

	if _, _, _, err := client.LatestFrame(); err != nil {
		t.Fatalf("second LatestFrame() error = %v", err)
	}
	client.mu.Lock()
	deadlineAfterFallback := client.preferFallbackUntil
	client.preferFallbackUntil = time.Now().Add(-time.Nanosecond)
	client.mu.Unlock()
	if !deadlineAfterFallback.Equal(stickyDeadline) {
		t.Fatalf("sticky deadline extended from %v to %v", stickyDeadline, deadlineAfterFallback)
	}

	primaryAvailable = true
	_, frame, _, err := client.LatestFrame()
	if err != nil {
		t.Fatalf("LatestFrame() after sticky expiry error = %v", err)
	}
	if string(frame) != "primary" {
		t.Fatalf("capture = %q, want primary after sticky expiry", string(frame))
	}
	if primary.latestFrameCalls != 2 {
		t.Fatalf("primary calls = %d, want retry after sticky expiry", primary.latestFrameCalls)
	}
}

func TestScreenCaptureClientReturnsPrimaryErrorWhenFallbackUnavailable(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return nil, nil, errors.New("no connected adb device")
		},
	}

	client := NewFallbackFromSources(primary, fallback)
	_, _, _, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, true, CropHint{})
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "frame service: SERVICE_RECOVERING" {
		t.Fatalf("error = %q, want primary error", got)
	}
}

func TestScreenCaptureClientReportsFallbackCaptureInfo(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error) {
			return &FrameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("jpeg"), nil
		},
		lastCaptureInfo: CaptureInfo{
			Backend: "adb",
			ADBDevice: &DeviceInfo{
				Serial: "serial123",
				Name:   "Pixel 9",
				State:  "device",
			},
		},
	}

	client := NewFallbackFromSources(primary, fallback)
	_, _, info, err := client.LatestFrameWithFormat("jpeg", DefaultJPEGQuality, true, CropHint{})
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
