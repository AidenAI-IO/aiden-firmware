package agent

import (
	"fmt"
	"log"
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

	socketPath := s.runtime.config.HID.FrameSocketOrDefault()
	client := NewFrameServiceClient(socketPath)
	meta, jpegData, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Coordinate debug screenshot capture failed: %v", err)
		} else {
			log.Printf("[ERROR] Coordinate debug screenshot capture failed: %v", err)
		}
		http.Error(w, "capture failed", http.StatusInternalServerError)
		return
	}
	if meta.PixelFormat != "jpeg" {
		http.Error(w, fmt.Sprintf("expected jpeg format, got %s", meta.PixelFormat),
			http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Width", fmt.Sprintf("%d", meta.Width))
	w.Header().Set("X-Frame-Height", fmt.Sprintf("%d", meta.Height))
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
