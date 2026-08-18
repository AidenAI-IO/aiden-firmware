package agent

import (
	"context"
	"time"
)

// quickCaptureTimeout bounds a single capture. The vision call is the slow part;
// past this the user has long since stopped waiting for a tone.
const quickCaptureTimeout = 60 * time.Second

// quickCapturer runs the screen-memory pipeline: frame → vision → store. It
// returns the id of the memory it wrote.
type quickCapturer interface {
	Capture(ctx context.Context) (string, error)
}

// quickCaptureTonePlayer plays a single feedback tone, blocking until it
// finishes so the caller controls ordering. The error is advisory: playback is
// exclusive on device and may be refused while the agent is speaking.
type quickCaptureTonePlayer func(ctx context.Context, kind promptSoundKind) error

// ButtonGesture identifies the trigger passed to the controller. GPIO gesture
// recognition is intentionally not wired into the legacy wakeup path yet; the
// manual HTTP trigger uses ButtonGestureLongPress to exercise this pipeline.
type ButtonGesture int

const (
	ButtonGestureShortPress ButtonGesture = iota + 1
	ButtonGestureLongPress
)

// QuickCaptureTonePlayer is the tone-playing capability the controller needs,
// as satisfied by *AudioDialog.
type QuickCaptureTonePlayer interface {
	PlayQuickCaptureTone(ctx context.Context, kind promptSoundKind) error
}

// QuickCaptureTones adapts a tone player for NewQuickCaptureController. It
// returns nil when player is nil or holds a nil pointer, which the controller
// treats as "capture without tones".
//
// It exists so callers outside this package can wire a tone player without
// naming promptSoundKind, which stays unexported.
func QuickCaptureTones(player QuickCaptureTonePlayer) quickCaptureTonePlayer {
	if player == nil {
		return nil
	}
	if dialog, ok := player.(*AudioDialog); ok && dialog == nil {
		return nil
	}
	return player.PlayQuickCaptureTone
}

// QuickCaptureController runs a saved Screen Memory capture.
//
// The GPIO watch loop calls HandleGesture from its edge-handling goroutine, so
// the capture runs on a separate goroutine: a blocking vision call here would
// stall the loop and make it miss the next edge.
type QuickCaptureController struct {
	capturer   quickCapturer
	tonePlayer quickCaptureTonePlayer
	logger     *Logger

	// spawn runs the capture. Tests replace it to run synchronously.
	spawn func(func())
}

// NewQuickCaptureController builds the controller.
func NewQuickCaptureController(capturer quickCapturer, tonePlayer quickCaptureTonePlayer, logger *Logger) *QuickCaptureController {
	return &QuickCaptureController{
		capturer:   capturer,
		tonePlayer: tonePlayer,
		logger:     logger,
		spawn:      func(f func()) { go f() },
	}
}

// Trigger starts one capture asynchronously. The manual HTTP endpoint uses
// this method while a future hardware trigger can call it after its gesture is
// verified.
func (c *QuickCaptureController) Trigger() {
	if c == nil || c.capturer == nil {
		return
	}
	spawn := c.spawn
	if spawn == nil {
		spawn = func(f func()) { go f() }
	}
	spawn(c.runCapture)
}

// HandleGesture keeps the gesture-facing API narrow for callers that already
// classify a trigger. Only a long press captures; a short press is Wakeup and
// is handled elsewhere.
func (c *QuickCaptureController) HandleGesture(gesture ButtonGesture) {
	if c == nil || gesture != ButtonGestureLongPress {
		return
	}
	// Quick Capture disabled or not wired: stay silent rather than promising a
	// capture with a tone and then not making one.
	if c.capturer == nil {
		return
	}

	c.Trigger()
}

// runCapture plays the threshold tone, captures, then reports the outcome.
func (c *QuickCaptureController) runCapture() {
	ctx, cancel := context.WithTimeout(context.Background(), quickCaptureTimeout)
	defer cancel()

	// This tone tells the user they can release the button, so it must come
	// before the vision call rather than after it.
	c.playTone(ctx, promptSoundQuickCaptureThreshold)

	id, err := c.capturer.Capture(ctx)
	if err != nil {
		c.logf(func(l *Logger) { l.Warn("[quick_capture] capture failed: %v", err) })
		c.playTone(ctx, promptSoundQuickCaptureFailed)
		return
	}

	c.logf(func(l *Logger) { l.Info("[quick_capture] screen memory saved: id=%s", id) })
	c.playTone(ctx, promptSoundQuickCaptureSuccess)
}

// playTone plays a tone if one can be played. A refused or failed tone is
// logged and ignored: it must never fail the capture that requested it.
func (c *QuickCaptureController) playTone(ctx context.Context, kind promptSoundKind) {
	if c.tonePlayer == nil {
		return
	}
	if err := c.tonePlayer(ctx, kind); err != nil {
		c.logf(func(l *Logger) { l.Warn("[quick_capture] %s tone not played: %v", kind, err) })
	}
}

func (c *QuickCaptureController) logf(emit func(*Logger)) {
	if c.logger == nil {
		return
	}
	emit(c.logger)
}
