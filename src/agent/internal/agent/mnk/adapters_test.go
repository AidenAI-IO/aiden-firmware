package mnk

import (
	"context"
	"testing"
)

// TestTouchGestureToolAdapter 测试 touch_gesture 工具适配器
func TestTouchGestureToolAdapter(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewTouchGestureToolAdapter(mock)

	tests := []struct {
		name     string
		input    string
		wantErr  bool
		validate func(t *testing.T)
	}{
		{
			name:  "tap",
			input: `{"type":"tap","point":{"x":500,"y":500}}`,
			validate: func(t *testing.T) {
				if len(mock.clicks) != 1 {
					t.Fatalf("expected 1 click, got %d", len(mock.clicks))
				}
				click := mock.clicks[0]
				if click.X != 500 || click.Y != 500 {
					t.Errorf("expected (500, 500), got (%.0f, %.0f)", click.X, click.Y)
				}
				if click.HoldMs != 0 {
					t.Errorf("expected holdMs=0, got %d", click.HoldMs)
				}
			},
		},
		{
			name:  "long_press",
			input: `{"type":"long_press","point":{"x":500,"y":500},"hold_ms":500}`,
			validate: func(t *testing.T) {
				if len(mock.clicks) != 1 {
					t.Fatalf("expected 1 click, got %d", len(mock.clicks))
				}
				click := mock.clicks[0]
				if click.HoldMs != 500 {
					t.Errorf("expected holdMs=500, got %d", click.HoldMs)
				}
			},
		},
		{
			name:  "double_tap",
			input: `{"type":"double_tap","point":{"x":300,"y":400}}`,
			validate: func(t *testing.T) {
				if len(mock.doubleClicks) != 1 {
					t.Fatalf("expected 1 double click, got %d", len(mock.doubleClicks))
				}
				dc := mock.doubleClicks[0]
				if dc.X != 300 || dc.Y != 400 {
					t.Errorf("expected (300, 400), got (%.0f, %.0f)", dc.X, dc.Y)
				}
			},
		},
		{
			name:  "swipe",
			input: `{"type":"swipe","start":{"x":100,"y":500},"end":{"x":900,"y":500}}`,
			validate: func(t *testing.T) {
				if len(mock.swipes) != 1 {
					t.Fatalf("expected 1 swipe, got %d", len(mock.swipes))
				}
				swipe := mock.swipes[0]
				if len(swipe.Path) != 2 {
					t.Fatalf("expected path length 2, got %d", len(swipe.Path))
				}
				if swipe.Path[0][0] != 100 || swipe.Path[0][1] != 500 {
					t.Errorf("expected start (100, 500), got (%.0f, %.0f)", swipe.Path[0][0], swipe.Path[0][1])
				}
				if swipe.Path[1][0] != 900 || swipe.Path[1][1] != 500 {
					t.Errorf("expected end (900, 500), got (%.0f, %.0f)", swipe.Path[1][0], swipe.Path[1][1])
				}
				if swipe.DurationMs != 320 {
					t.Errorf("expected duration 320ms at default speed, got %d", swipe.DurationMs)
				}
			},
		},
		{
			name:  "swipe_direction",
			input: `{"type":"swipe","start":{"x":500,"y":800},"direction":"up","speed":2500,"duration_ms":300}`,
			validate: func(t *testing.T) {
				if len(mock.swipes) != 1 {
					t.Fatalf("expected 1 swipe, got %d", len(mock.swipes))
				}
				swipe := mock.swipes[0]
				if swipe.Path[0] != [2]float64{500, 800} || swipe.Path[1] != [2]float64{500, 50} {
					t.Errorf("swipe path = %#v, want [[500 800] [500 50]]", swipe.Path)
				}
				if swipe.DurationMs != 300 {
					t.Errorf("duration = %d, want 300", swipe.DurationMs)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.Reset()
			result, err := adapter.Call(context.Background(), tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Call() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result != "ok" {
				t.Errorf("Call() result = %q, want %q", result, "ok")
			}
			if tt.validate != nil {
				tt.validate(t)
			}
		})
	}
}

func TestTouchGestureToolAdapterIgnoresUnknownFields(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewTouchGestureToolAdapter(mock)

	result, err := adapter.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":500,"coord_space":"pixel"},"duration_ms":1}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result != "ok" || len(mock.clicks) != 1 {
		t.Fatalf("Call() = %q, clicks=%d; want ok and one click", result, len(mock.clicks))
	}
}

func TestTouchGestureToolAdapterRejectsZeroDistancePaths(t *testing.T) {
	for _, gestureType := range []string{"swipe", "drag"} {
		t.Run(gestureType, func(t *testing.T) {
			mock := NewMockProvider()
			adapter := NewTouchGestureToolAdapter(mock)
			_, err := adapter.Call(context.Background(), `{"type":"`+gestureType+`","start":{"x":500,"y":500},"end":{"x":500,"y":500}}`)
			if got := AsError(err); got == nil || got.Kind != ErrInvalidArguments {
				t.Fatalf("Call() error = %v, want invalid arguments", err)
			}
			if len(mock.drags) != 0 || len(mock.swipes) != 0 {
				t.Fatalf("drags = %d, swipes = %d; want none", len(mock.drags), len(mock.swipes))
			}
		})
	}
}

func TestTouchGestureToolAdapterSwipeForms(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewTouchGestureToolAdapter(mock)
	_, err := adapter.Call(context.Background(), `{"type":"swipe","start":{"x":900,"y":125},"direction":"left","speed":2000,"duration_ms":400}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(mock.swipes) != 1 {
		t.Fatalf("swipes = %d, want 1", len(mock.swipes))
	}
	path := mock.swipes[0].Path
	if path[0] != [2]float64{900, 125} || path[1] != [2]float64{100, 125} {
		t.Fatalf("path = %#v, want [[900 125] [100 125]]", path)
	}
	if mock.swipes[0].DurationMs != 400 {
		t.Fatalf("duration = %d, want 400", mock.swipes[0].DurationMs)
	}

	for _, input := range []string{
		`{"type":"swipe","start":{"x":500,"y":500}}`,
		`{"type":"swipe","start":{"x":500,"y":500},"end":{"x":500,"y":100},"direction":"up"}`,
		`{"type":"swipe","start":{"x":500,"y":500},"direction":"diagonal"}`,
		`{"type":"swipe","start":{"x":500,"y":500},"direction":"up","speed":0}`,
		`{"type":"swipe","start":{"x":500,"y":500},"direction":"up","speed":0.01}`,
		`{"type":"swipe","start":{"x":500,"y":500},"direction":"up","duration_ms":0}`,
		`{"type":"swipe_up","start":{"x":500,"y":800}}`,
		`{"type":"back"}`,
		`{"type":"home"}`,
	} {
		mock.Reset()
		if _, err := adapter.Call(context.Background(), input); AsError(err) == nil || AsError(err).Kind != ErrInvalidArguments {
			t.Fatalf("Call(%s) error = %v, want invalid arguments", input, err)
		}
		if len(mock.swipes) != 0 {
			t.Fatalf("Call(%s) recorded %d swipes, want none", input, len(mock.swipes))
		}
	}
}

func TestTouchGestureToolAdapterSeparatesSwipeFromDrag(t *testing.T) {
	provider := NewMockProvider()
	adapter := NewTouchGestureToolAdapter(provider)

	if _, err := adapter.Call(context.Background(), `{"type":"swipe","start":{"x":700,"y":500},"direction":"left","duration_ms":160}`); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(provider.swipes) != 1 {
		t.Fatalf("swipes = %d, want 1", len(provider.swipes))
	}
	path := provider.swipes[0].Path
	if path[0] != [2]float64{700, 500} || path[1] != [2]float64{300, 500} {
		t.Fatalf("path = %#v, want [[700 500] [300 500]]", path)
	}

	if _, err := adapter.Call(context.Background(), `{"type":"swipe","start":{"x":700,"y":500},"end":{"x":300,"y":500}}`); err != nil {
		t.Fatalf("explicit swipe Call() error = %v", err)
	}
	if len(provider.swipes) != 2 {
		t.Fatalf("swipes after explicit swipe = %d, want 2", len(provider.swipes))
	}
	if _, err := adapter.Call(context.Background(), `{"type":"drag","start":{"x":700,"y":500},"end":{"x":300,"y":500}}`); err != nil {
		t.Fatalf("drag Call() error = %v", err)
	}
	if len(provider.drags) != 1 {
		t.Fatalf("drags = %d, want 1", len(provider.drags))
	}
	if len(provider.swipes) != 2 {
		t.Fatalf("swipes after drag = %d, want 2", len(provider.swipes))
	}
}

// TestKeyboardTapToolAdapter 测试 keyboard_tap 工具适配器
func TestKeyboardTapToolAdapter(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewKeyboardTapToolAdapter(mock)

	tests := []struct {
		name     string
		input    string
		wantErr  bool
		validate func(t *testing.T)
	}{
		{
			name:  "single_key",
			input: `{"keys":["enter"]}`,
			validate: func(t *testing.T) {
				if len(mock.keypresses) != 1 {
					t.Fatalf("expected 1 keypress, got %d", len(mock.keypresses))
				}
				kp := mock.keypresses[0]
				if len(kp.Keys) != 1 || kp.Keys[0] != "enter" {
					t.Errorf("expected [enter], got %v", kp.Keys)
				}
			},
		},
		{
			name:  "modifier_chord",
			input: `{"keys":["ctrl","a"]}`,
			validate: func(t *testing.T) {
				if len(mock.keypresses) != 1 {
					t.Fatalf("expected 1 keypress, got %d", len(mock.keypresses))
				}
				kp := mock.keypresses[0]
				if len(kp.Keys) != 2 || kp.Keys[0] != "ctrl" || kp.Keys[1] != "a" {
					t.Errorf("expected [ctrl, a], got %v", kp.Keys)
				}
			},
		},
		{
			name:  "complex_chord",
			input: `{"keys":["ctrl","shift","t"]}`,
			validate: func(t *testing.T) {
				if len(mock.keypresses) != 1 {
					t.Fatalf("expected 1 keypress, got %d", len(mock.keypresses))
				}
				kp := mock.keypresses[0]
				if len(kp.Keys) != 3 {
					t.Errorf("expected 3 keys, got %d", len(kp.Keys))
				}
			},
		},
		{
			name:    "empty_keys",
			input:   `{"keys":[]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.Reset()
			result, err := adapter.Call(context.Background(), tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Call() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result != "ok" {
				t.Errorf("Call() result = %q, want %q", result, "ok")
			}
			if tt.validate != nil {
				tt.validate(t)
			}
		})
	}
}

// TestMouseMoveToolAdapter 测试 mouse_move 工具适配器
func TestMouseMoveToolAdapter(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewMouseMoveToolAdapter(mock)

	result, err := adapter.Call(context.Background(), `{"x":250,"y":750}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result != "ok" {
		t.Errorf("Call() result = %q, want %q", result, "ok")
	}

	if len(mock.moves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(mock.moves))
	}
	move := mock.moves[0]
	if move.X != 250 || move.Y != 750 {
		t.Errorf("expected (250, 750), got (%.0f, %.0f)", move.X, move.Y)
	}
}

// TestMouseScrollToolAdapter 测试 mouse_scroll 工具适配器
func TestMouseScrollToolAdapter(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewMouseScrollToolAdapter(mock)

	result, err := adapter.Call(context.Background(), `{"delta":-3}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result != "ok" {
		t.Errorf("Call() result = %q, want %q", result, "ok")
	}

	if len(mock.scrolls) != 1 {
		t.Fatalf("expected 1 scroll, got %d", len(mock.scrolls))
	}
	scroll := mock.scrolls[0]
	if scroll.ScrollX != 0 || scroll.ScrollY != -3 {
		t.Errorf("expected (0, -3), got (%d, %d)", scroll.ScrollX, scroll.ScrollY)
	}
}

// TestCoordinateValidation 测试坐标验证
func TestCoordinateValidation(t *testing.T) {
	mock := NewMockProvider()

	tests := []struct {
		name    string
		x, y    float64
		wantErr bool
	}{
		{"valid_center", 500, 500, false},
		{"valid_top_left", 0, 0, false},
		{"valid_bottom_right", 1000, 1000, false},
		{"invalid_x_negative", -1, 500, false}, // Mock doesn't validate
		{"invalid_x_too_large", 1001, 500, false},
		{"invalid_y_negative", 500, -1, false},
		{"invalid_y_too_large", 500, 1001, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mock.Click(context.Background(), tt.x, tt.y, "left", 0)
			if (err != nil) != tt.wantErr {
				t.Errorf("Click(%.1f, %.1f) error = %v, wantErr %v", tt.x, tt.y, err, tt.wantErr)
			}
		})
	}
}

// TestPathValidation 测试路径验证
func TestPathValidation(t *testing.T) {
	mock := NewMockProvider()

	tests := []struct {
		name    string
		path    [][2]float64
		wantErr bool
	}{
		{
			name:    "valid_2_point",
			path:    [][2]float64{{100, 500}, {900, 500}},
			wantErr: false,
		},
		{
			name:    "valid_3_point",
			path:    [][2]float64{{100, 500}, {500, 300}, {900, 500}},
			wantErr: false,
		},
		{
			name:    "valid_4_point_curve",
			path:    [][2]float64{{100, 500}, {300, 300}, {700, 300}, {900, 500}},
			wantErr: false,
		},
		{
			name:    "empty_path",
			path:    [][2]float64{},
			wantErr: false, // Mock doesn't validate
		},
		{
			name:    "single_point",
			path:    [][2]float64{{500, 500}},
			wantErr: false, // Mock doesn't validate
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mock.Drag(context.Background(), tt.path, "left")
			if (err != nil) != tt.wantErr {
				t.Errorf("Drag() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestKeyboardMapping 测试键盘映射
func TestKeyboardMapping(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantCode uint8
		found    bool
	}{
		{"letter_a", "a", 0x04, true},
		{"letter_z", "z", 0x1d, true},
		{"number_0", "0", 0x27, true},
		{"number_9", "9", 0x26, true},
		{"enter", "enter", 0x28, true},
		{"escape", "escape", 0x29, true},
		{"space", "space", 0x2c, true},
		{"up_arrow", "up", 0x52, true},
		{"f1", "f1", 0x3a, true},
		{"f12", "f12", 0x45, true},
		{"unknown", "unknown_key", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, found := hidKeyboardMap[tt.key]
			if found != tt.found {
				t.Errorf("hidKeyboardMap[%q] found = %v, want %v", tt.key, found, tt.found)
			}
			if found && code != tt.wantCode {
				t.Errorf("hidKeyboardMap[%q] = 0x%02x, want 0x%02x", tt.key, code, tt.wantCode)
			}
		})
	}
}

// TestModifierMapping 测试修饰键映射
func TestModifierMapping(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantBit uint8
		found   bool
	}{
		{"ctrl", "ctrl", 0x01, true},
		{"shift", "shift", 0x02, true},
		{"alt", "alt", 0x04, true},
		{"meta", "meta", 0x08, true},
		{"lctrl", "lctrl", 0x01, true},
		{"rctrl", "rctrl", 0x10, true},
		{"lshift", "lshift", 0x02, true},
		{"rshift", "rshift", 0x20, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bit, found := hidModifierMap[tt.key]
			if found != tt.found {
				t.Errorf("hidModifierMap[%q] found = %v, want %v", tt.key, found, tt.found)
			}
			if found && bit != tt.wantBit {
				t.Errorf("hidModifierMap[%q] = 0x%02x, want 0x%02x", tt.key, bit, tt.wantBit)
			}
		})
	}
}

// TestADBKeycodeMapping 测试 ADB keycode 映射
func TestADBKeycodeMapping(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantCode string
		found    bool
	}{
		{"letter_a", "a", "KEYCODE_A", true},
		{"android_back", "android_back", "KEYCODE_BACK", true},
		{"android_home", "android_home", "KEYCODE_HOME", true},
		{"volume_up", "volume_up", "KEYCODE_VOLUME_UP", true},
		{"enter", "enter", "KEYCODE_ENTER", true},
		{"ctrl", "ctrl", "KEYCODE_CTRL_LEFT", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 先检查别名
			code, found := adbAndroidKeycodeAliases[tt.key]
			if !found {
				// 再检查标准映射
				code, found = adbKeyboardKeycodeMap[tt.key]
			}
			if found != tt.found {
				t.Errorf("adb keycode for %q found = %v, want %v", tt.key, found, tt.found)
			}
			if found && code != tt.wantCode {
				t.Errorf("adb keycode for %q = %q, want %q", tt.key, code, tt.wantCode)
			}
		})
	}
}
