package agent

import (
	"encoding/json"
	"testing"
)

func TestFrameMetadataUnmarshalSupportsStringNumbers(t *testing.T) {
	input := []byte(`{
		"seq":"123",
		"width":"1920",
		"height":"1080",
		"pixel_format":"jpeg",
		"stride":"5760",
		"bytes":"456789",
		"stale":"false"
	}`)

	var meta frameMetadata
	if err := json.Unmarshal(input, &meta); err != nil {
		t.Fatalf("unmarshal frameMetadata: %v", err)
	}

	if meta.Seq != 123 {
		t.Fatalf("unexpected seq: %d", meta.Seq)
	}
	if meta.Width != 1920 {
		t.Fatalf("unexpected width: %d", meta.Width)
	}
	if meta.Height != 1080 {
		t.Fatalf("unexpected height: %d", meta.Height)
	}
	if meta.PixelFormat != "jpeg" {
		t.Fatalf("unexpected pixel format: %q", meta.PixelFormat)
	}
	if meta.Stride != 5760 {
		t.Fatalf("unexpected stride: %d", meta.Stride)
	}
	if meta.Bytes != 456789 {
		t.Fatalf("unexpected bytes: %d", meta.Bytes)
	}
	if meta.Stale {
		t.Fatalf("unexpected stale flag: %v", meta.Stale)
	}
}
