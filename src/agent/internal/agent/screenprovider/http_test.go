package screenprovider

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubProvider struct {
	meta         FrameMetadata
	data         []byte
	info         CaptureInfo
	err          error
	calls        int
	format       string
	quality      int
	cropBlack    bool
	minimalWidth int
}

func (s *stubProvider) LatestFrameWithFormat(format string, quality int, cropBlack bool, minimalWidth int) (*FrameMetadata, []byte, CaptureInfo, error) {
	s.calls++
	s.format = format
	s.quality = quality
	s.cropBlack = cropBlack
	s.minimalWidth = minimalWidth
	if s.err != nil {
		return nil, nil, CaptureInfo{}, s.err
	}
	meta := s.meta
	return &meta, append([]byte(nil), s.data...), CloneCaptureInfo(s.info), nil
}

func TestHTTPProviderFetchesFrame(t *testing.T) {
	const jpeg = "jpeg-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != Path {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get(TaskIDHeader); got != "suite:task-1" {
			t.Errorf("task header = %q, want suite:task-1", got)
		}
		var req httpRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Format != "jpeg" || req.Quality != 80 || !req.CropBlack || req.MinimalWidth != 608 {
			t.Errorf("unexpected request body: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"data": map[string]any{
				"meta": map[string]any{
					"seq":          7,
					"width":        2,
					"height":       1,
					"pixel_format": "jpeg",
					"bytes":        len(jpeg),
				},
				"capture_info": map[string]any{"capture_backend": "adb"},
				"image":        base64.StdEncoding.EncodeToString([]byte(jpeg)),
			},
		})
	}))
	defer server.Close()

	client := NewHTTP(server.URL, "suite:task-1")
	meta, data, info, err := client.LatestFrameWithFormat("jpeg", 80, true, 608)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if meta == nil || meta.Seq != 7 || meta.Width != 2 || meta.PixelFormat != "jpeg" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if string(data) != jpeg {
		t.Fatalf("image = %q, want %q", string(data), jpeg)
	}
	if info.Backend != "adb" {
		t.Fatalf("capture backend = %q, want adb", info.Backend)
	}
}

func TestHTTPProviderUsesDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 30*time.Second {
		t.Fatalf("DefaultTimeout = %s, want 30s", DefaultTimeout)
	}
	client := NewHTTP("http://127.0.0.1:9", "")
	if client.httpClient == nil || client.httpClient.Timeout != DefaultTimeout {
		t.Fatalf("http client timeout = %v, want %s", client.httpClient, DefaultTimeout)
	}
	client = NewHTTPWithClient("http://127.0.0.1:9", "", nil)
	if client.httpClient == nil || client.httpClient.Timeout != DefaultTimeout {
		t.Fatalf("nil-client timeout = %v, want %s", client.httpClient, DefaultTimeout)
	}
}

func TestHTTPProviderMapsRemoteError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "capture_failed", "message": "device offline"},
		})
	}))
	defer server.Close()

	_, _, _, err := NewHTTP(server.URL, "").LatestFrameWithFormat("jpeg", 80, false, 0)
	if err == nil || !strings.Contains(err.Error(), "device offline") {
		t.Fatalf("error = %v, want device offline", err)
	}
}

func TestHandleHTTPServesProviderFrame(t *testing.T) {
	provider := &stubProvider{
		meta: FrameMetadata{Seq: 3, Width: 4, Height: 5, PixelFormat: "jpeg", Bytes: 4},
		data: []byte("jpeg"),
		info: CaptureInfo{Backend: "frame_service"},
	}
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(`{"format":"jpeg","quality":80,"crop_black":true,"minimal_width":100}`))
	rec := httptest.NewRecorder()
	HandleHTTP(rec, req, provider)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	if provider.calls != 1 || provider.format != "jpeg" || provider.quality != 80 || !provider.cropBlack || provider.minimalWidth != 100 {
		t.Fatalf("unexpected provider call: %+v", provider)
	}
	body, _ := io.ReadAll(rec.Body)
	var parsed httpResponseBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !parsed.OK || parsed.Data.Meta.Width != 4 || parsed.Data.CaptureInfo.Backend != "frame_service" {
		t.Fatalf("unexpected response: %+v", parsed)
	}
	decoded, err := base64.StdEncoding.DecodeString(parsed.Data.Image)
	if err != nil || string(decoded) != "jpeg" {
		t.Fatalf("image = %q err=%v", string(decoded), err)
	}
}
