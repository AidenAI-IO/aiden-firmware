package agent

import (
	"encoding/json"
	"net/http"
)

// newServerQuickCapture wires the standalone capture pipeline for the manual
// HTTP trigger. It is intentionally independent from GPIO configuration: the
// GPIO wakeup path remains the legacy falling-edge path until a hardware
// trigger is chosen and verified.
func newServerQuickCapture(runtime *Runtime, frameClient screenshotFrameClient) *QuickCaptureController {
	if runtime == nil || !runtime.config.QuickCapture.EnabledOrDefault() || frameClient == nil || runtime.Model() == nil || runtime.LongTermMemoryStore() == nil {
		return nil
	}
	pipeline := NewScreenMemoryPipeline(
		frameClient,
		runtime.ScreenState(),
		runtime.Model(),
		runtime.LongTermMemoryStore(),
		ScreenMemoryOptions{TTL: runtime.config.QuickCapture.ScreenMemoryTTLOrDefault()},
	)
	return NewQuickCaptureController(pipeline, nil, runtime.Logger())
}

func (s *Server) handleQuickCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.quickCapture == nil {
		http.Error(w, "Quick Capture is not configured", http.StatusServiceUnavailable)
		return
	}

	s.quickCapture.Trigger()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":     true,
		"status": "started",
	})
}
