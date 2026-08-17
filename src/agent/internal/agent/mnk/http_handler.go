package mnk

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// HTTPHandler handles HTTP requests for MNK operations
type HTTPHandler struct {
	provider Provider
}

// NewHTTPHandler creates a new HTTP handler for MNK operations
func NewHTTPHandler(provider Provider) *HTTPHandler {
	return &HTTPHandler{
		provider: provider,
	}
}

// ServeHTTP handles HTTP requests for MNK operations
// POST /api/providers/mnk
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	defer r.Body.Close()

	// Parse request
	var req MNKRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("parse request: %v", err))
		return
	}

	// Log request (optional, can be controlled by log level)
	taskID := r.Header.Get("X-Task-ID")
	if taskID != "" {
		log.Printf("[MNK] Task %s: %s", taskID, req.Operation)
	}

	// Execute operation
	var execErr error
	switch req.Operation {
	case "click":
		execErr = h.handleClick(req.Click)
	case "double_click":
		execErr = h.handleDoubleClick(req.DoubleClick)
	case "drag":
		execErr = h.handleDrag(req.Drag)
	case "keypress":
		execErr = h.handleKeypress(req.Keypress)
	case "move":
		execErr = h.handleMove(req.Move)
	case "scroll":
		execErr = h.handleScroll(req.Scroll)
	default:
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown operation: %q", req.Operation))
		return
	}

	// Handle execution error
	if execErr != nil {
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("execute operation: %v", execErr))
		return
	}

	// Write success response
	h.writeSuccess(w)
}

func (h *HTTPHandler) handleClick(params *ClickParams) error {
	if params == nil {
		return fmt.Errorf("click params required")
	}
	return h.provider.Click(params.X, params.Y, params.Button, params.HoldMs)
}

func (h *HTTPHandler) handleDoubleClick(params *DoubleClickParams) error {
	if params == nil {
		return fmt.Errorf("double_click params required")
	}
	return h.provider.DoubleClick(params.X, params.Y, params.Button)
}

func (h *HTTPHandler) handleDrag(params *DragParams) error {
	if params == nil {
		return fmt.Errorf("drag params required")
	}
	if len(params.Path) < 2 {
		return fmt.Errorf("drag path must contain at least 2 points")
	}
	return h.provider.Drag(params.Path, params.Button)
}

func (h *HTTPHandler) handleKeypress(params *KeypressParams) error {
	if params == nil {
		return fmt.Errorf("keypress params required")
	}
	if len(params.Keys) == 0 {
		return fmt.Errorf("keys array must not be empty")
	}
	return h.provider.Keypress(params.Keys)
}

func (h *HTTPHandler) handleMove(params *MoveParams) error {
	if params == nil {
		return fmt.Errorf("move params required")
	}
	return h.provider.Move(params.X, params.Y)
}

func (h *HTTPHandler) handleScroll(params *ScrollParams) error {
	if params == nil {
		return fmt.Errorf("scroll params required")
	}
	return h.provider.Scroll(params.ScrollX, params.ScrollY)
}

func (h *HTTPHandler) writeSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(MNKResponse{
		Success: true,
	})
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(MNKErrorResponse{
		Error: message,
	})
}

// RegisterHandler registers the MNK HTTP handler with a ServeMux
func RegisterHandler(mux *http.ServeMux, provider Provider) {
	handler := NewHTTPHandler(provider)
	mux.Handle("/api/providers/mnk", handler)
}

// RegisterHandlerWithPath registers the MNK HTTP handler with a custom path
func RegisterHandlerWithPath(mux *http.ServeMux, path string, provider Provider) {
	handler := NewHTTPHandler(provider)
	mux.Handle(path, handler)
}
