package screenprovider

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFrameServiceWaitUntilReadyRetriesUntilSocketExists(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "frame.sock")
	serverDone := make(chan error, 1)

	go func() {
		time.Sleep(150 * time.Millisecond)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			serverDone <- err
			return
		}
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if _, _, err := ReadUDSMessage(conn); err != nil {
			serverDone <- err
			return
		}
		response := []byte(`{"type":"response","method":"health","status":"OK","state":"RUNNING","capture_mode":"buffered","latest_seq":1,"frame_age_ms":10}`)
		serverDone <- WriteUDSMessage(conn, response, nil)
	}()

	started := time.Now()
	health, err := NewFrameService(socketPath).WaitUntilReady(2 * time.Second)
	if err != nil {
		t.Fatalf("WaitUntilReady() error = %v", err)
	}
	if health == nil || health.State != "RUNNING" || health.LatestSeq != 1 {
		t.Fatalf("unexpected health: %#v", health)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("returned before delayed socket existed: %s", elapsed)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake frame service error: %v", err)
	}
}

func TestFrameServiceWaitUntilReadyWaitsForFirstBufferedFrame(t *testing.T) {
	var healthCalls atomic.Int32
	socketPath := startFrameServiceTestSocket(t, func() string {
		if healthCalls.Add(1) == 1 {
			return `{"type":"response","method":"health","status":"OK","state":"RECOVERING","capture_mode":"buffered","latest_seq":0,"frame_age_ms":0}`
		}
		return `{"type":"response","method":"health","status":"OK","state":"RUNNING","capture_mode":"buffered","latest_seq":2,"frame_age_ms":12}`
	})

	health, err := NewFrameService(socketPath).WaitUntilReady(time.Second)
	if err != nil {
		t.Fatalf("WaitUntilReady() error = %v", err)
	}
	if healthCalls.Load() < 2 {
		t.Fatalf("health calls = %d, want at least 2", healthCalls.Load())
	}
	if health.State != "RUNNING" || health.LatestSeq != 2 {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestFrameServiceWaitUntilReadyDoesNotWaitForOnDemandFrame(t *testing.T) {
	var healthCalls atomic.Int32
	socketPath := startFrameServiceTestSocket(t, func() string {
		healthCalls.Add(1)
		return `{"type":"response","method":"health","status":"OK","state":"STARTING","capture_mode":"on_demand","latest_seq":0,"frame_age_ms":0}`
	})

	health, err := NewFrameService(socketPath).WaitUntilReady(time.Second)
	if err != nil {
		t.Fatalf("WaitUntilReady() error = %v", err)
	}
	if health == nil || health.CaptureMode != "on_demand" || health.State != "STARTING" {
		t.Fatalf("unexpected health: %#v", health)
	}
	if healthCalls.Load() != 1 {
		t.Fatalf("health calls = %d, want 1", healthCalls.Load())
	}
}

func TestFrameServiceWaitUntilReadyReportsStateOnTimeout(t *testing.T) {
	socketPath := startFrameServiceTestSocket(t, func() string {
		return `{"type":"response","method":"health","status":"OK","state":"RECOVERING","capture_mode":"buffered","latest_seq":0,"frame_age_ms":0}`
	})

	_, err := NewFrameService(socketPath).WaitUntilReady(150 * time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	for _, want := range []string{"timed out", "state=RECOVERING", "latest_seq=0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
}

func TestFrameServiceWaitUntilReadyRejectsNonTransientError(t *testing.T) {
	var healthCalls atomic.Int32
	socketPath := startFrameServiceTestSocket(t, func() string {
		healthCalls.Add(1)
		return `{not-json`
	})

	_, err := NewFrameService(socketPath).WaitUntilReady(time.Second)
	if err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Fatalf("error = %v, want parse response", err)
	}
	if healthCalls.Load() != 1 {
		t.Fatalf("health calls = %d, want non-transient error to stop after 1", healthCalls.Load())
	}
}

func startFrameServiceTestSocket(t *testing.T, response func() string) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "frame.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on fake frame socket: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if _, _, err := ReadUDSMessage(conn); err != nil {
				t.Errorf("fake frame service read request: %v", err)
				conn.Close()
				continue
			}
			if err := WriteUDSMessage(conn, []byte(response()), nil); err != nil {
				t.Errorf("fake frame service write response: %v", err)
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
	})
	return socketPath
}

func TestFrameMetadataUnmarshalSupportsStringNumbers(t *testing.T) {
	input := []byte(`{
		"seq":"123",
		"width":"1920",
		"height":"1080",
		"source_width":"1280",
		"source_height":"720",
		"crop_x":"0",
		"crop_y":"72",
		"crop_width":"1280",
		"crop_height":"576",
		"pixel_format":"jpeg",
		"stride":"5760",
		"bytes":"456789",
		"stale":"false"
	}`)

	var meta FrameMetadata
	if err := json.Unmarshal(input, &meta); err != nil {
		t.Fatalf("unmarshal FrameMetadata: %v", err)
	}
	if meta.Seq != 123 || meta.Width != 1920 || meta.Height != 1080 {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if meta.SourceWidth != 1280 || meta.SourceHeight != 720 {
		t.Fatalf("unexpected source dims: %dx%d", meta.SourceWidth, meta.SourceHeight)
	}
	if meta.CropX != 0 || meta.CropY != 72 || meta.CropWidth != 1280 || meta.CropHeight != 576 {
		t.Fatalf("unexpected crop rect: x=%d y=%d w=%d h=%d", meta.CropX, meta.CropY, meta.CropWidth, meta.CropHeight)
	}
	if meta.PixelFormat != "jpeg" || meta.Stride != 5760 || meta.Bytes != 456789 || meta.Stale {
		t.Fatalf("unexpected remaining fields: %#v", meta)
	}
}

func TestFrameHealthResponseUnmarshalSupportsStringUint64Fields(t *testing.T) {
	input := []byte(`{
		"status":"OK",
		"state":"RUNNING",
		"latest_seq":"10663",
		"frame_age_ms":"139",
		"ring_buffer_size":3,
		"ring_buffer_used":3,
		"consecutive_failures":0,
		"last_error":"",
		"last_recovery_ts":"22965550301",
		"avg_frame_serve_latency_ms":1090.015,
		"avg_capture_copy_latency_ms":19.126
	}`)

	var response frameHealthResponse
	if err := json.Unmarshal(input, &response); err != nil {
		t.Fatalf("unmarshal frameHealthResponse: %v", err)
	}
	if response.LatestSeq != 10663 || response.FrameAgeMs != 139 || response.LastRecoveryTs != 22965550301 {
		t.Fatalf("unexpected health response: %+v", response)
	}
}

func TestFrameMetadataUnmarshalAllowsOmittedSourceAndCropFields(t *testing.T) {
	input := []byte(`{
		"seq":"123",
		"width":"1920",
		"height":"1080",
		"pixel_format":"jpeg",
		"stride":"5760",
		"bytes":"456789",
		"stale":"false"
	}`)

	var meta FrameMetadata
	if err := json.Unmarshal(input, &meta); err != nil {
		t.Fatalf("unmarshal FrameMetadata: %v", err)
	}
	if meta.SourceWidth != 0 || meta.SourceHeight != 0 {
		t.Fatalf("unexpected source dims: %dx%d", meta.SourceWidth, meta.SourceHeight)
	}
	if meta.CropX != 0 || meta.CropY != 0 || meta.CropWidth != 0 || meta.CropHeight != 0 {
		t.Fatalf("unexpected crop rect: x=%d y=%d w=%d h=%d", meta.CropX, meta.CropY, meta.CropWidth, meta.CropHeight)
	}
}

func TestFrameHealthResponseUnmarshalSupportsStringLastRecoveryTs(t *testing.T) {
	input := []byte(`{
		"status":"OK",
		"state":"RUNNING",
		"latest_seq":12,
		"frame_age_ms":7,
		"ring_buffer_size":8,
		"ring_buffer_used":3,
		"consecutive_failures":1,
		"last_error":"recovering",
		"last_recovery_ts":"9007199254740993",
		"avg_frame_serve_latency_ms":1.5,
		"avg_capture_copy_latency_ms":2.5
	}`)

	var resp frameHealthResponse
	if err := json.Unmarshal(input, &resp); err != nil {
		t.Fatalf("unmarshal frameHealthResponse: %v", err)
	}
	if resp.LastRecoveryTs != 9007199254740993 {
		t.Fatalf("unexpected last_recovery_ts: %d", resp.LastRecoveryTs)
	}
}

func TestLatestFrameRequestJSONEscapesFormat(t *testing.T) {
	encoded, err := latestFrameRequestJSON(`jpeg","evil":true`, 80, true, CropHint{
		MinimalWidth: 16,
		ScreenWidth:  2608,
		ScreenHeight: 1200,
	})
	if err != nil {
		t.Fatalf("latestFrameRequestJSON() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		t.Fatalf("request is not valid JSON: %v body=%s", err, encoded)
	}
	if payload["format"] != `jpeg","evil":true` {
		t.Fatalf("format = %#v, want the original string as one JSON value", payload["format"])
	}
	if payload["method"] != "latest_frame" || payload["quality"] != float64(80) ||
		payload["minimal_width"] != float64(16) || payload["screen_width"] != float64(2608) ||
		payload["screen_height"] != float64(1200) {
		t.Fatalf("unexpected request payload: %#v", payload)
	}
}
