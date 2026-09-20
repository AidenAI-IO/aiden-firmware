package mnk

import (
	"context"
	"math"
	"time"
)

const (
	SwipeProfileLinear          = "linear"
	SwipeProfileDecelerate      = "decelerate"
	DefaultDecelerateDurationMs = 600
)

func validateSwipeProfile(profile string) error {
	if profile != "" && profile != SwipeProfileLinear && profile != SwipeProfileDecelerate {
		return InvalidArguments("profile must be linear or decelerate")
	}
	return nil
}

func defaultSwipeDuration(profile string) int {
	if profile == SwipeProfileDecelerate {
		return DefaultDecelerateDurationMs
	}
	return defaultSwipeGestureDurationMs
}

// decelerateProgress cruises for 40% of the time, then uses a cubic tail.
// Position and velocity are continuous at the join. Both velocity and
// acceleration approach zero at release, without appending a stationary hold.
// Keep this curve in sync with MobileGym os/swipeMotion.ts.
func decelerateProgress(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	if t <= 0.4 {
		return t * 5 / 3
	}
	u := (1 - t) / 0.6
	return 1 - u*u*u/3
}

// pointAlongPath measures progress by total arc length, so a multi-segment
// path decelerates only once, near its final endpoint.
func pointAlongPath(path [][2]int, distance float64) [2]int {
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		dx, dy := float64(b[0]-a[0]), float64(b[1]-a[1])
		length := math.Hypot(dx, dy)
		if length > 0 && distance < length {
			return [2]int{a[0] + int(math.Round(dx*distance/length)), a[1] + int(math.Round(dy*distance/length))}
		}
		distance -= length
	}
	return path[len(path)-1]
}

func (p *HIDProvider) deceleratingSwipe(ctx context.Context, path [][2]int, button uint8, options SwipeOptions) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	last := path[0]
	if err = p.pressPointer(last[0], last[1], button); err != nil {
		return err
	}
	defer func() {
		// Cancellation or a failed write must not jump to the intended endpoint.
		releaseErr := p.releasePointerRepeated(last[0], last[1])
		if err == nil {
			err = releaseErr
		}
	}()
	if err = waitForContext(ctx, time.Duration(options.HoldBeforeMs)*time.Millisecond); err != nil {
		return err
	}
	steps := max(options.Steps, (options.DurationMs+15)/16)
	duration := time.Duration(options.DurationMs) * time.Millisecond
	started := time.Now()
	length := pathLength(path)
	for step := 1; step <= steps; step++ {
		deadline := started.Add(duration * time.Duration(step) / time.Duration(steps))
		if err = waitForContext(ctx, time.Until(deadline)); err != nil {
			return err
		}
		// Sample elapsed time, skipping missed deadlines instead of emitting a
		// burst of stale coordinates after a scheduler or HID write delay.
		t := math.Min(1, float64(time.Since(started))/float64(duration))
		point := pointAlongPath(path, length*decelerateProgress(t))
		if err = p.movePointer(point[0], point[1], button); err != nil {
			return err
		}
		last = point
		if t >= 1 {
			break
		}
		step = max(step, int(t*float64(steps)))
	}
	return waitForContext(ctx, time.Duration(options.HoldAfterMs)*time.Millisecond)
}
