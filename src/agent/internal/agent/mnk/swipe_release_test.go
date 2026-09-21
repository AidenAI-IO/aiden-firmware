package mnk

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

type releaseSample struct {
	at   time.Time
	data []byte
}

type releaseCaptureDevice struct{ samples []releaseSample }

func (d *releaseCaptureDevice) Write(b []byte) error {
	d.samples = append(d.samples, releaseSample{time.Now(), append([]byte(nil), b...)})
	return nil
}
func (d *releaseCaptureDevice) Close() {}

func TestSwipeReleaseHasRealSlowMotionAndExactEndpoint(t *testing.T) {
	for _, touchscreen := range []bool{false, true} {
		t.Run(fmt.Sprintf("touchscreen=%t", touchscreen), func(t *testing.T) {
			testSwipeReleaseHasRealSlowMotionAndExactEndpoint(t, touchscreen)
		})
	}
}

func testSwipeReleaseHasRealSlowMotionAndExactEndpoint(t *testing.T, touchscreen bool) {
	for _, tc := range []struct {
		name string
		path [][2]float64
	}{
		{"up", [][2]float64{{500, 800}, {500, 200}}},
		{"down", [][2]float64{{500, 200}, {500, 800}}},
		{"horizontal", [][2]float64{{800, 500}, {200, 500}}},
		{"diagonal", [][2]float64{{800, 800}, {200, 200}}},
		{"short", [][2]float64{{500, 500}, {500, 496}}},
		{"duplicate endpoint", [][2]float64{{500, 800}, {500, 200}, {500, 200}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &releaseCaptureDevice{}
			p := NewHIDProvider(d, nil, nil, nil, touchscreen, "qwerty", nil)
			err := p.SwipeWithOptions(context.Background(), tc.path, ButtonLeft, SwipeOptions{DurationMs: 40, Steps: 24})
			if err != nil {
				t.Fatal(err)
			}
			var held, releases []releaseSample
			for _, r := range d.samples {
				if r.data[0]&1 != 0 {
					if touchscreen && (r.data[0] != 0x03 || r.data[1] != 1) {
						t.Fatal("touchscreen contact lost tip/in-range flags or changed contact ID")
					}
					held = append(held, r)
				} else if len(held) > 0 {
					releases = append(releases, r)
				}
			}
			if len(held) < 10 || len(releases) != p.releaseRepeatCount {
				t.Fatal("missing release tail")
			}
			position := func(r releaseSample) (int, int) {
				offset := 1
				if touchscreen {
					offset = 2
				}
				return int(binary.LittleEndian.Uint16(r.data[offset : offset+2])), int(binary.LittleEndian.Uint16(r.data[offset+2 : offset+4]))
			}
			end := tc.path[len(tc.path)-1]
			wantX, wantY, _ := p.normalizedToAbsolute(end[0], end[1])
			x, y := position(held[len(held)-1])
			if x != wantX || y != wantY {
				t.Fatal("did not reach requested endpoint")
			}
			for _, r := range releases {
				if r.data[0] != 0 {
					t.Fatal("contact flags not cleared on release")
				}
				x, y := position(r)
				if x != wantX || y != wantY {
					t.Fatal("release moved endpoint")
				}
			}
			// Require real low-speed moves: unchanged-coordinate holds did not
			// clear fling velocity in the measured iOS AssistiveTouch trials.

			// The main movement builds speed and brakes, instead of entering or
			// leaving the fast section with a large constant-speed step.
			stepDistance := func(index int) float64 {
				a, b := position(held[index-1])
				c, d := position(held[index])
				return math.Hypot(float64(c-a), float64(d-b))
			}
			if stepDistance(12) <= stepDistance(2) || stepDistance(12) <= stepDistance(23) {
				t.Fatal("main movement lacks acceleration/braking")
			}
			tailStart := held[24]
			if duration := tailStart.at.Sub(held[0].at); duration < 180*time.Millisecond {
				t.Fatalf("main motion duration %v, want at least 180ms before the slow tail", duration)
			}
			x, y = position(tailStart)
			distance := math.Hypot(float64(wantX-x), float64(wantY-y)) * 1000 / absMouseMaxPos
			if distance <= 0 || distance > 2.1 {
				t.Fatalf("tail distance %v", distance)
			}
			if duration := held[len(held)-1].at.Sub(tailStart.at); duration < 95*time.Millisecond || duration > 250*time.Millisecond {
				t.Fatalf("tail duration %v", duration)
			}
			if gap := releases[0].at.Sub(held[len(held)-1].at); gap > 50*time.Millisecond {
				t.Fatalf("unexpected final hold %v", gap)
			}
			previousX, previousY := position(held[0])
			for _, r := range held[1:] {
				x, y := position(r)
				if (x-previousX)*(wantX-previousX) < 0 || (y-previousY)*(wantY-previousY) < 0 || math.Abs(float64(wantX-x)) > math.Abs(float64(wantX-previousX)) || math.Abs(float64(wantY-y)) > math.Abs(float64(wantY-previousY)) {
					t.Fatal("swipe reversed or overshot")
				}
				previousX, previousY = x, y
			}
		})
	}
}

func TestControlledSwipeMotionPreservesDurationAcrossSegments(t *testing.T) {
	for _, tc := range []struct {
		name       string
		durationMs int
		steps      int
	}{
		{"single step per segment", 181, 1},
		{"fractional intervals", 283, 24},
		{"sub-millisecond intervals", 181, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &releaseCaptureDevice{}
			p := NewHIDProvider(d, nil, nil, nil, true, "qwerty", nil)
			path := [][2]int{{1000, 1000}, {2000, 1000}, {2000, 1000}, {2000, 3000}, {6000, 3000}}
			started := time.Now()
			x, y, err := p.moveAlongPathWithSteps(context.Background(), path, 1, tc.durationMs, tc.steps, true)
			if err != nil {
				t.Fatal(err)
			}
			if x != 6000 || y != 3000 || len(d.samples) == 0 {
				t.Fatalf("main motion did not reach endpoint: (%d, %d)", x, y)
			}
			if elapsed := d.samples[len(d.samples)-1].at.Sub(started); elapsed < time.Duration(tc.durationMs)*time.Millisecond {
				t.Fatalf("main motion duration %v, want at least %dms", elapsed, tc.durationMs)
			}
		})
	}
}

func TestSwipeEdgeAndExplicitHoldRemainUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start [2]float64
		hold  int
		touch bool
	}{
		{"home", [2]float64{500, 999}, 0, false},
		{"back", [2]float64{1, 500}, 0, false},
		{"explicit", [2]float64{500, 800}, 20, false},
		{"touchscreen home", [2]float64{500, 999}, 0, true},
		{"touchscreen back", [2]float64{1, 500}, 0, true},
		{"touchscreen explicit", [2]float64{500, 800}, 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &releaseCaptureDevice{}
			p := NewHIDProvider(d, nil, nil, nil, tc.touch, "qwerty", nil)
			err := p.SwipeWithOptions(context.Background(), [][2]float64{tc.start, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 40, Steps: 4, HoldAfterMs: tc.hold})
			if err != nil {
				t.Fatal(err)
			}
			firstRelease := len(d.samples) - p.releaseRepeatCount
			delay := d.samples[firstRelease].at.Sub(d.samples[firstRelease-1].at)
			if delay > 80*time.Millisecond {
				t.Fatalf("unexpected internal settle: %v", delay)
			}
			if tc.hold > 0 && delay < time.Duration(tc.hold)*time.Millisecond {
				t.Fatal("explicit hold was shortened")
			}
		})
	}
}

type failOnceSwipeDevice struct {
	releaseCaptureDevice
	failAt int
	writes int
}

func (d *failOnceSwipeDevice) Write(data []byte) error {
	d.writes++
	if d.writes == d.failAt {
		return errors.New("injected write failure")
	}
	return d.releaseCaptureDevice.Write(data)
}

func TestSwipeWriteFailureReleasesLastSuccessfulPosition(t *testing.T) {
	for _, failAt := range []int{3, 8} { // during movement; during the slow tail
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			d := &failOnceSwipeDevice{failAt: failAt}
			p := NewHIDProvider(d, nil, nil, nil, false, "qwerty", nil)
			err := p.SwipeWithOptions(context.Background(), [][2]float64{{500, 800}, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 40, Steps: 4})
			if err == nil {
				t.Fatal("missing write error")
			}
			firstRelease := len(d.samples) - p.releaseRepeatCount
			lastHeld := d.samples[firstRelease-1].data
			for _, sample := range d.samples[firstRelease:] {
				if sample.data[0]&3 != 0 || string(sample.data[1:]) != string(lastHeld[1:]) {
					t.Fatal("cleanup jumped or failed to release")
				}
			}
		})
	}
}

func TestSwipeCanceledDuringSlowTailStillReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &cancelAfterWritesDevice{cancel: cancel, cancelAt: 7}
	p := NewHIDProvider(d, nil, nil, nil, false, "qwerty", nil)
	err := p.SwipeWithOptions(ctx, [][2]float64{{500, 800}, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 40, Steps: 4})
	if err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
	data := d.bytes()
	if len(data) != (7+p.releaseRepeatCount)*6 {
		t.Fatal("contact reports continued after cancellation")
	}
	for offset := 7 * 6; offset < len(data); offset += 6 {
		if data[offset]&3 != 0 || string(data[offset+1:offset+6]) != string(data[6*6+1:7*6]) {
			t.Fatal("missing endpoint cleanup")
		}
	}
}

func TestSwipeCanceledDuringMainMotionStillReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &cancelAfterWritesDevice{cancel: cancel, cancelAt: 5}
	p := NewHIDProvider(d, nil, nil, nil, false, "qwerty", nil)
	err := p.SwipeWithOptions(ctx, [][2]float64{{500, 800}, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 40, Steps: 4})
	if err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
	data := d.bytes()
	last := data[4*6 : 5*6]
	if len(data) != (5+p.releaseRepeatCount)*6 {
		t.Fatal("missing cleanup releases")
	}
	for offset := 5 * 6; offset < len(data); offset += 6 {
		r := data[offset : offset+6]
		if r[0]&3 != 0 || string(r[1:]) != string(last[1:]) {
			t.Fatal("contact not released at last position")
		}
	}
}

func TestTouchscreenSwipeCancellationReleasesLastSuccessfulPosition(t *testing.T) {
	for _, cancelAt := range []int{3, 6} { // during main movement; first slow-tail report
		t.Run(fmt.Sprint(cancelAt), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &cancelAfterWritesDevice{cancel: cancel, cancelAt: cancelAt}
			p := NewHIDProvider(d, nil, nil, nil, true, "qwerty", nil)
			err := p.SwipeWithOptions(ctx, [][2]float64{{500, 800}, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 40, Steps: 4})
			if err != context.Canceled {
				t.Fatalf("error=%v, want context.Canceled", err)
			}
			data := d.bytes()
			if len(data) != (cancelAt+p.releaseRepeatCount)*6 {
				t.Fatal("contact reports continued after cancellation")
			}
			last := data[(cancelAt-1)*6 : cancelAt*6]
			for offset := cancelAt * 6; offset < len(data); offset += 6 {
				if data[offset] != 0 || string(data[offset+1:offset+6]) != string(last[1:]) {
					t.Fatal("touchscreen cleanup changed contact ID/position or kept contact active")
				}
			}
		})
	}
}
