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

// ToolProxyClient forwards tool calls to a remote daemon's /api/tools endpoint.
type ToolProxyClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewToolProxyClient creates a client that forwards tool calls to the remote endpoint.
func NewToolProxyClient(endpoint string) *ToolProxyClient {
	endpoint = strings.TrimRight(endpoint, "/")
	return &ToolProxyClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Match typical tool execution timeout
		},
	}
}

// CallTool forwards a tool invocation to the remote daemon and returns the result.
// The response format matches local tool execution (ToolInvokeResponse from server.go).
func (c *ToolProxyClient) CallTool(ctx context.Context, toolName, input string) (output string, isError bool, err error) {
	url := fmt.Sprintf("%s/api/tools/%s", c.endpoint, toolName)

	// Prepare request body matching server.go's decodeToolInvokeInput expectations
	reqBody := map[string]interface{}{
		"input": input,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", true, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", true, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("http call failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, fmt.Errorf("read response: %w", err)
	}

	// Non-2xx status codes are transport errors
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", true, fmt.Errorf("remote returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse ToolInvokeResponse (matches server.go:1793)
	var invokeResp struct {
		Output     string `json:"output"`
		IsError    bool   `json:"is_error"`
		Error      string `json:"error,omitempty"`
		DurationMs int64  `json:"duration_ms"`
	}
	if err := json.Unmarshal(respBody, &invokeResp); err != nil {
		return "", true, fmt.Errorf("parse response: %w (body: %s)", err, string(respBody))
	}

	// Return exactly what the remote daemon returned, preserving error semantics
	return invokeResp.Output, invokeResp.IsError, nil
}
