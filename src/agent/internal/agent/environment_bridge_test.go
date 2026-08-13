package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aiden-agent/internal/agent/statemanager"

	"github.com/BurntSushi/toml"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

// newMockEnvironmentBridge starts an httptest server that behaves like a real
// environment bridge /api/tools/{name} endpoint by running executeToolCall
// locally against the supplied tools, then serializing the response exactly as
// handleToolInvoke does.
func newMockEnvironmentBridge(t *testing.T, tools ...langtools.Tool) *httptest.Server {
	t.Helper()
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: toolMapFromSlice(tools)},
		NewSkillIndex(),
	)
	runtime.logger = newTestLogger()
	runtime.stateManager = statemanager.NewStateManager()
	server := newServerForTest(runtime)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tools/", server.handleToolInvoke)
	return httptest.NewServer(mux)
}

func toolMapFromSlice(tools []langtools.Tool) map[string]langtools.Tool {
	m := make(map[string]langtools.Tool, len(tools))
	for _, tool := range tools {
		m[tool.Name()] = tool
	}
	return m
}

// runDirect executes a tool call locally and returns the LLM-visible result.
func runDirect(t *testing.T, tool langtools.Tool, input string) ToolResult {
	t.Helper()
	specs := NewToolSpecs([]langtools.Tool{tool})
	res := executeToolCall(context.Background(), ToolCallExecution{
		Specs:  specs,
		Action: schema.AgentAction{Tool: tool.Name(), ToolInput: input},
	})
	return res.Result
}

// runViaEnvironmentBridge executes a tool call through the environment bridge
// client pointed at a mock bridge and returns the LLM-visible result.
func runViaEnvironmentBridge(t *testing.T, endpoint string, toolName, input string) ToolResult {
	t.Helper()
	// The local spec only needs a name/tool placeholder; the bridge path never
	// calls spec.Tool.Call locally.
	specs := NewToolSpecs([]langtools.Tool{&stubTool{name: toolName, description: "bridged"}})
	res := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  specs,
		Action:                 schema.AgentAction{Tool: toolName, ToolInput: input},
		EnvironmentBridge:      NewEnvironmentBridgeClient(endpoint),
		EnvironmentBridgeTools: []string{toolName}, // Explicitly forward this tool
	})
	return res.Result
}

func TestEnvironmentBridgeMatchesLocalSuccess(t *testing.T) {
	localTool := &stubTool{name: "echo", description: "Echo text.", output: "hello world"}
	remoteTool := &stubTool{name: "echo", description: "Echo text.", output: "hello world"}

	server := newMockEnvironmentBridge(t, remoteTool)
	defer server.Close()

	local := runDirect(t, localTool, "hi")
	bridged := runViaEnvironmentBridge(t, server.URL, "echo", "hi")

	if bridged.Output != local.Output {
		t.Fatalf("output mismatch: bridge=%q local=%q", bridged.Output, local.Output)
	}
	if bridged.IsError() != local.IsError() {
		t.Fatalf("is_error mismatch: bridge=%v local=%v", bridged.IsError(), local.IsError())
	}
}

func TestEnvironmentBridgeMatchesLocalToolError(t *testing.T) {
	// A tool that returns an error should produce identical LLM-visible output
	// whether run locally or via the environment bridge.
	failErr := errSentinel("boom")
	localFail := &stubTool{name: "shell", description: "Run shell.", err: failErr}
	remoteFail := &stubTool{name: "shell", description: "Run shell.", err: failErr}

	server := newMockEnvironmentBridge(t, remoteFail)
	defer server.Close()

	local := runDirect(t, localFail, `{"command":"x"}`)
	bridged := runViaEnvironmentBridge(t, server.URL, "shell", `{"command":"x"}`)

	if bridged.Output != local.Output {
		t.Fatalf("error output mismatch:\n bridge=%q\n local=%q", bridged.Output, local.Output)
	}
	if !bridged.IsError() || !local.IsError() {
		t.Fatalf("expected both to be errors: bridge=%v local=%v", bridged.IsError(), local.IsError())
	}
}

func TestEnvironmentBridgeMatchesLocalErrorLikeOutput(t *testing.T) {
	// A tool that returns non-error output that *looks* like an error should be
	// flagged IsError identically (the remote computes this and we pass it through).
	localTool := &stubTool{name: "echo", description: "Echo.", output: "error: something went wrong"}
	remoteTool := &stubTool{name: "echo", description: "Echo.", output: "error: something went wrong"}

	server := newMockEnvironmentBridge(t, remoteTool)
	defer server.Close()

	local := runDirect(t, localTool, "x")
	bridged := runViaEnvironmentBridge(t, server.URL, "echo", "x")

	if bridged.Output != local.Output {
		t.Fatalf("output mismatch: bridge=%q local=%q", bridged.Output, local.Output)
	}
	if bridged.IsError() != local.IsError() {
		t.Fatalf("is_error mismatch for error-like output: bridge=%v local=%v", bridged.IsError(), local.IsError())
	}
}

func TestEnvironmentBridgeTransportFailureIsError(t *testing.T) {
	// Point the bridge client at a dead endpoint; the call must surface as a tool error
	// in the same structured ToolResult shape as a local failure.
	bridged := runViaEnvironmentBridge(t, "http://127.0.0.1:1", "echo", "x")
	if !bridged.IsError() {
		t.Fatal("expected transport failure to be marked as error")
	}
	if bridged.Output == "" {
		t.Fatal("expected non-empty error output on transport failure")
	}
	if bridged.Error == nil || bridged.Error.Code != CodeEnvironmentBridgeTransport {
		t.Fatalf("transport Error = %+v, want environment_bridge_transport_failed", bridged.Error)
	}
}

func TestEnvironmentBridgeSendsBenchmarkTaskIDHeader(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get(BenchmarkTaskIDHeader)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output":      "ok",
			"is_error":    false,
			"duration_ms": 1,
		})
	}))
	defer server.Close()

	client := NewEnvironmentBridgeClient(server.URL, WithEnvironmentBridgeBenchmarkTaskID("clock.CountAlarms"))
	got, err := client.CallTool(context.Background(), "screenshot", "{}")
	if err != nil {
		t.Fatalf("CallTool returned error: %v", err)
	}
	if got == nil || got.IsError() {
		t.Fatalf("CallTool result = %+v", got)
	}
	if hdr := <-seen; hdr != "clock.CountAlarms" {
		t.Fatalf("%s header = %q, want %q", BenchmarkTaskIDHeader, hdr, "clock.CountAlarms")
	}
}

func TestEnvironmentBridgeCallToolReturnsStructuredToolResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Server returns a serialized ToolResult shape (post-Task 4 wire).
		_, _ = w.Write([]byte(`{"output":"hi","summary":"","error":null,"terminate":false}`))
	}))
	defer srv.Close()
	c := NewEnvironmentBridgeClient(srv.URL)
	got, err := c.CallTool(context.Background(), "screenshot", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Output != "hi" || got.IsError() {
		t.Errorf("CallTool result = %+v", got)
	}
}

func TestEnvironmentBridgeCallToolHTTPErrorIsStructuredTransientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream is down"))
	}))
	defer srv.Close()
	c := NewEnvironmentBridgeClient(srv.URL)
	got, err := c.CallTool(context.Background(), "screenshot", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.IsError() {
		t.Fatalf("expected structured error result; got %+v", got)
	}
	if got.Error.Code != CodeEnvironmentBridgeRemote {
		t.Errorf("Error.Code = %q want %q", got.Error.Code, CodeEnvironmentBridgeRemote)
	}
}

func TestShouldForwardToolWithExplicitList(t *testing.T) {
	environmentBridgeTools := []string{"screenshot", "touch_gesture"}

	if !shouldForwardToEnvironmentBridge("screenshot", environmentBridgeTools) {
		t.Error("screenshot should be forwarded when in explicit list")
	}
	if !shouldForwardToEnvironmentBridge("touch_gesture", environmentBridgeTools) {
		t.Error("touch_gesture should be forwarded when in explicit list")
	}
	if shouldForwardToEnvironmentBridge("local_utility", environmentBridgeTools) {
		t.Error("local_utility should not be forwarded when not in explicit list")
	}
	if shouldForwardToEnvironmentBridge("keyboard_tap", environmentBridgeTools) {
		t.Error("keyboard_tap should not be forwarded when not in explicit list")
	}
}

func TestShouldForwardToolEmptyListForwardsNothing(t *testing.T) {
	// An empty forward list must forward nothing; there is no hardcoded default.
	var environmentBridgeTools []string
	for _, tool := range []string{"screenshot", "keyboard_tap", "local_utility"} {
		if shouldForwardToEnvironmentBridge(tool, environmentBridgeTools) {
			t.Errorf("%s should not be forwarded when forward list is empty", tool)
		}
	}
}

func TestShouldForwardToolWildcard(t *testing.T) {
	tests := []struct {
		name          string
		patterns      []string
		toolName      string
		wantForwarded bool
	}{
		{"star matches everything", []string{"*"}, "local_utility", true},
		{"star matches device tool", []string{"*"}, "screenshot", true},
		{"prefix glob matches", []string{"keyboard_*"}, "keyboard_tap", true},
		{"prefix glob matches text", []string{"keyboard_*"}, "keyboard_text", true},
		{"prefix glob non-match", []string{"keyboard_*"}, "touch_gesture", false},
		{"suffix glob matches", []string{"*_gesture"}, "touch_gesture", true},
		{"suffix glob non-match", []string{"*_gesture"}, "mouse_scroll", false},
		{"multiple patterns first", []string{"mouse_*", "screenshot"}, "mouse_move", true},
		{"multiple patterns second", []string{"mouse_*", "screenshot"}, "screenshot", true},
		{"multiple patterns none", []string{"mouse_*", "screenshot"}, "keyboard_tap", false},
		{"exact name match", []string{"screenshot"}, "screenshot", true},
		{"exact name non-match", []string{"screenshot"}, "screensho", false},
		{"blank pattern ignored", []string{"", "screenshot"}, "screenshot", true},
		{"only blank forwards nothing", []string{"  "}, "screenshot", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldForwardToEnvironmentBridge(tc.toolName, tc.patterns); got != tc.wantForwarded {
				t.Errorf("shouldForwardToEnvironmentBridge(%q, %v) = %v, want %v", tc.toolName, tc.patterns, got, tc.wantForwarded)
			}
		})
	}
}

func TestEnvironmentBridgeForwardsByWildcard(t *testing.T) {
	// Create a mock environment bridge with keyboard_tap and a local utility.
	keyboard := &stubTool{name: "keyboard_tap", description: "Keyboard", output: "remote keyboard"}
	utility := &stubTool{name: "local_utility", description: "Utility", output: "remote utility"}
	server := newMockEnvironmentBridge(t, keyboard, utility)
	defer server.Close()

	localKeyboard := &stubTool{name: "keyboard_tap", description: "Keyboard", output: "local keyboard"}
	localUtility := &stubTool{name: "local_utility", description: "Utility", output: "local utility"}
	specs := NewToolSpecs([]langtools.Tool{localKeyboard, localUtility})

	// "keyboard_*" should forward keyboard_tap but not the local utility.
	environmentBridgeTools := []string{"keyboard_*"}

	kbRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  specs,
		Action:                 schema.AgentAction{Tool: "keyboard_tap", ToolInput: "{}"},
		EnvironmentBridge:      NewEnvironmentBridgeClient(server.URL),
		EnvironmentBridgeTools: environmentBridgeTools,
	})
	if kbRes.Result.Output != "remote keyboard" {
		t.Errorf("keyboard_tap should be forwarded by keyboard_* glob, got: %q", kbRes.Result.Output)
	}

	utilityRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  specs,
		Action:                 schema.AgentAction{Tool: "local_utility", ToolInput: "{}"},
		EnvironmentBridge:      NewEnvironmentBridgeClient(server.URL),
		EnvironmentBridgeTools: environmentBridgeTools,
	})
	if utilityRes.Result.Output != "local utility" {
		t.Errorf("local_utility should run locally with keyboard_* glob, got: %q", utilityRes.Result.Output)
	}
}

func TestEnvironmentBridgeOnlyForwardsSpecifiedTools(t *testing.T) {
	// Create a mock environment bridge with both screenshot and a local utility.
	screenshot := &stubTool{name: "screenshot", description: "Screenshot", output: "remote screenshot"}
	utility := &stubTool{name: "local_utility", description: "Utility", output: "remote utility"}
	server := newMockEnvironmentBridge(t, screenshot, utility)
	defer server.Close()

	// Create local specs for both tools with different outputs
	localScreenshot := &stubTool{name: "screenshot", description: "Screenshot", output: "local screenshot"}
	localUtility := &stubTool{name: "local_utility", description: "Utility", output: "local utility"}
	specs := NewToolSpecs([]langtools.Tool{localScreenshot, localUtility})

	// Forward only screenshot
	environmentBridgeTools := []string{"screenshot"}

	// Screenshot should be forwarded (remote output)
	screenshotRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  specs,
		Action:                 schema.AgentAction{Tool: "screenshot", ToolInput: "{}"},
		EnvironmentBridge:      NewEnvironmentBridgeClient(server.URL),
		EnvironmentBridgeTools: environmentBridgeTools,
	})
	if screenshotRes.Result.Output != "remote screenshot" {
		t.Errorf("screenshot should be forwarded, got output: %q", screenshotRes.Result.Output)
	}

	// The local utility should run locally.
	utilityRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:                  specs,
		Action:                 schema.AgentAction{Tool: "local_utility", ToolInput: "{}"},
		EnvironmentBridge:      NewEnvironmentBridgeClient(server.URL),
		EnvironmentBridgeTools: environmentBridgeTools,
	})
	if utilityRes.Result.Output != "local utility" {
		t.Errorf("local_utility should run locally, got output: %q", utilityRes.Result.Output)
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// TestEnvironmentBridgeNotReadFromConfigFile guards the invariant that environment bridge
// settings are CLI-only: a [environment_bridge] section in a TOML config file must be
// ignored, never populating Config.EnvironmentBridge, and must not cause a decode
// error. The Config.EnvironmentBridge field and every EnvironmentBridgeConfig field carry
// toml:"-" for this reason.
func TestEnvironmentBridgeNotReadFromConfigFile(t *testing.T) {
	data := `
instruction = "hi"

[environment_bridge]
enabled = true
endpoint = "http://should-not-be-read:8080"
forward_tools = ["*"]
`
	var cfg Config
	if _, err := toml.Decode(data, &cfg); err != nil {
		t.Fatalf("decode should not error even with [environment_bridge] present: %v", err)
	}
	if cfg.EnvironmentBridge.Enabled {
		t.Error("EnvironmentBridge.Enabled must stay false; config file must not set it")
	}
	if cfg.EnvironmentBridge.Endpoint != "" {
		t.Errorf("EnvironmentBridge.Endpoint must stay empty; got %q", cfg.EnvironmentBridge.Endpoint)
	}
	if len(cfg.EnvironmentBridge.Tools) != 0 {
		t.Errorf("EnvironmentBridge.Tools must stay empty; got %v", cfg.EnvironmentBridge.Tools)
	}
}
