package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
)

// newMockRemoteDaemon starts an httptest server that behaves like a real
// daemon's /api/tools/{name} endpoint by running executeToolCall locally
// against the supplied tools, then serializing the response exactly as
// handleToolInvoke does.
func newMockRemoteDaemon(t *testing.T, tools ...langtools.Tool) *httptest.Server {
	t.Helper()
	runtime := NewRuntimeWithDeps(
		Config{Model: ModelConfig{Provider: "fake"}},
		&testModelResolver{model: &scriptedModel{}},
		NewMemoryManager(""),
		&ToolSet{tools: toolMapFromSlice(tools)},
		NewSkillIndex(),
	)
	server := NewServer(runtime, ":0")
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

// runViaProxy executes a tool call through the proxy client pointed at a mock
// remote daemon and returns the LLM-visible result.
func runViaProxy(t *testing.T, endpoint string, toolName, input string) ToolResult {
	t.Helper()
	// The proxy spec only needs a name/tool placeholder; the proxy path never
	// calls spec.Tool.Call locally.
	specs := NewToolSpecs([]langtools.Tool{&stubTool{name: toolName, description: "proxied"}})
	res := executeToolCall(context.Background(), ToolCallExecution{
		Specs:        specs,
		Action:       schema.AgentAction{Tool: toolName, ToolInput: input},
		ProxyClient:  NewToolProxyClient(endpoint),
		ForwardTools: []string{toolName}, // Explicitly forward this tool
	})
	return res.Result
}

func TestToolProxyMatchesLocalSuccess(t *testing.T) {
	localTool := &stubTool{name: "echo", description: "Echo text.", output: "hello world"}
	remoteTool := &stubTool{name: "echo", description: "Echo text.", output: "hello world"}

	server := newMockRemoteDaemon(t, remoteTool)
	defer server.Close()

	local := runDirect(t, localTool, "hi")
	proxied := runViaProxy(t, server.URL, "echo", "hi")

	if proxied.Output != local.Output {
		t.Fatalf("output mismatch: proxy=%q local=%q", proxied.Output, local.Output)
	}
	if proxied.IsError != local.IsError {
		t.Fatalf("is_error mismatch: proxy=%v local=%v", proxied.IsError, local.IsError)
	}
}

func TestToolProxyMatchesLocalToolError(t *testing.T) {
	// A tool that returns an error should produce identical LLM-visible output
	// whether run locally or via the proxy.
	failErr := errSentinel("boom")
	localFail := &stubTool{name: "shell", description: "Run shell.", err: failErr}
	remoteFail := &stubTool{name: "shell", description: "Run shell.", err: failErr}

	server := newMockRemoteDaemon(t, remoteFail)
	defer server.Close()

	local := runDirect(t, localFail, `{"command":"x"}`)
	proxied := runViaProxy(t, server.URL, "shell", `{"command":"x"}`)

	if proxied.Output != local.Output {
		t.Fatalf("error output mismatch:\n proxy=%q\n local=%q", proxied.Output, local.Output)
	}
	if !proxied.IsError || !local.IsError {
		t.Fatalf("expected both to be errors: proxy=%v local=%v", proxied.IsError, local.IsError)
	}
}

func TestToolProxyMatchesLocalErrorLikeOutput(t *testing.T) {
	// A tool that returns non-error output that *looks* like an error should be
	// flagged IsError identically (the remote computes this and we pass it through).
	localTool := &stubTool{name: "echo", description: "Echo.", output: "error: something went wrong"}
	remoteTool := &stubTool{name: "echo", description: "Echo.", output: "error: something went wrong"}

	server := newMockRemoteDaemon(t, remoteTool)
	defer server.Close()

	local := runDirect(t, localTool, "x")
	proxied := runViaProxy(t, server.URL, "echo", "x")

	if proxied.Output != local.Output {
		t.Fatalf("output mismatch: proxy=%q local=%q", proxied.Output, local.Output)
	}
	if proxied.IsError != local.IsError {
		t.Fatalf("is_error mismatch for error-like output: proxy=%v local=%v", proxied.IsError, local.IsError)
	}
}

func TestToolProxyTransportFailureIsError(t *testing.T) {
	// Point the proxy at a dead endpoint; the call must surface as a tool error
	// in the same "error: X failed" shape as a local failure.
	proxied := runViaProxy(t, "http://127.0.0.1:1", "echo", "x")
	if !proxied.IsError {
		t.Fatal("expected transport failure to be marked as error")
	}
	if proxied.Output == "" {
		t.Fatal("expected non-empty error output on transport failure")
	}
}

func TestShouldProxyToolWithExplicitList(t *testing.T) {
	forwardTools := []string{"screenshot", "touch_gesture"}

	if !shouldProxyTool("screenshot", forwardTools) {
		t.Error("screenshot should be forwarded when in explicit list")
	}
	if !shouldProxyTool("touch_gesture", forwardTools) {
		t.Error("touch_gesture should be forwarded when in explicit list")
	}
	if shouldProxyTool("calculator", forwardTools) {
		t.Error("calculator should not be forwarded when not in explicit list")
	}
	if shouldProxyTool("keyboard_tap", forwardTools) {
		t.Error("keyboard_tap should not be forwarded when not in explicit list")
	}
}

func TestShouldProxyToolEmptyListForwardsNothing(t *testing.T) {
	// An empty forward list must forward nothing; there is no hardcoded default.
	var forwardTools []string
	for _, tool := range []string{"screenshot", "keyboard_tap", "calculator"} {
		if shouldProxyTool(tool, forwardTools) {
			t.Errorf("%s should not be forwarded when forward list is empty", tool)
		}
	}
}

func TestShouldProxyToolWildcard(t *testing.T) {
	tests := []struct {
		name          string
		patterns      []string
		toolName      string
		wantForwarded bool
	}{
		{"star matches everything", []string{"*"}, "calculator", true},
		{"star matches device tool", []string{"*"}, "screenshot", true},
		{"prefix glob matches", []string{"keyboard_*"}, "keyboard_tap", true},
		{"prefix glob matches text", []string{"keyboard_*"}, "keyboard_text", true},
		{"prefix glob non-match", []string{"keyboard_*"}, "mouse_click", false},
		{"suffix glob matches", []string{"*_click"}, "mouse_click", true},
		{"suffix glob non-match", []string{"*_click"}, "mouse_scroll", false},
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
			if got := shouldProxyTool(tc.toolName, tc.patterns); got != tc.wantForwarded {
				t.Errorf("shouldProxyTool(%q, %v) = %v, want %v", tc.toolName, tc.patterns, got, tc.wantForwarded)
			}
		})
	}
}

func TestToolProxyForwardsByWildcard(t *testing.T) {
	// Create a remote daemon with keyboard_tap and calculator
	keyboard := &stubTool{name: "keyboard_tap", description: "Keyboard", output: "remote keyboard"}
	calculator := &stubTool{name: "calculator", description: "Calculator", output: "remote calc"}
	server := newMockRemoteDaemon(t, keyboard, calculator)
	defer server.Close()

	localKeyboard := &stubTool{name: "keyboard_tap", description: "Keyboard", output: "local keyboard"}
	localCalculator := &stubTool{name: "calculator", description: "Calculator", output: "local calc"}
	specs := NewToolSpecs([]langtools.Tool{localKeyboard, localCalculator})

	// "keyboard_*" should forward keyboard_tap but not calculator
	forwardTools := []string{"keyboard_*"}

	kbRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:        specs,
		Action:       schema.AgentAction{Tool: "keyboard_tap", ToolInput: "{}"},
		ProxyClient:  NewToolProxyClient(server.URL),
		ForwardTools: forwardTools,
	})
	if kbRes.Result.Output != "remote keyboard" {
		t.Errorf("keyboard_tap should be forwarded by keyboard_* glob, got: %q", kbRes.Result.Output)
	}

	calcRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:        specs,
		Action:       schema.AgentAction{Tool: "calculator", ToolInput: "1+1"},
		ProxyClient:  NewToolProxyClient(server.URL),
		ForwardTools: forwardTools,
	})
	if calcRes.Result.Output != "local calc" {
		t.Errorf("calculator should run locally with keyboard_* glob, got: %q", calcRes.Result.Output)
	}
}

func TestToolProxyOnlyForwardsSpecifiedTools(t *testing.T) {
	// Create a remote daemon with both screenshot and calculator
	screenshot := &stubTool{name: "screenshot", description: "Screenshot", output: "remote screenshot"}
	calculator := &stubTool{name: "calculator", description: "Calculator", output: "remote calc"}
	server := newMockRemoteDaemon(t, screenshot, calculator)
	defer server.Close()

	// Create local specs for both tools with different outputs
	localScreenshot := &stubTool{name: "screenshot", description: "Screenshot", output: "local screenshot"}
	localCalculator := &stubTool{name: "calculator", description: "Calculator", output: "local calc"}
	specs := NewToolSpecs([]langtools.Tool{localScreenshot, localCalculator})

	// Forward only screenshot
	forwardTools := []string{"screenshot"}

	// Screenshot should be forwarded (remote output)
	screenshotRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:        specs,
		Action:       schema.AgentAction{Tool: "screenshot", ToolInput: "{}"},
		ProxyClient:  NewToolProxyClient(server.URL),
		ForwardTools: forwardTools,
	})
	if screenshotRes.Result.Output != "remote screenshot" {
		t.Errorf("screenshot should be forwarded, got output: %q", screenshotRes.Result.Output)
	}

	// Calculator should run locally (local output)
	calcRes := executeToolCall(context.Background(), ToolCallExecution{
		Specs:        specs,
		Action:       schema.AgentAction{Tool: "calculator", ToolInput: "1+1"},
		ProxyClient:  NewToolProxyClient(server.URL),
		ForwardTools: forwardTools,
	})
	if calcRes.Result.Output != "local calc" {
		t.Errorf("calculator should run locally, got output: %q", calcRes.Result.Output)
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// TestToolProxyNotReadFromConfigFile guards the invariant that tool proxy
// settings are CLI-only: a [tool_proxy] section in a TOML config file must be
// ignored, never populating Config.ToolProxy, and must not cause a decode
// error. The Config.ToolProxy field and every ToolProxyConfig field carry
// toml:"-" for this reason.
func TestToolProxyNotReadFromConfigFile(t *testing.T) {
	data := `
instruction = "hi"

[tool_proxy]
enabled = true
endpoint = "http://should-not-be-read:8080"
forward_tools = ["*"]
`
	var cfg Config
	if _, err := toml.Decode(data, &cfg); err != nil {
		t.Fatalf("decode should not error even with [tool_proxy] present: %v", err)
	}
	if cfg.ToolProxy.Enabled {
		t.Error("ToolProxy.Enabled must stay false; config file must not set it")
	}
	if cfg.ToolProxy.Endpoint != "" {
		t.Errorf("ToolProxy.Endpoint must stay empty; got %q", cfg.ToolProxy.Endpoint)
	}
	if len(cfg.ToolProxy.ForwardTools) != 0 {
		t.Errorf("ToolProxy.ForwardTools must stay empty; got %v", cfg.ToolProxy.ForwardTools)
	}
}
