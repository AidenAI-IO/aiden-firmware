package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type quickCaptureHTTPFakeCapturer struct {
	calls int
}

func (f *quickCaptureHTTPFakeCapturer) Capture(context.Context) (string, error) {
	f.calls++
	return "mem_http_test", nil
}

func TestQuickCaptureHTTPTriggerStartsCapture(t *testing.T) {
	capturer := &quickCaptureHTTPFakeCapturer{}
	controller := NewQuickCaptureController(capturer, nil, nil)
	controller.spawn = func(run func()) { run() }

	server := &Server{quickCapture: controller}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/quick-capture", nil)

	server.handleQuickCapture(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	if capturer.calls != 1 {
		t.Fatalf("capture calls = %d, want 1", capturer.calls)
	}
}

func TestQuickCaptureHTTPTriggerRequiresPost(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/quick-capture", nil)

	server.handleQuickCapture(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
