package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

type recordingToneenPlayer struct {
	mu    sync.Mutex
	kinds []promptSoundKind
	err   error
}

func (p *recordingToneenPlayer) play(ctx context.Context, kind promptSoundKind) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kinds = append(p.kinds, kind)
	return p.err
}

func (p *recordingToneenPlayer) played() []promptSoundKind {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]promptSoundKind(nil), p.kinds...)
}

type stubCapturer struct {
	mu    sync.Mutex
	id    string
	err   error
	calls int
}

func (c *stubCapturer) Capture(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.id, c.err
}

func (c *stubCapturer) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// newTestController runs captures synchronously so assertions are deterministic.
func newTestController(capturer quickCapturer, tones *recordingToneenPlayer) *QuickCaptureController {
	c := NewQuickCaptureController(capturer, tones.play, nil)
	c.spawn = func(f func()) { f() }
	return c
}

func TestQuickCaptureControllerPlaysThresholdThenSuccess(t *testing.T) {
	tones := &recordingToneenPlayer{}
	capturer := &stubCapturer{id: "mem_1"}
	c := newTestController(capturer, tones)

	c.HandleGesture(ButtonGestureLongPress)

	if capturer.callCount() != 1 {
		t.Fatalf("capture calls = %d, want 1", capturer.callCount())
	}
	got := tones.played()
	want := []promptSoundKind{promptSoundQuickCaptureThreshold, promptSoundQuickCaptureSuccess}
	if len(got) != len(want) {
		t.Fatalf("tones = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tone[%d] = %d, want %d (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestQuickCaptureControllerPlaysFailureToneOnError(t *testing.T) {
	tones := &recordingToneenPlayer{}
	capturer := &stubCapturer{err: fmt.Errorf("network unreachable")}
	c := newTestController(capturer, tones)

	c.HandleGesture(ButtonGestureLongPress)

	got := tones.played()
	if len(got) != 2 || got[1] != promptSoundQuickCaptureFailed {
		t.Fatalf("tones = %v, want threshold then failure", got)
	}
}

func TestQuickCaptureControllerThresholdToneComesBeforeCapture(t *testing.T) {
	// The threshold tone tells the user they can let go. It must not wait for
	// the vision call, which takes seconds.
	tones := &recordingToneenPlayer{}
	var toneBeforeCapture bool
	capturer := &captureObserver{onCapture: func() {
		toneBeforeCapture = len(tones.played()) > 0
	}}
	c := newTestController(capturer, tones)

	c.HandleGesture(ButtonGestureLongPress)

	if !toneBeforeCapture {
		t.Fatal("capture started before the threshold tone; the user would not know when to release")
	}
}

func TestQuickCaptureControllerIgnoresShortPress(t *testing.T) {
	// A short press is Wakeup and is handled elsewhere; the controller must not
	// capture or make a sound.
	tones := &recordingToneenPlayer{}
	capturer := &stubCapturer{id: "mem_1"}
	c := newTestController(capturer, tones)

	c.HandleGesture(ButtonGestureShortPress)

	if capturer.callCount() != 0 {
		t.Fatalf("short press triggered %d captures, want 0", capturer.callCount())
	}
	if len(tones.played()) != 0 {
		t.Fatalf("short press played tones %v, want none", tones.played())
	}
}

func TestQuickCaptureControllerToneFailureDoesNotBlockCapture(t *testing.T) {
	// Playback is exclusive on device, so a tone is refused while the agent is
	// speaking. That must not stop the capture from being saved.
	tones := &recordingToneenPlayer{err: fmt.Errorf("SERVICE_RECOVERING")}
	capturer := &stubCapturer{id: "mem_1"}
	c := newTestController(capturer, tones)

	c.HandleGesture(ButtonGestureLongPress)

	if capturer.callCount() != 1 {
		t.Fatalf("capture calls = %d, want 1 despite tone failure", capturer.callCount())
	}
}

func TestQuickCaptureControllerNilCapturerIsSafe(t *testing.T) {
	// Quick Capture disabled or not wired: pressing the button must not panic.
	tones := &recordingToneenPlayer{}
	c := NewQuickCaptureController(nil, tones.play, nil)
	c.spawn = func(f func()) { f() }

	c.HandleGesture(ButtonGestureLongPress)

	if len(tones.played()) != 0 {
		t.Fatalf("tones played with no capturer: %v", tones.played())
	}
}

func TestQuickCaptureControllerNilReceiverIsSafe(t *testing.T) {
	var c *QuickCaptureController
	c.HandleGesture(ButtonGestureLongPress)
}

func TestQuickCaptureControllerRunsCaptureOffTheEdgeThread(t *testing.T) {
	// With the real spawn, HandleGesture must return without waiting for the
	// vision call, or the GPIO watch loop would stall and miss the next edge.
	tones := &recordingToneenPlayer{}
	release := make(chan struct{})
	done := make(chan struct{})
	capturer := &captureObserver{onCapture: func() {
		<-release
		close(done)
	}}
	c := NewQuickCaptureController(capturer, tones.play, nil)

	c.HandleGesture(ButtonGestureLongPress)
	// If HandleGesture blocked, this line would not be reached until release.
	close(release)
	<-done
}

type captureObserver struct {
	onCapture func()
}

func (c *captureObserver) Capture(ctx context.Context) (string, error) {
	if c.onCapture != nil {
		c.onCapture()
	}
	return "mem_1", nil
}
