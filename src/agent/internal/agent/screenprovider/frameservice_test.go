package screenprovider

import (
	"encoding/json"
	"testing"
)

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
