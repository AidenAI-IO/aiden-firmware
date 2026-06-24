package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
)

// handleScreenshotJPEG captures the latest frame from frame_service and returns
// it as a raw image/jpeg response. Used by the coordinate debug page to load the
// live device screen without going through the base64 JSON screenshot tool.
func (s *Server) handleScreenshotJPEG(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	options := parseCoordinateDebugScreenshotOptions(r)
	result, jpegData, err := s.captureCoordinateDebugScreenshot(options)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Coordinate debug screenshot capture failed: %v", err)
		} else {
			log.Printf("[ERROR] Coordinate debug screenshot capture failed: %v", err)
		}
		http.Error(w, "capture failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Width", fmt.Sprintf("%d", result.Width))
	w.Header().Set("X-Frame-Height", fmt.Sprintf("%d", result.Height))
	w.Header().Set("X-Source-Width", fmt.Sprintf("%d", result.SourceWidth))
	w.Header().Set("X-Source-Height", fmt.Sprintf("%d", result.SourceHeight))
	if result.OriginalScreenWidthPixels != nil && result.OriginalScreenHeightPixels != nil {
		w.Header().Set("X-Original-Screen-Width", fmt.Sprintf("%d", *result.OriginalScreenWidthPixels))
		w.Header().Set("X-Original-Screen-Height", fmt.Sprintf("%d", *result.OriginalScreenHeightPixels))
		w.Header().Set("X-Original-Screen-Valid", "true")
	} else {
		w.Header().Set("X-Original-Screen-Valid", "false")
	}
	if result.SourceActiveArea != nil {
		w.Header().Set("X-Source-Active-X", fmt.Sprintf("%d", result.SourceActiveArea.X))
		w.Header().Set("X-Source-Active-Y", fmt.Sprintf("%d", result.SourceActiveArea.Y))
		w.Header().Set("X-Source-Active-Width", fmt.Sprintf("%d", result.SourceActiveArea.Width))
		w.Header().Set("X-Source-Active-Height", fmt.Sprintf("%d", result.SourceActiveArea.Height))
		w.Header().Set("X-Source-Active-Valid", "true")
	} else {
		w.Header().Set("X-Source-Active-Valid", "false")
	}
	w.WriteHeader(http.StatusOK)
	w.Write(jpegData)
}

// handleCoordinateDebug serves the normalized-coordinate debug page.
func (s *Server) handleCoordinateDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(coordinateDebugHTML))
}

type coordinateDebugTapRequest struct {
	X             int    `json:"x"`
	Y             int    `json:"y"`
	Type          string `json:"type"`
	CropBlackBars *bool  `json:"crop_black_bars,omitempty"`
}

type coordinateDebugTapResponse struct {
	OK         bool                                       `json:"ok"`
	Error      string                                     `json:"error,omitempty"`
	ActionType string                                     `json:"action_type,omitempty"`
	Screenshot *coordinateDebugPostActionScreenshotResult `json:"screenshot,omitempty"`
}

type coordinateDebugPostActionScreenshotResult struct {
	coordinateDebugScreenshotResult
	ActionOutput string   `json:"action_output,omitempty"`
	ScreenStable *bool    `json:"screen_stable,omitempty"`
	StableWaitMs *int64   `json:"stable_wait_ms,omitempty"`
	LastDiff     *float64 `json:"last_diff,omitempty"`
}

func writeCoordinateDebugTapError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(coordinateDebugTapResponse{
		OK:    false,
		Error: message,
	})
}

func (s *Server) handleCoordinateDebugTap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCoordinateDebugTapError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if s.runtime == nil || s.runtime.tools == nil {
		writeCoordinateDebugTapError(w, http.StatusServiceUnavailable, "runtime not configured")
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeCoordinateDebugTapError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	var req coordinateDebugTapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCoordinateDebugTapError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}
	if req.X < 0 || req.X > 1000 || req.Y < 0 || req.Y > 1000 {
		writeCoordinateDebugTapError(w, http.StatusBadRequest, "x and y must be in range 0-1000")
		return
	}

	options := coordinateDebugScreenshotOptions{CropBlackBars: true}
	if req.CropBlackBars != nil {
		options.CropBlackBars = *req.CropBlackBars
	}

	gestureType := req.Type
	switch gestureType {
	case "", "tap":
		gestureType = "tap"
	case "double_tap", "long_press":
	default:
		http.Error(w, `{"ok":false,"error":"unsupported tap type"}`, http.StatusBadRequest)
		return
	}

	tool, ok := s.runtime.tools.Get("touch_gesture")
	if !ok {
		http.Error(w, `{"ok":false,"error":"touch_gesture tool unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	screen := s.coordinateDebugScreen()
	mappingState := screenMappingState{}
	if screen != nil {
		mappingState = screen.MappingState()
		defer screen.RestoreMappingState(mappingState)
	}

	toolInput, err := json.Marshal(map[string]any{
		"type":        gestureType,
		"coord_space": "normalized",
		"point": map[string]int{
			"x": req.X,
			"y": req.Y,
		},
	})
	if err != nil {
		http.Error(w, `{"ok":false,"error":"failed to encode tap payload"}`, http.StatusInternalServerError)
		return
	}

	output, err := tool.Call(r.Context(), string(toolInput))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if toolOutputLooksLikeError(output) {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, output), http.StatusBadRequest)
		return
	}
	if screen != nil {
		screen.RestoreMappingState(mappingState)
	}

	var actionResult postActionScreenshotResult
	if err := json.Unmarshal([]byte(output), &actionResult); err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":"invalid tool output: %s"}`, err), http.StatusInternalServerError)
		return
	}
	var screenshotPayload coordinateDebugPostActionScreenshotResult
	if options.CropBlackBars && actionResult.Data != "" && actionResult.Format != "" &&
		coordinateDebugScreenshotMatchesMapping(actionResult.screenshotResult, mappingState) {
		screenshotPayload = coordinateDebugPostActionScreenshotResult{
			coordinateDebugScreenshotResult: *s.coordinateDebugScreenshotResultFromScreenState(actionResult.screenshotResult),
			ActionOutput:                    actionResult.ActionOutput,
			ScreenStable:                    actionResult.ScreenStable,
			StableWaitMs:                    actionResult.StableWaitMs,
			LastDiff:                        actionResult.LastDiff,
		}
	} else {
		screenshotResult, err := s.captureCoordinateDebugScreenshotResult(options)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"ok":false,"error":"post-action screenshot failed: %s"}`, err), http.StatusInternalServerError)
			return
		}
		screenshotPayload = coordinateDebugPostActionScreenshotResult{
			coordinateDebugScreenshotResult: *screenshotResult,
			ActionOutput:                    actionResult.ActionOutput,
			ScreenStable:                    actionResult.ScreenStable,
			StableWaitMs:                    actionResult.StableWaitMs,
			LastDiff:                        actionResult.LastDiff,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(coordinateDebugTapResponse{
		OK:         true,
		ActionType: gestureType,
		Screenshot: &screenshotPayload,
	})
}
