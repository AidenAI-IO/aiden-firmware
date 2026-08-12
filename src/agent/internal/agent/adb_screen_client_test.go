package agent

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		"if [ \"$1\" = \"devices\" ] && [ \"$2\" = \"-l\" ]; then\n" +
		"  printf 'List of devices attached\\nserial123\\tdevice product:panther model:Pixel_7_Pro device:panther transport_id:1\\n'\n" +
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
	defer clearAutoConfiguredADBSerial("serial123")

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

	pngMeta, pngFrame, err := client.LatestFrameWithFormat("png", screenshotJPEGQuality, 0)
	if err != nil {
		t.Fatalf("LatestFrameWithFormat(png) error = %v", err)
	}
	if pngMeta == nil || pngMeta.PixelFormat != "png" || pngMeta.Width != 2 || pngMeta.Height != 1 {
		t.Fatalf("unexpected png metadata: %#v", pngMeta)
	}
	if !bytes.Equal(pngFrame, pngBuf.Bytes()) {
		t.Fatal("png adb frame does not match source png")
	}

	jpegMeta, jpegFrame, err := client.LatestFrameWithFormat("jpeg", screenshotJPEGQuality, 0)
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

	info := client.LastCaptureInfo()
	if info.Backend != "adb" {
		t.Fatalf("capture backend = %q, want adb", info.Backend)
	}
	if info.ADBDevice == nil {
		t.Fatal("expected adb device info")
	}
	if info.ADBDevice.Serial != "serial123" {
		t.Fatalf("adb serial = %q, want serial123", info.ADBDevice.Serial)
	}
	if info.ADBDevice.Name != "Pixel 7 Pro" {
		t.Fatalf("adb device name = %q, want Pixel 7 Pro", info.ADBDevice.Name)
	}
	if info.ADBDevice.State != "device" {
		t.Fatalf("adb device state = %q, want device", info.ADBDevice.State)
	}
}

func TestEnsureADBReverseUsesResolvedSerial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell script uses /bin/sh")
	}

	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "adb.log")
	adbPath := filepath.Join(tmpDir, "adb")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$AIDEN_TEST_ADB_LOG\"\n" +
		"if [ \"$1\" = \"devices\" ] && [ \"$2\" = \"-l\" ]; then\n" +
		"  printf 'List of devices attached\\nserial123\\tdevice product:panther model:Pixel_7_Pro device:panther transport_id:1\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"-s\" ] && [ \"$2\" = \"serial123\" ] && [ \"$3\" = \"reverse\" ] && [ \"$4\" = \"tcp:8080\" ] && [ \"$5\" = \"tcp:8080\" ]; then\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"unexpected args: $@\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", adbPath, err)
	}

	t.Setenv("AIDEN_ADB_PATH", adbPath)
	t.Setenv("AIDEN_TEST_ADB_LOG", logPath)
	t.Setenv("AIDEN_ADB_SERIAL", "")
	t.Setenv("ANDROID_SERIAL", "")
	defer clearAutoConfiguredADBSerial("serial123")

	if err := EnsureADBReverse(context.Background(), "8080", "8080"); err != nil {
		t.Fatalf("EnsureADBReverse() error = %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", logPath, err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "devices -l") {
		t.Fatalf("adb log missing devices call: %q", logText)
	}
	if !strings.Contains(logText, "-s serial123 reverse tcp:8080 tcp:8080") {
		t.Fatalf("adb log missing reverse call: %q", logText)
	}
}

func TestADBScreenClientAutoSetsAIDENADBSerialForUniqueDevice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell script uses /bin/sh")
	}

	tmpDir := t.TempDir()
	adbPath := filepath.Join(tmpDir, "adb")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"devices\" ] && [ \"$2\" = \"-l\" ]; then\n" +
		"  printf 'List of devices attached\\nserial123\\tdevice product:panther model:Pixel_7_Pro device:panther transport_id:1\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo \"unexpected args: $@\" >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", adbPath, err)
	}

	t.Setenv("AIDEN_ADB_PATH", adbPath)
	t.Setenv("AIDEN_ADB_SERIAL", "")
	t.Setenv("ANDROID_SERIAL", "")
	defer clearAutoConfiguredADBSerial("serial123")

	client := NewADBScreenClient()
	serial, err := client.resolveSerial(context.Background(), adbPath)
	if err != nil {
		t.Fatalf("resolveSerial() error = %v", err)
	}
	if serial != "serial123" {
		t.Fatalf("resolveSerial() = %q, want serial123", serial)
	}
	if got := os.Getenv("AIDEN_ADB_SERIAL"); got != "serial123" {
		t.Fatalf("AIDEN_ADB_SERIAL = %q, want serial123", got)
	}

	if err := os.Unsetenv("AIDEN_ADB_SERIAL"); err != nil {
		t.Fatalf("Unsetenv(AIDEN_ADB_SERIAL) error = %v", err)
	}
	serial, err = client.resolveSerial(context.Background(), adbPath)
	if err != nil {
		t.Fatalf("resolveSerial() cached error = %v", err)
	}
	if serial != "serial123" {
		t.Fatalf("resolveSerial() cached = %q, want serial123", serial)
	}
	if got := os.Getenv("AIDEN_ADB_SERIAL"); got != "serial123" {
		t.Fatalf("cached resolve AIDEN_ADB_SERIAL = %q, want serial123", got)
	}

	client.invalidateAutoSerial("serial123")
	if got := os.Getenv("AIDEN_ADB_SERIAL"); got != "" {
		t.Fatalf("AIDEN_ADB_SERIAL after invalidate = %q, want empty", got)
	}
}

func TestADBScreenClientDoesNotOverrideConfiguredADBSerial(t *testing.T) {
	t.Setenv("AIDEN_ADB_SERIAL", "manual123")
	t.Setenv("ANDROID_SERIAL", "")

	if err := setAutoConfiguredADBSerial("auto123"); err != nil {
		t.Fatalf("setAutoConfiguredADBSerial() error = %v", err)
	}
	if got := os.Getenv("AIDEN_ADB_SERIAL"); got != "manual123" {
		t.Fatalf("AIDEN_ADB_SERIAL = %q, want manual123", got)
	}
}
