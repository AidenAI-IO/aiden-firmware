package mnk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPProvider implements Provider interface by forwarding operations to a remote HTTP server.
// This allows remote control and cross-process communication.
type HTTPProvider struct {
	baseURL    string
	httpClient *http.Client
	taskID     string // Optional task ID for request tracking
}

// HTTPProviderConfig configuration for HTTP Provider
type HTTPProviderConfig struct {
	BaseURL string        // Base URL of the MNK HTTP server
	Timeout time.Duration // HTTP request timeout
	TaskID  string        // Optional task ID for request tracking
}

// NewHTTPProvider creates a new HTTP-based MNK provider
func NewHTTPProvider(config HTTPProviderConfig) *HTTPProvider {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	return &HTTPProvider{
		baseURL: config.BaseURL,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		taskID: config.TaskID,
	}
}

// Click performs a click operation via HTTP
func (p *HTTPProvider) Click(ctx context.Context, x, y float64, button string, holdMs int) error {
	_ = ctx
	req := MNKRequest{
		Operation: "click",
		Click: &ClickParams{
			X:      x,
			Y:      y,
			Button: button,
			HoldMs: holdMs,
		},
	}
	return p.sendRequest(req)
}

// DoubleClick performs a double-click operation via HTTP
func (p *HTTPProvider) DoubleClick(ctx context.Context, x, y float64, button string) error {
	_ = ctx
	req := MNKRequest{
		Operation: "double_click",
		DoubleClick: &DoubleClickParams{
			X:      x,
			Y:      y,
			Button: button,
		},
	}
	return p.sendRequest(req)
}

// Drag performs a drag operation via HTTP
func (p *HTTPProvider) Drag(ctx context.Context, path [][2]float64, button string) error {
	_ = ctx
	req := MNKRequest{
		Operation: "drag",
		Drag: &DragParams{
			Path:   path,
			Button: button,
		},
	}
	return p.sendRequest(req)
}

// Keypress performs a keypress operation via HTTP
func (p *HTTPProvider) Keypress(ctx context.Context, keys []string) error {
	_ = ctx
	req := MNKRequest{
		Operation: "keypress",
		Keypress: &KeypressParams{
			Keys: keys,
		},
	}
	return p.sendRequest(req)
}

// Move performs a move operation via HTTP
func (p *HTTPProvider) Move(ctx context.Context, x, y float64) error {
	_ = ctx
	req := MNKRequest{
		Operation: "move",
		Move: &MoveParams{
			X: x,
			Y: y,
		},
	}
	return p.sendRequest(req)
}

// Scroll performs a scroll operation via HTTP
func (p *HTTPProvider) Scroll(ctx context.Context, scrollX, scrollY int) error {
	_ = ctx
	req := MNKRequest{
		Operation: "scroll",
		Scroll: &ScrollParams{
			ScrollX: scrollX,
			ScrollY: scrollY,
		},
	}
	return p.sendRequest(req)
}

// sendRequest sends an MNK request to the HTTP server
func (p *HTTPProvider) sendRequest(req MNKRequest) error {
	// Marshal request to JSON
	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		p.baseURL+"/api/providers/mnk",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return fmt.Errorf("create http request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	if p.taskID != "" {
		httpReq.Header.Set("X-Task-ID", p.taskID)
	}

	// Send request
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send http request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		var errResp MNKErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("mnk operation failed: %s", errResp.Error)
		}
		return fmt.Errorf("mnk operation failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse success response
	var successResp MNKResponse
	if err := json.Unmarshal(respBody, &successResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if !successResp.Success {
		return fmt.Errorf("mnk operation failed: %s", successResp.Error)
	}

	return nil
}

// MNKRequest represents an MNK operation request
type MNKRequest struct {
	Operation   string              `json:"operation"`
	Click       *ClickParams        `json:"click,omitempty"`
	DoubleClick *DoubleClickParams  `json:"double_click,omitempty"`
	Drag        *DragParams         `json:"drag,omitempty"`
	Keypress    *KeypressParams     `json:"keypress,omitempty"`
	Move        *MoveParams         `json:"move,omitempty"`
	Scroll      *ScrollParams       `json:"scroll,omitempty"`
}

// ClickParams parameters for click operation
type ClickParams struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button string  `json:"button"`
	HoldMs int     `json:"hold_ms"`
}

// DoubleClickParams parameters for double-click operation
type DoubleClickParams struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button string  `json:"button"`
}

// DragParams parameters for drag operation
type DragParams struct {
	Path   [][2]float64 `json:"path"`
	Button string       `json:"button"`
}

// KeypressParams parameters for keypress operation
type KeypressParams struct {
	Keys []string `json:"keys"`
}

// MoveParams parameters for move operation
type MoveParams struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ScrollParams parameters for scroll operation
type ScrollParams struct {
	ScrollX int `json:"scroll_x"`
	ScrollY int `json:"scroll_y"`
}

// MNKResponse represents an MNK operation response
type MNKResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MNKErrorResponse represents an error response
type MNKErrorResponse struct {
	Error string `json:"error"`
}
