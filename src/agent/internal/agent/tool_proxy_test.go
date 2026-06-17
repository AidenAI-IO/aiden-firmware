package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
	server := NewServer(runtime, ":0", "")
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
		Specs:       specs,
		Action:      schema.AgentAction{Tool: toolName, ToolInput: input},
		ProxyClient: NewToolProxyClient(endpoint),
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

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
