package screenprovider

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type httpHandlerRequest struct {
	Format       string `json:"format"`
	Quality      int    `json:"quality"`
	CropBlack    bool   `json:"crop_black"`
	MinimalWidth int    `json:"minimal_width"`
}

// HandleHTTP serves POST /api/providers/screenshot from a local Provider.
func HandleHTTP(w http.ResponseWriter, r *http.Request, provider Provider) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if provider == nil {
		writeProviderError(w, http.StatusInternalServerError, "provider_unavailable", "screen provider not configured")
		return
	}

	var req httpHandlerRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeProviderError(w, http.StatusBadRequest, "bad_request", "failed to read request body")
		return
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed != "" {
		if err := json.Unmarshal(body, &req); err != nil {
			writeProviderError(w, http.StatusBadRequest, "bad_json", "invalid JSON body")
			return
		}
	}
	req.Format = normalizeProviderFormat(req.Format)
	if req.Quality <= 0 {
		req.Quality = DefaultJPEGQuality
	}

	meta, image, info, err := provider.LatestFrameWithFormat(req.Format, req.Quality, req.CropBlack, req.MinimalWidth)
	if err != nil {
		writeProviderError(w, http.StatusInternalServerError, "capture_failed", err.Error())
		return
	}
	if meta == nil {
		writeProviderError(w, http.StatusInternalServerError, "capture_failed", "screen capture returned no metadata")
		return
	}

	payload := map[string]any{
		"ok": true,
		"data": map[string]any{
			"meta":         meta,
			"capture_info": CloneCaptureInfo(info),
			"image":        base64.StdEncoding.EncodeToString(image),
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func writeProviderError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func normalizeProviderFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		return "png"
	case "raw":
		return "raw"
	default:
		return "jpeg"
	}
}
