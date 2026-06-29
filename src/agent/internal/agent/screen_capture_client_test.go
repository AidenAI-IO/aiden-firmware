package agent

import (
	"errors"
	"testing"
)

type fakeScreenCaptureSource struct {
	latestFrameCalls           int
	latestFrameWithFormatCalls int
	latestFrameFn              func() (*frameMetadata, []byte, error)
	latestFrameWithFormatFn    func(format string, quality int) (*frameMetadata, []byte, error)
}

func (f *fakeScreenCaptureSource) LatestFrame() (*frameMetadata, []byte, error) {
	f.latestFrameCalls++
	if f.latestFrameFn == nil {
		return nil, nil, errors.New("LatestFrame not configured")
	}
	return f.latestFrameFn()
}

func (f *fakeScreenCaptureSource) LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, error) {
	f.latestFrameWithFormatCalls++
	if f.latestFrameWithFormatFn == nil {
		return nil, nil, errors.New("LatestFrameWithFormat not configured")
	}
	return f.latestFrameWithFormatFn(format, quality)
}

func TestScreenCaptureClientFallsBackWhenPrimaryFails(t *testing.T) {
	primary := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int) (*frameMetadata, []byte, error) {
			return &frameMetadata{Seq: 1, Width: 2, Height: 1, PixelFormat: "jpeg"}, []byte("jpeg"), nil
		},
	}

	client := newScreenCaptureClient(primary, fallback)
	meta, frame, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if meta == nil || meta.PixelFormat != "jpeg" {
		t.Fatalf("unexpected fallback metadata: %#v", meta)
	}
	if string(frame) != "jpeg" {
		t.Fatalf("unexpected fallback payload: %q", string(frame))
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
	}

	client := newScreenCaptureClient(primary, fallback)
	meta, frame, err := client.LatestFrame()
	if err != nil {
		t.Fatalf("LatestFrame() error = %v", err)
	}
	if meta == nil || meta.Stale {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if string(frame) != "fresh" {
		t.Fatalf("unexpected fallback payload: %q", string(frame))
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
	if _, _, err := client.LatestFrame(); err != nil {
		t.Fatalf("first LatestFrame() error = %v", err)
	}
	if _, _, err := client.LatestFrame(); err != nil {
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
		latestFrameWithFormatFn: func(format string, quality int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("frame service: SERVICE_RECOVERING")
		},
	}
	fallback := &fakeScreenCaptureSource{
		latestFrameWithFormatFn: func(format string, quality int) (*frameMetadata, []byte, error) {
			return nil, nil, errors.New("no connected adb device")
		},
	}

	client := newScreenCaptureClient(primary, fallback)
	_, _, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "frame service: SERVICE_RECOVERING" {
		t.Fatalf("error = %q, want primary error", got)
	}
}
