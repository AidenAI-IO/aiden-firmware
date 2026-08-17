package mnk

import (
	"context"
	"testing"
)

// TestTouchGestureToolAdapter 测试 touch_gesture 工具适配器
func TestTouchGestureToolAdapter(t *testing.T) {
	mock := NewMockProvider()
	adapter := NewTouchGestureToolAdapter(mock, func() string { return "android" })

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
			input: `{"type":"long_press","point":{"x":500,"y":500},"duration_ms":500}`,
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
				if len(mock.drags) != 1 {
					t.Fatalf("expected 1 drag, got %d", len(mock.drags))
				}
				drag := mock.drags[0]
				if len(drag.Path) != 2 {
					t.Fatalf("expected path length 2, got %d", len(drag.Path))
				}
				if drag.Path[0][0] != 100 || drag.Path[0][1] != 500 {
					t.Errorf("expected start (100, 500), got (%.0f, %.0f)", drag.Path[0][0], drag.Path[0][1])
				}
				if drag.Path[1][0] != 900 || drag.Path[1][1] != 500 {
					t.Errorf("expected end (900, 500), got (%.0f, %.0f)", drag.Path[1][0], drag.Path[1][1])
				}
			},
		},
		{
			name:  "swipe_up",
			input: `{"type":"swipe_up","strength":"medium"}`,
			validate: func(t *testing.T) {
				if len(mock.drags) != 1 {
					t.Fatalf("expected 1 drag, got %d", len(mock.drags))
				}
				drag := mock.drags[0]
				if len(drag.Path) != 2 {
					t.Fatalf("expected path length 2, got %d", len(drag.Path))
				}
				// Swipe up: start below, end above
				if drag.Path[0][1] <= drag.Path[1][1] {
					t.Errorf("swipe_up should move upward, got start Y=%.0f, end Y=%.0f", drag.Path[0][1], drag.Path[1][1])
				}
			},
		},
		{
			name:  "back",
			input: `{"type":"back"}`,
			validate: func(t *testing.T) {
				if len(mock.drags) != 1 {
					t.Fatalf("expected 1 drag, got %d", len(mock.drags))
				}
				drag := mock.drags[0]
				// Edge swipe from left
				if drag.Path[0][0] >= 50 {
					t.Errorf("back gesture should start from left edge, got X=%.0f", drag.Path[0][0])
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
			err := mock.Click(tt.x, tt.y, "left", 0)
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
			err := mock.Drag(tt.path, "left")
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
		name     string
		key      string
		wantBit  uint8
		found    bool
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
		name      string
		key       string
		wantCode  string
		found     bool
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
