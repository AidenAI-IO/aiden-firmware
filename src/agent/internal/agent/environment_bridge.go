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

type environmentBridgeHealth struct {
	Platform       string `json:"platform"`
	DevicePlatform string `json:"device_platform"`
	BridgeType     string `json:"bridge_type"`
}

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

// ApplyEnvironmentBridgePlatform makes bridge health the runtime platform
// authority without changing agent.toml. An explicitly configured device type
// is accepted only when it agrees with the bridge.
func ApplyEnvironmentBridgePlatform(ctx context.Context, cfg *Config) error {
	if cfg == nil || !cfg.EnvironmentBridge.Enabled {
		return nil
	}
	endpoint := strings.TrimSpace(cfg.EnvironmentBridge.Endpoint)
	if endpoint == "" {
		return fmt.Errorf("environment bridge endpoint is required")
	}
	platform, err := NewEnvironmentBridgeClient(endpoint).Platform(ctx)
	if err != nil {
		return err
	}
	deviceType := deviceTypeFromPlatform(platform)
	if deviceType == "" {
		return fmt.Errorf("environment bridge health did not report a supported platform")
	}
	if cfg.deviceTypeConfigured && cfg.DevicePlatformOrDefault() != deviceTypePlatform(deviceType) {
		return fmt.Errorf(
			"configured device type %q does not match environment bridge platform %q",
			cfg.DeviceTypeOrDefault(),
			platform,
		)
	}
	cfg.Device.DeviceType = deviceType
	cfg.HID.PointerMode = cfg.PointerModeOrDefault()
	return nil
}

// Platform reads the controlled platform from the bridge health endpoint.
func (c *EnvironmentBridgeClient) Platform(ctx context.Context) (string, error) {
	url := c.endpoint + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create environment bridge health request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("read environment bridge health: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read environment bridge health response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("environment bridge health returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	var envelope struct {
		Data *environmentBridgeHealth `json:"data"`
		environmentBridgeHealth
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("parse environment bridge health: %w", err)
	}
	health := envelope.environmentBridgeHealth
	if envelope.Data != nil {
		health = *envelope.Data
	}
	platform := strings.ToLower(strings.TrimSpace(health.Platform))
	if platform == "" {
		platform = strings.ToLower(strings.TrimSpace(health.DevicePlatform))
	}
	switch platform {
	case "ios", "iphone", "ipad", "ipados":
		return "ios", nil
	case "android":
		return "android", nil
	case "mac", "macos", "darwin":
		return "macos", nil
	case "windows", "win":
		return "windows", nil
	case "linux":
		return "linux", nil
	}
	switch strings.ToLower(strings.TrimSpace(health.BridgeType)) {
	case "adb_android", "mobilegym":
		return "android", nil
	case "vphone_ios":
		return "ios", nil
	default:
		return "", fmt.Errorf("environment bridge health did not report a supported platform")
	}
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
