package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const BenchmarkTaskIDHeader = "benchmark-task-id"

// EnvironmentBridgeClient forwards selected tool calls to an environment bridge's /api/tools endpoint.
type EnvironmentBridgeClient struct {
	endpoint        string
	benchmarkTaskID string
	httpClient      *http.Client
}

type EnvironmentBridgeClientOption func(*EnvironmentBridgeClient)

func WithEnvironmentBridgeBenchmarkTaskID(taskID string) EnvironmentBridgeClientOption {
	return func(c *EnvironmentBridgeClient) {
		c.benchmarkTaskID = strings.TrimSpace(taskID)
	}
}

// NewEnvironmentBridgeClient creates a client that forwards tool calls to the bridge endpoint.
func NewEnvironmentBridgeClient(endpoint string, opts ...EnvironmentBridgeClientOption) *EnvironmentBridgeClient {
	endpoint = strings.TrimRight(endpoint, "/")
	c := &EnvironmentBridgeClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Match typical tool execution timeout
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// CallTool forwards a tool invocation to the environment bridge and returns the result
// as a structured *ToolResult. The Go error return is reserved for context cancellation
// (Canceled / DeadlineExceeded) so callers can propagate via the executor's existing
// cancel path. All other failure modes (transport, protocol, remote HTTP non-2xx) are
// returned as a *ToolResult with Error populated and the second return value nil.
func (c *EnvironmentBridgeClient) CallTool(ctx context.Context, toolName, input string) (*ToolResult, error) {
	url := fmt.Sprintf("%s/api/tools/%s", c.endpoint, toolName)

	reqBody := map[string]interface{}{
		"input": input,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return errorToolResult(CodeEnvironmentBridgeProtocol, fmt.Sprintf("marshal request: %v", err)), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return errorToolResult(CodeEnvironmentBridgeProtocol, fmt.Sprintf("create request: %v", err)), nil
	}
	req.Header.Set("Content-Type", "application/json")
	if c.benchmarkTaskID != "" {
		req.Header.Set(BenchmarkTaskIDHeader, c.benchmarkTaskID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Surface context cancellation/deadline through the Go error slot so the
		// executor's existing propagation path runs unchanged.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return errorToolResult(CodeEnvironmentBridgeTransport, fmt.Sprintf("http call failed: %v", err)), nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return errorToolResult(CodeEnvironmentBridgeProtocol, fmt.Sprintf("read response: %v", err)), nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorToolResult(CodeEnvironmentBridgeRemote, fmt.Sprintf("remote returned HTTP %d: %s", resp.StatusCode, string(respBody))), nil
	}

	// Parse the response. The new shape carries a structured *ToolError under
	// `tool_error`; the legacy shape carries a string `error` plus `is_error`.
	// Both flow through the same struct: tool_error wins when present.
	var invokeResp struct {
		Output    string     `json:"output"`
		Summary   string     `json:"summary,omitempty"`
		ToolError *ToolError `json:"tool_error,omitempty"`
		LegacyErr string     `json:"error,omitempty"`
		IsError   bool       `json:"is_error,omitempty"`
		Terminate bool       `json:"terminate,omitempty"`
	}
	if err := json.Unmarshal(respBody, &invokeResp); err != nil {
		return errorToolResult(CodeEnvironmentBridgeProtocol, fmt.Sprintf("parse response: %v (body: %s)", err, string(respBody))), nil
	}

	result := &ToolResult{
		Output:    invokeResp.Output,
		Summary:   invokeResp.Summary,
		Terminate: invokeResp.Terminate,
		Error:     invokeResp.ToolError,
	}
	// Legacy fallback: if remote sends only is_error + error string (no tool_error),
	// synthesize a *ToolError so the LLM still sees structured failure info.
	if result.Error == nil && invokeResp.IsError {
		msg := invokeResp.LegacyErr
		if msg == "" {
			msg = invokeResp.Output
		}
		result.Error = NewToolError(CodeToolExecutionFailed, msg)
	}
	return result, nil
}

// errorToolResult constructs a *ToolResult carrying a structured ToolError where
// Output mirrors Error.Message (the spec invariant for failure shapes).
func errorToolResult(code, message string) *ToolResult {
	te := NewToolError(code, message)
	return &ToolResult{Output: te.Message, Error: te}
}
