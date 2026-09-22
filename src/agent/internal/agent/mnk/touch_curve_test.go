package mnk

import (
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

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
