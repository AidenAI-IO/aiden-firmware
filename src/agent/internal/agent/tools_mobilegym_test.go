package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewBuiltinToolSetLegacyFactoryUsesHardwareScreenshotTool(t *testing.T) {
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tool, ok := tools.Get("screenshot")
	if !ok {
		t.Fatal("screenshot tool missing")
	}
	if _, ok := tool.(*ScreenshotTool); !ok {
		t.Fatalf("screenshot tool type = %T, want *ScreenshotTool", tool)
	}
}

func TestNewBuiltinToolSetFromConfigDefaultsToHardwareScreenshotTool(t *testing.T) {
	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}}, ProxyConfig{}, &mobileGymSessionStore{})
	tool, ok := tools.Get("screenshot")
	if !ok {
		t.Fatal("screenshot tool missing")
	}
	if _, ok := tool.(*ScreenshotTool); !ok {
		t.Fatalf("screenshot tool type = %T, want *ScreenshotTool", tool)
	}
}

func TestNewBuiltinToolSetFromConfigMobileGymUsesBridgeScreenshotTool(t *testing.T) {
	store := &mobileGymSessionStore{}
	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}, Device: DeviceConfig{Backend: "mobilegym"}}, ProxyConfig{}, store)
	tool, ok := tools.Get("screenshot")
	if !ok {
		t.Fatal("screenshot tool missing")
	}
	mt, ok := tool.(*mobileGymScreenshotTool)
	if !ok {
		t.Fatalf("screenshot tool type = %T, want *mobileGymScreenshotTool", tool)
	}
	if mt.client.sessions != store {
		t.Fatal("tool bridge client does not share supplied session store")
	}
}

func TestNewBuiltinToolSetFromConfigMobileGymExcludesUnsafeToolsByDefault(t *testing.T) {
	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}, Device: DeviceConfig{Backend: "mobilegym"}}, ProxyConfig{}, &mobileGymSessionStore{})
	for _, name := range []string{"shell", "web_scraper", "web_search", "weather", "wikipedia"} {
		if _, ok := tools.Get(name); ok {
			t.Fatalf("unsafe tool %q registered in mobilegym backend", name)
		}
	}
}

func TestNewBuiltinToolSetFromConfigMobileGymRegistersSafeInputTools(t *testing.T) {
	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}, Device: DeviceConfig{Backend: "mobilegym"}}, ProxyConfig{}, &mobileGymSessionStore{})
	for _, name := range []string{"touch_gesture", "mouse_click", "mouse_move", "mouse_scroll", "keyboard_text", "keyboard_tap", "run_script"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("safe mobilegym input tool %q missing", name)
		}
	}
}

func TestMobileGymScreenshotToolCallsBridgeWithEpisodeAndToken(t *testing.T) {
	var gotAuth string
	var gotEpisode string
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/screenshot" {
			t.Fatalf("path = %q, want /screenshot", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			EpisodeID string `json:"episode_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotEpisode = req.EpisodeID
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"width":720,"height":1280,"format":"jpeg","size":4,"data":"/9j/"}`))
	}))
	defer bridge.Close()

	store := &mobileGymSessionStore{}
	store.Set(mobileGymSession{EpisodeID: "ep1", BridgeURL: bridge.URL, BridgeToken: "device-token"})
	screen := &screenState{}
	tool := &mobileGymScreenshotTool{client: &mobileGymBridgeClient{sessions: store, http: bridge.Client()}, screen: screen}
	out, err := tool.Call(context.Background(), "")
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if gotAuth != "Bearer device-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotEpisode != "ep1" {
		t.Fatalf("episode_id = %q", gotEpisode)
	}
	if !strings.Contains(out, `"width":720`) || !strings.Contains(out, `"height":1280`) || !strings.Contains(out, `"data":"/9j/"`) {
		t.Fatalf("output = %s", out)
	}
	if width, height, ok := screen.Dimensions(); !ok || width != 720 || height != 1280 {
		t.Fatalf("screen dimensions = %dx%d ok=%v", width, height, ok)
	}
}

func TestMobileGymInputToolsCallBridge(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    string
		wantPath string
		check    func(*testing.T, map[string]any)
	}{
		{
			name:     "touch tap",
			toolName: "touch_gesture",
			input:    `{"type":"tap","point":{"x":250,"y":500}}`,
			wantPath: "/tap",
			check: func(t *testing.T, body map[string]any) {
				assertJSONInt(t, body, "x", 250)
				assertJSONInt(t, body, "y", 1000)
			},
		},
		{
			name:     "touch double tap",
			toolName: "touch_gesture",
			input:    `{"type":"double_tap","point":{"x":250,"y":500}}`,
			wantPath: "/tap",
			check: func(t *testing.T, body map[string]any) {
				assertJSONInt(t, body, "x", 250)
				assertJSONInt(t, body, "y", 1000)
				assertJSONInt(t, body, "count", 2)
			},
		},
		{
			name:     "touch long press",
			toolName: "touch_gesture",
			input:    `{"type":"long_press","point":{"x":250,"y":500},"duration_ms":777}`,
			wantPath: "/tap",
			check: func(t *testing.T, body map[string]any) {
				assertJSONInt(t, body, "x", 250)
				assertJSONInt(t, body, "y", 1000)
				assertJSONInt(t, body, "duration_ms", 777)
				if got := body["kind"]; got != "long_press" {
					t.Fatalf("kind = %#v, want long_press", got)
				}
			},
		},
		{
			name:     "touch swipe left",
			toolName: "touch_gesture",
			input:    `{"type":"swipe_left","distance":200,"anchor":500,"steps":3,"duration_ms":123}`,
			wantPath: "/swipe",
			check: func(t *testing.T, body map[string]any) {
				if startX, endX := jsonInt(body["start_x"]), jsonInt(body["end_x"]); startX <= endX {
					t.Fatalf("swipe_left start_x=%d end_x=%d, want start_x > end_x", startX, endX)
				}
				assertJSONInt(t, body, "start_y", jsonInt(body["end_y"]))
				assertJSONInt(t, body, "steps", 3)
				assertJSONInt(t, body, "duration_ms", 123)
			},
		},
		{
			name:     "touch back",
			toolName: "touch_gesture",
			input:    `{"type":"back"}`,
			wantPath: "/back",
		},
		{
			name:     "touch home",
			toolName: "touch_gesture",
			input:    `{"type":"home"}`,
			wantPath: "/home",
		},
		{
			name:     "mouse click",
			toolName: "mouse_click",
			input:    `{"x":100,"y":200,"coord_space":"pixel"}`,
			wantPath: "/tap",
			check: func(t *testing.T, body map[string]any) {
				assertJSONInt(t, body, "x", 100)
				assertJSONInt(t, body, "y", 200)
			},
		},
		{
			name:     "mouse scroll",
			toolName: "mouse_scroll",
			input:    `{"delta":-3}`,
			wantPath: "/swipe",
			check: func(t *testing.T, body map[string]any) {
				if startY, endY := jsonInt(body["start_y"]), jsonInt(body["end_y"]); startY <= endY {
					t.Fatalf("negative scroll start_y=%d end_y=%d, want finger swipe upward", startY, endY)
				}
				assertJSONInt(t, body, "start_x", jsonInt(body["end_x"]))
			},
		},
		{
			name:     "keyboard text",
			toolName: "keyboard_text",
			input:    `{"text":"hello"}`,
			wantPath: "/type_text",
			check: func(t *testing.T, body map[string]any) {
				if got := body["text"]; got != "hello" {
					t.Fatalf("text = %#v, want hello", got)
				}
			},
		},
		{
			name:     "keyboard enter",
			toolName: "keyboard_tap",
			input:    `{"keys":["enter"]}`,
			wantPath: "/key",
			check: func(t *testing.T, body map[string]any) {
				if got := body["key"]; got != "enter" {
					t.Fatalf("key = %#v, want enter", got)
				}
			},
		},
		{
			name:     "keyboard meta h home",
			toolName: "keyboard_tap",
			input:    `{"keys":["meta","h"]}`,
			wantPath: "/home",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools, calls := newMobileGymBridgeToolHarness(t, true)
			tool, ok := tools.Get(tc.toolName)
			if !ok {
				t.Fatalf("tool %q missing", tc.toolName)
			}

			out, err := tool.Call(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("Call() error = %v", err)
			}
			assertMobileGymSuccessfulOutput(t, out)

			actions := calls.ActionCalls()
			if len(actions) != 1 {
				t.Fatalf("action calls = %#v, want exactly one", actions)
			}
			call := actions[0]
			if call.path != tc.wantPath {
				t.Fatalf("path = %q, want %q", call.path, tc.wantPath)
			}
			if call.auth != "Bearer device-token" {
				t.Fatalf("Authorization = %q", call.auth)
			}
			if got := call.body["episode_id"]; got != "ep1" {
				t.Fatalf("episode_id = %#v, want ep1", got)
			}
			if tc.check != nil {
				tc.check(t, call.body)
			}
		})
	}
}

func TestMobileGymMouseMoveToolIsLogicalOnly(t *testing.T) {
	tools, calls := newMobileGymBridgeToolHarness(t, true)
	tool, ok := tools.Get("mouse_move")
	if !ok {
		t.Fatal("mouse_move tool missing")
	}

	out, err := tool.Call(context.Background(), `{"x":200,"y":800}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}
	if got := calls.All(); len(got) != 0 {
		t.Fatalf("mouse_move bridge calls = %#v, want none", got)
	}
}

func TestMobileGymTouchGestureNormalizedCoordinatesFallbackToDefaultScreenBeforeScreenshot(t *testing.T) {
	tools, calls := newMobileGymBridgeToolHarness(t, false)
	tool, ok := tools.Get("touch_gesture")
	if !ok {
		t.Fatal("touch_gesture tool missing")
	}

	out, err := tool.Call(context.Background(), `{"type":"tap","point":{"x":500,"y":250}}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	assertMobileGymSuccessfulOutput(t, out)

	actions := calls.ActionCalls()
	if len(actions) != 1 {
		t.Fatalf("action calls = %#v, want exactly one", actions)
	}
	assertJSONInt(t, actions[0].body, "x", 540)
	assertJSONInt(t, actions[0].body, "y", 600)
}

type mobileGymBridgeCall struct {
	path string
	auth string
	body map[string]any
}

type mobileGymBridgeCalls struct {
	mu    sync.Mutex
	calls []mobileGymBridgeCall
}

func (c *mobileGymBridgeCalls) Add(call mobileGymBridgeCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *mobileGymBridgeCalls) All() []mobileGymBridgeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]mobileGymBridgeCall, len(c.calls))
	copy(out, c.calls)
	return out
}

func (c *mobileGymBridgeCalls) ActionCalls() []mobileGymBridgeCall {
	all := c.All()
	actions := make([]mobileGymBridgeCall, 0, len(all))
	for _, call := range all {
		if call.path != "/screenshot" {
			actions = append(actions, call)
		}
	}
	return actions
}

func newMobileGymBridgeToolHarness(t *testing.T, seedScreen bool) (*ToolSet, *mobileGymBridgeCalls) {
	t.Helper()
	calls := &mobileGymBridgeCalls{}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		calls.Add(mobileGymBridgeCall{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/screenshot" {
			_, _ = w.Write([]byte(`{"width":1000,"height":2000,"format":"jpeg","size":4,"data":"/9j/"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"message":"ok","screenshot":{"width":1000,"height":2000,"format":"jpeg","size":4,"data":"/9j/"}}`))
	}))
	t.Cleanup(bridge.Close)

	store := &mobileGymSessionStore{}
	store.Set(mobileGymSession{EpisodeID: "ep1", BridgeURL: bridge.URL, BridgeToken: "device-token"})
	tools := NewBuiltinToolSetFromConfig(Config{Model: ModelConfig{Provider: "fake"}, Device: DeviceConfig{Backend: "mobilegym"}}, ProxyConfig{}, store)
	if seedScreen && tools.screen != nil {
		tools.screen.Update(1000, 2000)
	}
	return tools, calls
}

func assertMobileGymSuccessfulOutput(t *testing.T, out string) {
	t.Helper()
	if out == "" || strings.HasPrefix(out, "error:") {
		t.Fatalf("output = %q, want successful Aiden-compatible output", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		var result postActionScreenshotResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("output is invalid screenshot JSON: %v", err)
		}
		if result.ActionOutput != "ok" || result.Width != 1000 || result.Height != 2000 || result.Data != "/9j/" {
			t.Fatalf("unexpected post-action screenshot output: %#v", result)
		}
	}
}

func assertJSONInt(t *testing.T, body map[string]any, key string, want int) {
	t.Helper()
	if got := jsonInt(body[key]); got != want {
		t.Fatalf("%s = %d, want %d (body=%#v)", key, got, want, body)
	}
}

func jsonInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
