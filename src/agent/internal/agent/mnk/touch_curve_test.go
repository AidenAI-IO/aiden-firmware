package mnk

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestADBSwipeProfilesWholePathWithinDurationBudget(t *testing.T) {
	for _, points := range []int{2, 3, 10, 125} {
		path := make([][2]float64, points)
		for i := range path {
			path[i] = [2]float64{500, 800 - 600*float64(i)/float64(points-1)}
		}
		for _, duration := range []int{40, 180, 301} {
			t.Run(fmt.Sprintf("points=%d/duration=%d", points, duration), func(t *testing.T) {
				assertADBSwipeProgram(t, path, SwipeOptions{DurationMs: duration, HoldBeforeMs: 31}, max(duration, 180)+100+31)
			})
		}
	}
	for _, tc := range []struct {
		name         string
		path         [][2]float64
		options      SwipeOptions
		wantDuration int
	}{
		{"duplicate waypoints", [][2]float64{{500, 800}, {500, 500}, {500, 500}, {500, 200}, {500, 200}}, SwipeOptions{DurationMs: 180}, 280},
		{"tiny final segment", [][2]float64{{500, 800}, {500, 200.01}, {500, 200}}, SwipeOptions{DurationMs: 180}, 280},
		{"end hold", [][2]float64{{500, 800}, {500, 500}, {500, 200}}, SwipeOptions{DurationMs: 40, HoldBeforeMs: 31, HoldAfterMs: 43}, 114},
		{"edge origin", [][2]float64{{500, 1000}, {500, 700}, {500, 500}}, SwipeOptions{DurationMs: 40}, 40},
	} {
		t.Run(tc.name, func(t *testing.T) { assertADBSwipeProgram(t, tc.path, tc.options, tc.wantDuration) })
	}
}

// Inspect both injection scripts, including their actual sleep totals and
// DOWN/UP boundaries, rather than relying only on the planner's duration field.
func assertADBSwipeProgram(t *testing.T, path [][2]float64, options SwipeOptions, wantDuration int) {
	t.Helper()
	original := append([][2]float64(nil), path...)
	actions, err := adbSwipeActions(path, ButtonLeft, options)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(path, original) {
		t.Fatal("mutated caller path")
	}
	prepared := withTouchReleaseTails(actions)
	if len(prepared) != len(actions) {
		t.Fatal("profile applied twice")
	}
	if err := validateADBTouchActions(prepared); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []bool{false, true} {
		script, duration := adbProfileTestScript(t, prepared, raw)
		if duration != wantDuration || adbScriptSleepMillis(t, script) != wantDuration {
			t.Fatalf("raw=%t duration=%d sleeps=%d, want %d", raw, duration, adbScriptSleepMillis(t, script), wantDuration)
		}
		points, downs, ups := adbProfileTestPoints(script, raw)
		if downs != 1 || ups != 1 {
			t.Fatalf("raw=%t split contact: down=%d up=%d", raw, downs, ups)
		}
		end := path[len(path)-1]
		if len(points) == 0 || points[len(points)-1] != [2]int{int(math.Round(end[0])), int(math.Round(end[1]))} {
			t.Fatalf("raw=%t lost endpoint: %v", raw, points)
		}
	}
}

func TestADBSwipeDoesNotBrakeAtIntermediateWaypoints(t *testing.T) {
	actions, err := adbSwipeActions([][2]float64{{500, 800}, {500, 500}, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 180, Steps: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []bool{false, true} {
		script, _ := adbProfileTestScript(t, withTouchReleaseTails(actions), raw)
		points, _, _ := adbProfileTestPoints(script, raw)
		want := [][2]int{{500, 800}, {500, 725}, {500, 650}, {500, 575}, {500, 500}}
		if len(points) < 9 || !slices.Equal(points[:5], want) {
			t.Fatalf("raw=%t intermediate segment braked: %v", raw, points)
		}
		first := points[4][1] - points[5][1]
		middle := points[5][1] - points[6][1]
		last := points[7][1] - points[8][1]
		if first >= middle/2 || last >= middle/2 {
			t.Fatalf("raw=%t final segment lacks easing: %v", raw, points)
		}
	}
}

func adbProfileTestScript(t *testing.T, actions []TouchAction, raw bool) (string, int) {
	t.Helper()
	var script string
	var duration int
	var err error
	if raw {
		script, duration, err = buildADBTouchScript(adbTouchDevice{path: "/dev/input/event3", xMax: 1000, yMax: 1000, hasAbsXY: true, hasBtnTouch: true}, actions, 1)
	} else {
		script, duration, err = buildADBInputMotionScript(actions, adbInputScreenSize{1001, 1001})
	}
	if err != nil {
		t.Fatal(err)
	}
	return script, duration
}

func adbProfileTestPoints(script string, raw bool) (points [][2]int, downs, ups int) {
	script = script[strings.Index(script, " EXIT\n")+len(" EXIT\n"):]
	x, y, active := 0, 0, false
	for _, line := range strings.Split(script, "\n") {
		f := strings.Fields(line)
		if raw && len(f) == 5 && f[0] == "sendevent" {
			value, _ := strconv.Atoi(f[4])
			switch f[2] + ":" + f[3] {
			case "3:0":
				x = value
			case "3:1":
				y = value
			case "1:330":
				active = value == 1
				if active {
					downs++
				} else {
					ups++
				}
			case "0:0":
				if active {
					points = append(points, [2]int{x, y})
				}
			}
		} else if !raw && len(f) == 6 && f[0] == "input" {
			x, _ = strconv.Atoi(f[4])
			y, _ = strconv.Atoi(f[5])
			switch f[3] {
			case "DOWN":
				downs++
				points = append(points, [2]int{x, y})
			case "MOVE":
				points = append(points, [2]int{x, y})
			case "UP":
				ups++
			}
		}
	}
	return
}

func TestTouchProvidersRejectExpandedProgramBeforeInput(t *testing.T) {
	tooMany := []TouchAction{{Type: "touch_down", Point: &Point{500, 800}}}
	for len(tooMany) < 126 {
		tooMany = append(tooMany, TouchAction{Type: "wait"})
	}
	tooMany = append(tooMany, TouchAction{Type: "move_to", Point: &Point{500, 200}, DurationMs: 300}, TouchAction{Type: "touch_up"})
	for _, tc := range []struct {
		name    string
		actions []TouchAction
	}{
		{"expanded count", tooMany},
		{"expanded duration", []TouchAction{{Type: "touch_down", Point: &Point{500, 800}}, {Type: "wait", DurationMs: 30000}, {Type: "wait", DurationMs: 29999}, {Type: "move_to", Point: &Point{500, 200}, DurationMs: 1}, {Type: "touch_up"}}},
		{"movement budget", []TouchAction{{Type: "touch_down", Point: &Point{500, 800}}, {Type: "move_to", Point: &Point{500, 600}, DurationMs: 30000}, {Type: "move_to", Point: &Point{500, 400}, DurationMs: 30000}, {Type: "move_to", Point: &Point{500, 200}, DurationMs: 1}, {Type: "touch_up"}}},
		{"release hold budget", []TouchAction{{Type: "touch_down", Point: &Point{500, 800}}, {Type: "move_to", Point: &Point{500, 400}, DurationMs: 30000}, {Type: "wait", DurationMs: 30000}, {Type: "touch_up", Point: &Point{500, 200}, DurationMs: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			pointer := &layoutCaptureDevice{}
			hid := NewHIDProvider(pointer, nil, nil, nil, true, "qwerty", nil)
			err := hid.TouchActions(ctx, tc.actions)
			if got := AsError(err); got == nil || got.Kind != ErrInvalidArguments {
				t.Fatalf("HID error=%v, want invalid arguments", err)
			}
			if len(pointer.bytes()) != 0 {
				t.Fatal("HID sent input before rejecting program")
			}
			runner := &adbTestRunner{}
			adb := newTestADBProvider(t, runner)
			err = adb.TouchActions(ctx, tc.actions)
			if got := AsError(err); got == nil || got.Kind != ErrInvalidArguments {
				t.Fatalf("ADB error=%v, want invalid arguments", err)
			}
			if len(runner.commands) != 0 {
				t.Fatal("ADB ran commands before rejecting program")
			}
		})
	}
	// Exact execution limits remain valid; do not wait a minute to test them.
	actions := []TouchAction{{Type: "touch_down", Point: &Point{500, 800}}, {Type: "wait", DurationMs: 30000}, {Type: "wait", DurationMs: 29720}, {Type: "move_to", Point: &Point{500, 200}, DurationMs: 80}, {Type: "touch_up"}}
	if err := validateTouchActionLimits(withTouchReleaseTails(actions)); err != nil {
		t.Fatalf("exact 60000ms budget rejected: %v", err)
	}
	oneLess := append(append([]TouchAction(nil), tooMany[:1]...), tooMany[2:]...)
	prepared := withTouchReleaseTails(oneLess)
	if len(prepared) != 128 {
		t.Fatalf("expected exactly 128 expanded actions, got %d", len(prepared))
	}
	if err := validateTouchActionLimits(prepared); err != nil {
		t.Fatalf("128 expanded actions rejected: %v", err)
	}
}

func TestTouchReleaseTailEligibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		start, end   Point
		duration     int
		wait         bool
		releasePoint *Point
		wantTail     bool
	}{
		{"up", Point{500, 800}, Point{500, 200}, 300, false, nil, true},
		{"fast down", Point{500, 200}, Point{500, 800}, 40, false, nil, true},
		{"short diagonal", Point{500, 500}, Point{501, 501}, 300, false, nil, true},
		{"edge origin", Point{500, 1000}, Point{500, 500}, 300, false, nil, false},
		{"explicit end wait", Point{500, 800}, Point{500, 200}, 300, true, nil, false},
		{"explicit release jump", Point{500, 800}, Point{500, 200}, 300, false, &Point{500, 100}, false},
		{"same release point", Point{500, 800}, Point{500, 200}, 300, false, &Point{500, 200}, true},
		{"immediate", Point{500, 800}, Point{500, 200}, 0, false, nil, false},
		{"stationary", Point{500, 500}, Point{500, 500}, 300, false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actions := []TouchAction{{Type: "touch_down", Point: &tc.start}, {Type: "move_to", Point: &tc.end, DurationMs: tc.duration}}
			if tc.wait {
				actions = append(actions, TouchAction{Type: "wait", DurationMs: 100})
			}
			actions = append(actions, TouchAction{Type: "touch_up", Point: tc.releasePoint})
			prepared := withTouchReleaseTails(actions)
			if !tc.wantTail {
				if len(prepared) != len(actions) {
					t.Fatal("changed explicit/edge motion")
				}
				return
			}
			if len(prepared) != len(actions)+1 || !prepared[2].releaseTail || *prepared[2].Point != tc.end || prepared[2].DurationMs != 100 {
				t.Fatalf("missing moving tail: %+v", prepared)
			}
			if prepared[1].DurationMs != max(tc.duration, 180) {
				t.Fatal("incorrect main duration")
			}
			d := math.Hypot(prepared[1].Point.X-tc.end.X, prepared[1].Point.Y-tc.end.Y)
			if d <= 0 || d > 2.00001 {
				t.Fatalf("invalid tail distance: %v", d)
			}
			if *actions[1].Point != tc.end || actions[1].DurationMs != tc.duration {
				t.Fatal("mutated caller program")
			}
			if len(withTouchReleaseTails(prepared)) != len(prepared) {
				t.Fatal("duplicated release tail")
			}
			for _, raw := range []bool{false, true} {
				var script string
				var duration int
				var err error
				if raw {
					script, duration, err = buildADBTouchScript(adbTouchDevice{path: "/dev/input/event3", xMax: 32767, yMax: 32767, hasAbsXY: true, hasBtnTouch: true}, prepared, 1)
				} else {
					script, duration, err = buildADBInputMotionScript(prepared, adbInputScreenSize{1080, 2400})
				}
				if err != nil {
					t.Fatal(err)
				}
				want := max(tc.duration, 180) + 100
				if duration != want || adbScriptSleepMillis(t, script) != want {
					t.Fatalf("ADB tail timing: raw=%v duration=%d", raw, duration)
				}
			}
		})
	}
}

func adbScriptSleepMillis(t *testing.T, script string) int {
	t.Helper()
	total := 0.0
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "sleep" {
			seconds, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				t.Fatal(err)
			}
			total += seconds * 1000
		}
	}
	return int(math.Round(total))
}

func TestADBMotionBackendsAccelerateBrakeAndPreserveTiming(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, end := range []Point{{X: 500, Y: 900}, {X: 500, Y: 100}, {X: 900, Y: 500}} {
			t.Run(fmt.Sprintf("raw=%t/end=%v", raw, end), func(t *testing.T) {
				actions := []TouchAction{
					{Type: "touch_down", Point: &Point{X: 500, Y: 500}},
					{Type: "move_to", Point: &end, DurationMs: 253, steps: 7},
					{Type: "touch_up"},
				}
				var script string
				var duration int
				var err error
				if raw {
					script, duration, err = buildADBTouchScript(adbTouchDevice{path: "/dev/input/event3", xMax: 1000, yMax: 1000, hasAbsXY: true, hasBtnTouch: true}, actions, 1)
				} else {
					script, duration, err = buildADBInputMotionScript(actions, adbInputScreenSize{1001, 1001})
				}
				if err != nil {
					t.Fatal(err)
				}
				if duration != 253 || adbScriptSleepMillis(t, script) != 253 {
					t.Fatal("motion timing was rounded or shortened")
				}
				// Read the emitted events, excluding the failure-only EXIT trap.
				script = script[strings.Index(script, " EXIT\n")+len(" EXIT\n"):]
				var points [][2]int
				x, y, active := 0, 0, false
				for _, line := range strings.Split(script, "\n") {
					f := strings.Fields(line)
					if raw && len(f) == 5 && f[0] == "sendevent" {
						value, _ := strconv.Atoi(f[4])
						switch f[2] + ":" + f[3] {
						case "3:0":
							x = value
						case "3:1":
							y = value
						case "1:330":
							active = value == 1
						case "0:0":
							if active {
								points = append(points, [2]int{x, y})
							}
						}
					} else if !raw && len(f) == 6 && f[0] == "input" && (f[3] == "DOWN" || f[3] == "MOVE") {
						x, _ = strconv.Atoi(f[4])
						y, _ = strconv.Atoi(f[5])
						points = append(points, [2]int{x, y})
					}
				}
				if len(points) != 8 || points[0] != [2]int{500, 500} || points[7] != [2]int{int(end.X), int(end.Y)} {
					t.Fatalf("contact path or step count wrong: %v", points)
				}
				var distances []float64
				for i := 1; i < len(points); i++ {
					dx, dy := points[i][0]-points[i-1][0], points[i][1]-points[i-1][1]
					if float64(dx)*(end.X-500) < 0 || float64(dy)*(end.Y-500) < 0 {
						t.Fatal("motion reversed")
					}
					distances = append(distances, math.Hypot(float64(dx), float64(dy)))
				}
				if distances[0] >= distances[3]/3 || distances[6] >= distances[3]/3 {
					t.Fatalf("missing acceleration/braking: %v", distances)
				}
			})
		}
	}
}

func TestADBSwipeKeepsHoldsAndMultipleSegmentsInsideOneContact(t *testing.T) {
	actions, err := adbSwipeActions([][2]float64{{500, 800}, {500, 500}, {500, 500}, {500, 200}}, ButtonLeft, SwipeOptions{DurationMs: 301, HoldBeforeMs: 31, HoldAfterMs: 43, Steps: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 6 || actions[0].Type != "touch_down" || actions[1].Type != "wait" || actions[4].Type != "wait" || actions[5].Type != "touch_up" {
		t.Fatalf("incorrect contact boundaries: %+v", actions)
	}
	script, duration, err := buildADBInputMotionScript(actions, adbInputScreenSize{1001, 1001})
	if err != nil {
		t.Fatal(err)
	}
	if duration != 375 || adbScriptSleepMillis(t, script) != 375 {
		t.Fatalf("lost duration: %d", duration)
	}
	if strings.Count(script, "\ninput touchscreen motionevent DOWN ") != 1 || strings.Count(script, "\ninput touchscreen motionevent UP ") != 1 || strings.Count(script, "\ninput touchscreen motionevent MOVE ") != 20 {
		t.Fatal("split contact or lost custom step count")
	}
}

func TestADBProgramsReleaseContactAfterMoveFailure(t *testing.T) {
	actions := []TouchAction{{Type: "touch_down", Point: &Point{500, 800}}, {Type: "move_to", Point: &Point{500, 200}}, {Type: "touch_up"}}
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw=%t", raw), func(t *testing.T) {
			var script, stub string
			var err error
			if raw {
				script, _, err = buildADBTouchScript(adbTouchDevice{path: "/dev/input/event3", xMax: 1000, yMax: 1000, hasAbsXY: true, hasBtnTouch: true}, actions, 1)
				stub = "sendevent() { if [ \"$2 $3 $4\" = '3 1 200' ]; then return 7; fi; if [ \"$2 $3 $4\" = '1 330 0' ]; then echo released; fi; }\n"
			} else {
				script, _, err = buildADBInputMotionScript(actions, adbInputScreenSize{1001, 1001})
				stub = "input() { if [ \"$3\" = MOVE ]; then return 7; fi; if [ \"$3\" = UP ]; then echo released; fi; }\n"
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("sh", "-c", stub+script).CombinedOutput()
			if err == nil || strings.TrimSpace(string(out)) != "released" {
				t.Fatalf("failure did not release contact: %v, %s", err, out)
			}
		})
	}
}
