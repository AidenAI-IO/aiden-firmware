package mnk

import (
	"context"
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
	reqCtx := r.Context()
	switch req.Operation {
	case "click":
		execErr = h.handleClick(reqCtx, req.Click)
	case "double_click":
		execErr = h.handleDoubleClick(reqCtx, req.DoubleClick)
	case "drag":
		execErr = h.handleDrag(reqCtx, req.Drag)
	case "keypress":
		execErr = h.handleKeypress(reqCtx, req.Keypress)
	case "move":
		execErr = h.handleMove(reqCtx, req.Move)
	case "scroll":
		execErr = h.handleScroll(reqCtx, req.Scroll)
	default:
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown operation: %q", req.Operation))
		return
	}

	// Handle execution error with proper status code mapping
	if execErr != nil {
		statusCode := http.StatusInternalServerError
		// Map typed errors to appropriate status codes
		if e := AsError(execErr); e != nil && (e.Kind == ErrInvalidArguments || e.Kind == ErrModuleUnavailable) {
			statusCode = http.StatusBadRequest
		}
		h.writeError(w, statusCode, fmt.Sprintf("execute operation: %v", execErr))
		return
	}

	// Write success response
	h.writeSuccess(w)
}

func (h *HTTPHandler) handleClick(ctx context.Context, params *ClickParams) error {
	if params == nil {
		return InvalidArguments("click params required")
	}
	return h.provider.Click(ctx, params.X, params.Y, params.Button, params.HoldMs)
}

func (h *HTTPHandler) handleDoubleClick(ctx context.Context, params *DoubleClickParams) error {
	if params == nil {
		return InvalidArguments("double_click params required")
	}
	return h.provider.DoubleClick(ctx, params.X, params.Y, params.Button)
}

func (h *HTTPHandler) handleDrag(ctx context.Context, params *DragParams) error {
	if params == nil {
		return InvalidArguments("drag params required")
	}
	if len(params.Path) < 2 {
		return InvalidArguments("drag path must contain at least 2 points")
	}
	return h.provider.Drag(ctx, params.Path, params.Button)
}

func (h *HTTPHandler) handleKeypress(ctx context.Context, params *KeypressParams) error {
	if params == nil {
		return InvalidArguments("keypress params required")
	}
	if len(params.Keys) == 0 {
		return InvalidArguments("keys array must not be empty")
	}
	return h.provider.Keypress(ctx, params.Keys)
}

func (h *HTTPHandler) handleMove(ctx context.Context, params *MoveParams) error {
	if params == nil {
		return InvalidArguments("move params required")
	}
	return h.provider.Move(ctx, params.X, params.Y)
}

func (h *HTTPHandler) handleScroll(ctx context.Context, params *ScrollParams) error {
	if params == nil {
		return InvalidArguments("scroll params required")
	}
	return h.provider.Scroll(ctx, params.ScrollX, params.ScrollY)
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
