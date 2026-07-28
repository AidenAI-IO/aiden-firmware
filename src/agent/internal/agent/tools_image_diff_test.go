package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"strings"
	"testing"
)

func TestImageDiffAcceptsScreenshotAttachmentIDs(t *testing.T) {
	attachments := map[string][]byte{
		"11111111-1111-1111-1111-111111111111.jpg": solidImageDiffJPEG(t, color.Black),
		"22222222-2222-2222-2222-222222222222.jpg": solidImageDiffJPEG(t, color.White),
	}
	ctx := withImageDiffAttachmentResolver(context.Background(), func(attachmentID string) ([]byte, error) {
		data, ok := attachments[attachmentID]
		if !ok {
			return nil, errors.New("not found")
		}
		return data, nil
	})

	out, err := (&ImageDiffTool{}).Call(ctx, `{"before":"11111111-1111-1111-1111-111111111111.jpg","after":"22222222-2222-2222-2222-222222222222.jpg"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	var result imageDiffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v; output=%s", err, out)
	}
	if !result.Changed || result.DiffRatio != 1 {
		t.Fatalf("result = %#v, want changed=true diff_ratio=1", result)
	}
}

func TestImageDiffKeepsBase64Compatibility(t *testing.T) {
	before := base64.StdEncoding.EncodeToString(solidImageDiffJPEG(t, color.Black))
	after := base64.StdEncoding.EncodeToString(solidImageDiffJPEG(t, color.White))
	input, err := json.Marshal(map[string]string{"before": before, "after": after})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	out, err := (&ImageDiffTool{}).Call(context.Background(), string(input))
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	var result imageDiffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal result: %v; output=%s", err, out)
	}
	if !result.Changed || result.DiffRatio != 1 {
		t.Fatalf("result = %#v, want changed=true diff_ratio=1", result)
	}
}

func TestImageDiffRejectsUnavailableScreenshotAttachment(t *testing.T) {
	ctx, _ := WithToolError(withImageDiffAttachmentResolver(context.Background(), func(string) ([]byte, error) {
		return nil, errors.New("not present")
	}))
	out, err := (&ImageDiffTool{}).Call(ctx, `{"before":"11111111-1111-1111-1111-111111111111.jpg","after":"22222222-2222-2222-2222-222222222222.jpg"}`)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(out, "11111111-1111-1111-1111-111111111111.jpg") || !strings.Contains(out, "not present") {
		t.Fatalf("output = %q", out)
	}
	if toolErr := ToolErrorFromContext(ctx); toolErr == nil || toolErr.Code != CodeInvalidArguments {
		t.Fatalf("tool error = %#v, want code %q", toolErr, CodeInvalidArguments)
	}
}

func TestImageDiffSchemaUsesAttachmentCompatibleBeforeAfterFields(t *testing.T) {
	properties := (&ImageDiffTool{}).ArgsSchema()["properties"].(map[string]any)
	if properties["before"] == nil || properties["after"] == nil || properties["region"] == nil {
		t.Fatalf("schema missing expected fields: %#v", properties)
	}
	if properties["before_id"] != nil || properties["after_id"] != nil {
		t.Fatalf("schema unexpectedly exposes screenshot ID fields: %#v", properties)
	}
}

func TestScreenshotAttachmentIDRejectsPaths(t *testing.T) {
	for _, value := range []string{"../screen.jpg", "/tmp/screen.jpg", `dir\\screen.jpg`, "screen.txt", ""} {
		if isScreenshotAttachmentID(value) {
			t.Fatalf("isScreenshotAttachmentID(%q) = true", value)
		}
	}
	if !isScreenshotAttachmentID("11111111-1111-1111-1111-111111111111.jpg") {
		t.Fatal("valid screenshot attachment ID was rejected")
	}
	if !isScreenshotAttachmentID("11111111-1111-1111-1111-111111111111.png") {
		t.Fatal("valid PNG screenshot attachment ID was rejected")
	}
}

func solidImageDiffJPEG(t *testing.T, fill color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func TestImageDiffRegionRejectsInvalidValues(t *testing.T) {
	full := image.Rect(0, 0, 100, 200)
	tests := []imageDiffRegion{
		{X: -100, Y: 0, W: 500, H: 500},
		{X: 0, Y: 1100, W: 500, H: 500},
		{X: 0, Y: 0, W: 0, H: 500},
		{X: 0, Y: 0, W: 500, H: -100},
	}

	for _, tt := range tests {
		_, err := tt.toPixelRect(full)
		if err == nil {
			t.Fatalf("toPixelRect(%+v) succeeded, want error", tt)
		}
	}
}

func TestImageDiffRegionClampsToImageBounds(t *testing.T) {
	full := image.Rect(10, 20, 110, 220)
	got, err := (&imageDiffRegion{X: 800, Y: 750, W: 500, H: 500}).toPixelRect(full)
	if err != nil {
		t.Fatalf("toPixelRect returned error: %v", err)
	}
	want := image.Rect(90, 170, 110, 220)
	if got != want {
		t.Fatalf("toPixelRect = %v, want %v", got, want)
	}
}

func TestImageDiffRegionRejectsNonFiniteValues(t *testing.T) {
	full := image.Rect(0, 0, 100, 100)
	_, err := (&imageDiffRegion{X: 0, Y: 0, W: 500, H: math.NaN()}).toPixelRect(full)
	if err == nil {
		t.Fatal("expected error for non-finite region")
	}
	if !strings.Contains(err.Error(), "finite normalized") {
		t.Fatalf("unexpected error: %v", err)
	}
}
