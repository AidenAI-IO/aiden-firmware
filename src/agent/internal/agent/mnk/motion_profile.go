package mnk

import (
	"math"
	"strings"
)

// Shared content-scroll profile. Backends own report encoding and scheduling;
// new touch tools should use SwipeWithOptions or TouchActionProvider.TouchActions
// so they inherit this profile instead of generating their own trajectories.
const (
	defaultSwipeSmoothMotionMinMs   = 180
	defaultSwipeReleaseTailMs       = 100
	defaultSwipeReleaseTailDistance = 2.0
)

// quinticSmoothStep maps a normalized time to a trajectory that starts and
// ends with zero velocity and zero acceleration. It is used for continuous
// touch movement to reduce the velocity delivered to the OS before release.
func quinticSmoothStep(progress float64) float64 {
	if progress <= 0 {
		return 0
	}
	if progress >= 1 {
		return 1
	}
	return progress * progress * progress * (10 + progress*(-15+6*progress))
}

// controlledReleaseAllowed excludes physical-edge system gestures and explicit
// end holds. Use the contact origin, even for a multi-segment path.
func controlledReleaseAllowed(origin Point, holdAfterMs int) bool {
	return holdAfterMs == 0 && origin.X > 10 && origin.X < 990 && origin.Y > 10 && origin.Y < 990
}

// releaseTailStart reserves real movement before the exact requested endpoint.
// Coordinates are normalized, before backend-specific pixel/HID projection.
func releaseTailStart(from, to Point) Point {
	dx, dy := to.X-from.X, to.Y-from.Y
	distance := math.Hypot(dx, dy)
	if distance == 0 {
		return to
	}
	fraction := math.Min(0.5, defaultSwipeReleaseTailDistance/distance)
	return Point{X: to.X - dx*fraction, Y: to.Y - dy*fraction}
}

// withTouchReleaseTails gives a final timed content move the same real,
// low-speed approach to its endpoint as HID SwipeWithOptions. A zero slope
// at the end of the quintic alone does not reliably clear AssistiveTouch's
// retained fling velocity. Preserve edge gestures, explicit end holds,
// immediate moves and explicit touch_up endpoint changes.
func withTouchReleaseTails(actions []TouchAction) []TouchAction {
	out := make([]TouchAction, 0, len(actions))
	var current, origin *Point
	active := false
	for i, action := range actions {
		switch strings.ToLower(strings.TrimSpace(action.Type)) {
		case "touch_down":
			if action.Point != nil {
				current = action.Point
			}
			origin, active = current, true
		case "move_to":
			if active && current != nil && origin != nil && action.Point != nil && action.DurationMs > 0 && !action.releaseTail && i+1 < len(actions) {
				next := actions[i+1]
				interior := controlledReleaseAllowed(*origin, 0)
				releasing := strings.EqualFold(strings.TrimSpace(next.Type), "touch_up") && (next.Point == nil || *next.Point == *action.Point)
				if interior && releasing && *current != *action.Point {
					tailStart := releaseTailStart(*current, *action.Point)
					main := action
					main.Point = &tailStart
					main.DurationMs = max(action.DurationMs, defaultSwipeSmoothMotionMinMs)
					out = append(out, main, TouchAction{Type: "move_to", Point: action.Point, DurationMs: defaultSwipeReleaseTailMs, Button: action.Button, releaseTail: true})
					current = action.Point
					continue
				}
			}
			current = action.Point
		case "touch_up":
			active = false
			if action.Point != nil {
				current = action.Point
			}
		}
		out = append(out, action)
	}
	return out
}
