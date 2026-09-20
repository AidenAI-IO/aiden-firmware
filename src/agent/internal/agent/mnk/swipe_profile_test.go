package mnk

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestSwipeProfileAdapterTimingAndValidation(t *testing.T) {
	for _, tc := range []struct {
		extra    string
		duration int
	}{
		{`"profile":"decelerate"`, 600},
		{`"profile":"decelerate","duration_ms":240`, 240},
		{`"profile":"decelerate","speed":1000`, 400},
		{`"profile":"linear"`, 160},
	} {
		mock := NewMockProvider()
		_, err := NewTouchGestureToolAdapter(mock).Call(context.Background(), `{"type":"swipe","start":{"x":500,"y":800},"end":{"x":500,"y":400},`+tc.extra+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := mock.swipes[0]; got.DurationMs != tc.duration || got.HoldAfterMs != 0 || got.Profile == "" {
			t.Fatalf("%s: got %+v", tc.extra, got)
		}
	}
	mock := NewMockProvider()
	_, err := NewTouchGestureToolAdapter(mock).Call(context.Background(), `{"type":"swipe","start":{"x":500,"y":800},"end":{"x":500,"y":400},"profile":"unknown"}`)
	if err == nil || len(mock.swipes) != 0 {
		t.Fatalf("invalid profile executed: %v", err)
	}
	// ADB input swipe cannot execute this trajectory and must not ignore it.
	var adb ADBProvider
	if err := adb.SwipeWithOptions(context.Background(), nil, ButtonLeft, SwipeOptions{Profile: SwipeProfileDecelerate}); err == nil {
		t.Fatal("ADB silently accepted an unsupported profile")
	}
}

func TestDeceleratingPathNeverOvershootsAndSlowsBeforeRelease(t *testing.T) {
	last := 0.0
	for i := 0; i <= 1000; i++ {
		p := decelerateProgress(float64(i) / 1000)
		if p < last || p > 1 {
			t.Fatalf("progress %f after %f", p, last)
		}
		last = p
	}
	if last != 1 || decelerateProgress(0) != 0 {
		t.Fatal("wrong endpoints")
	}
	cruise := decelerateProgress(.2) - decelerateProgress(.1)
	tail := decelerateProgress(1) - decelerateProgress(.9)
	if tail >= cruise/50 || tail <= 0 {
		t.Fatalf("cruise=%g tail=%g", cruise, tail)
	}
	// A corner and a duplicate waypoint must not restart the speed curve.
	path := [][2]int{{0, 0}, {100, 0}, {100, 0}, {100, 300}}
	if p := pointAlongPath(path, 200); p != [2]int{100, 100} {
		t.Fatal(p)
	}
}

type motionReport struct {
	at   time.Time
	data []byte
}
type timedMotionDevice struct{ reports []motionReport }

func (d *timedMotionDevice) Write(b []byte) error {
	d.reports = append(d.reports, motionReport{time.Now(), append([]byte(nil), b...)})
	return nil
}
func (d *timedMotionDevice) Close() {}

func TestHIDDeceleratingSwipeReportsAndImmediateRelease(t *testing.T) {
	for _, touch := range []bool{true, false} {
		t.Run(fmt.Sprint(touch), func(t *testing.T) {
			device := &timedMotionDevice{}
			p := NewHIDProvider(device, nil, nil, nil, touch, "qwerty", nil)
			err := p.SwipeWithOptions(context.Background(), [][2]float64{{500, 800}, {500, 400}}, ButtonLeft, SwipeOptions{Profile: SwipeProfileDecelerate, DurationMs: 600, Steps: 2})
			if err != nil {
				t.Fatal(err)
			}
			var held []motionReport
			var release motionReport
			for _, r := range device.reports {
				if r.data[0]&1 != 0 {
					held = append(held, r)
				} else if len(held) > 0 {
					release = r
					break
				}
			}
			if len(held) < 20 {
				t.Fatalf("too few samples despite 600ms gesture: %d", len(held))
			}
			first, last := held[0], held[len(held)-1]
			if last.at.Sub(first.at) < 580*time.Millisecond {
				t.Fatal("duration not honored")
			}
			if release.at.Sub(last.at) > 30*time.Millisecond {
				t.Fatal("unexpected endpoint hold")
			}
			yOffset := 3
			if touch {
				yOffset = 4
			}
			y := func(r motionReport) int { return int(binary.LittleEndian.Uint16(r.data[yOffset : yOffset+2])) }
			_, wantY, _ := p.normalizedToAbsolute(500, 400)
			if y(last) != wantY || y(release) != wantY {
				t.Fatal("wrong release coordinate")
			}
			for i := 1; i < len(held); i++ {
				if y(held[i]) > y(held[i-1]) {
					t.Fatal("trajectory reversed")
				}
			}
			// Measure recent emitted movement, including quantization, not just
			// the analytic derivative (which alone does not constrain OS velocity).
			var tailStart motionReport
			for _, r := range held {
				if last.at.Sub(r.at) >= 100*time.Millisecond {
					tailStart = r
				}
			}
			if float64(y(tailStart)-y(last)) > .04*math.Abs(float64(y(first)-y(last))) {
				t.Fatal("release tail too fast")
			}
		})
	}
}

func TestHIDDeceleratingSwipeCancellationReleasesLastPosition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &cancelAfterWritesDevice{cancel: cancel, cancelAt: 3}
	p := NewHIDProvider(d, nil, nil, nil, true, "qwerty", nil)
	err := p.SwipeWithOptions(ctx, [][2]float64{{500, 800}, {500, 200}}, ButtonLeft, SwipeOptions{Profile: SwipeProfileDecelerate})
	if err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	reports := d.bytes()
	last := reports[12:18]
	for offset := 18; offset < len(reports); offset += 6 {
		r := reports[offset : offset+6]
		if r[0]&3 != 0 || string(r[2:]) != string(last[2:]) {
			t.Fatal("cancellation jumped or retained contact")
		}
	}
}
