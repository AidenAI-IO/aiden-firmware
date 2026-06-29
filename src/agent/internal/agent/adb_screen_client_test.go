package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestADBScreenClientCapturesPNGAndJPEG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell script uses /bin/sh")
	}

	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}

	tmpDir := t.TempDir()
	pngPath := filepath.Join(tmpDir, "screen.png")
	if err := os.WriteFile(pngPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", pngPath, err)
	}

	adbPath := filepath.Join(tmpDir, "adb")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"devices\" ]; then\n" +
		"  printf 'List of devices attached\\nserial123\\tdevice\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"-s\" ] && [ \"$2\" = \"serial123\" ] && [ \"$3\" = \"exec-out\" ] && [ \"$4\" = \"screencap\" ] && [ \"$5\" = \"-p\" ]; then\n" +
		"  cat \"$AIDEN_TEST_PNG\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"unexpected args: $@\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", adbPath, err)
	}

	t.Setenv("AIDEN_ADB_PATH", adbPath)
	t.Setenv("AIDEN_TEST_PNG", pngPath)
	t.Setenv("AIDEN_ADB_SERIAL", "")
	t.Setenv("ANDROID_SERIAL", "")

	client := NewADBScreenClient()

	rawMeta, rawFrame, err := client.LatestFrame()
	if err != nil {
		t.Fatalf("LatestFrame() error = %v", err)
	}
	if rawMeta == nil || rawMeta.PixelFormat != "png" || rawMeta.Width != 2 || rawMeta.Height != 1 {
		t.Fatalf("unexpected raw metadata: %#v", rawMeta)
	}
	if !bytes.Equal(rawFrame, pngBuf.Bytes()) {
		t.Fatal("raw adb frame does not match source png")
	}

	jpegMeta, jpegFrame, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat() error = %v", err)
	}
	if jpegMeta == nil || jpegMeta.PixelFormat != "jpeg" || jpegMeta.Width != 2 || jpegMeta.Height != 1 {
		t.Fatalf("unexpected jpeg metadata: %#v", jpegMeta)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(jpegFrame))
	if err != nil {
		t.Fatalf("jpeg.Decode() error = %v", err)
	}
	if bounds := decoded.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 1 {
		t.Fatalf("decoded jpeg bounds = %v, want 2x1", bounds)
	}
}
