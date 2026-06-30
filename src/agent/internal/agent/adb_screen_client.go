package agent

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	adbScreenCaptureTimeout = 5 * time.Second
	adbDeviceListCacheTTL   = 5 * time.Second
)

var (
	adbLookPath       = exec.LookPath
	adbCommandContext = exec.CommandContext
)

// ADBScreenClient captures the connected Android device screen via adb
// screencap when frame_service is unavailable.
type ADBScreenClient struct {
	mu                     sync.Mutex
	cachedAutoSerial       string
	cachedAutoSerialExpiry time.Time
	cachedDevices          []adbListedDevice
	cachedDevicesExpiry    time.Time
	lastCaptureInfo        screenCaptureInfo
	seq                    atomic.Uint64
}

type adbListedDevice struct {
	Serial     string
	State      string
	Model      string
	DeviceName string
	Product    string
}

func NewADBScreenClient() *ADBScreenClient {
	return &ADBScreenClient{}
}

func (c *ADBScreenClient) LastCaptureInfo() screenCaptureInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneScreenCaptureInfo(c.lastCaptureInfo)
}

func (c *ADBScreenClient) LatestFrame() (*frameMetadata, []byte, error) {
	return c.capture("raw", 0)
}

func (c *ADBScreenClient) LatestFrameWithFormat(format string, quality int) (*frameMetadata, []byte, error) {
	return c.capture(format, quality)
}

func (c *ADBScreenClient) capture(format string, quality int) (*frameMetadata, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), adbScreenCaptureTimeout)
	defer cancel()

	adbPath, err := c.adbPath()
	if err != nil {
		return nil, nil, err
	}

	serial, err := c.resolveSerial(ctx, adbPath)
	if err != nil {
		return nil, nil, err
	}

	pngData, err := c.capturePNG(ctx, adbPath, serial)
	if err != nil {
		c.invalidateAutoSerial(serial)
		return nil, nil, err
	}
	c.recordLastCaptureInfo(screenCaptureInfo{
		Backend:   "adb",
		ADBDevice: c.captureDeviceInfo(ctx, adbPath, serial),
	})

	cfg, _, err := image.DecodeConfig(bytes.NewReader(pngData))
	if err != nil {
		return nil, nil, fmt.Errorf("decode adb screenshot config: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, nil, fmt.Errorf("invalid adb screenshot dimensions: %dx%d", cfg.Width, cfg.Height)
	}

	meta := &frameMetadata{
		Seq:          c.seq.Add(1),
		Width:        uint32(cfg.Width),
		Height:       uint32(cfg.Height),
		SourceWidth:  uint32(cfg.Width),
		SourceHeight: uint32(cfg.Height),
		CropX:        0,
		CropY:        0,
		CropWidth:    uint32(cfg.Width),
		CropHeight:   uint32(cfg.Height),
		PixelFormat:  "png",
		Stride:       0,
		Bytes:        uint64(len(pngData)),
		Stale:        false,
	}

	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "raw":
		return meta, pngData, nil
	case "jpeg", "jpg":
		jpegData, err := encodeEncodedImageToJPEG(pngData, quality)
		if err != nil {
			return nil, nil, err
		}
		meta.PixelFormat = "jpeg"
		meta.Bytes = uint64(len(jpegData))
		return meta, jpegData, nil
	default:
		return nil, nil, fmt.Errorf("unsupported adb fallback format: %s", format)
	}
}

func (c *ADBScreenClient) adbPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("AIDEN_ADB_PATH")); configured != "" {
		return configured, nil
	}
	path, err := adbLookPath("adb")
	if err != nil {
		return "", fmt.Errorf("adb not found: %w", err)
	}
	return path, nil
}

func configuredADBSerial() string {
	for _, key := range []string{"AIDEN_ADB_SERIAL", "ANDROID_SERIAL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func (c *ADBScreenClient) resolveSerial(ctx context.Context, adbPath string) (string, error) {
	if serial := configuredADBSerial(); serial != "" {
		return serial, nil
	}

	now := time.Now()
	c.mu.Lock()
	cachedSerial := c.cachedAutoSerial
	cachedExpiry := c.cachedAutoSerialExpiry
	c.mu.Unlock()
	if cachedSerial != "" && now.Before(cachedExpiry) {
		return cachedSerial, nil
	}

	devices, err := c.listDevices(ctx, adbPath)
	if err != nil {
		return "", err
	}

	var connected []string
	for _, device := range devices {
		if device.State != "device" {
			continue
		}
		connected = append(connected, device.Serial)
	}
	if len(connected) == 0 {
		return "", fmt.Errorf("no connected adb device")
	}
	if len(connected) > 1 {
		return "", fmt.Errorf("multiple adb devices connected; set AIDEN_ADB_SERIAL or ANDROID_SERIAL")
	}

	serial := connected[0]
	c.mu.Lock()
	c.cachedAutoSerial = serial
	c.cachedAutoSerialExpiry = now.Add(adbDeviceListCacheTTL)
	c.mu.Unlock()
	return serial, nil
}

func (c *ADBScreenClient) invalidateAutoSerial(serial string) {
	if serial == "" || configuredADBSerial() != "" {
		return
	}
	c.mu.Lock()
	if c.cachedAutoSerial == serial {
		c.cachedAutoSerial = ""
		c.cachedAutoSerialExpiry = time.Time{}
	}
	c.mu.Unlock()
}

func (c *ADBScreenClient) listDevices(ctx context.Context, adbPath string) ([]adbListedDevice, error) {
	now := time.Now()
	c.mu.Lock()
	cachedDevices := append([]adbListedDevice(nil), c.cachedDevices...)
	cachedExpiry := c.cachedDevicesExpiry
	c.mu.Unlock()
	if len(cachedDevices) > 0 && now.Before(cachedExpiry) {
		return cachedDevices, nil
	}

	stdout, _, err := runADBCommand(ctx, adbPath, "devices", "-l")
	if err != nil {
		return nil, err
	}
	devices := parseADBDeviceList(stdout)
	c.mu.Lock()
	c.cachedDevices = append([]adbListedDevice(nil), devices...)
	c.cachedDevicesExpiry = now.Add(adbDeviceListCacheTTL)
	c.mu.Unlock()
	return devices, nil
}

func parseADBDeviceList(stdout []byte) []adbListedDevice {
	var devices []adbListedDevice
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices attached") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		device := adbListedDevice{
			Serial: fields[0],
			State:  fields[1],
		}
		for _, field := range fields[2:] {
			key, value, ok := strings.Cut(field, ":")
			if !ok {
				continue
			}
			switch key {
			case "model":
				device.Model = strings.ReplaceAll(value, "_", " ")
			case "device":
				device.DeviceName = value
			case "product":
				device.Product = value
			}
		}
		devices = append(devices, device)
	}
	return devices
}

func (d adbListedDevice) info() *adbDeviceInfo {
	name := strings.TrimSpace(d.Model)
	if name == "" {
		name = strings.TrimSpace(d.DeviceName)
	}
	if name == "" {
		name = strings.TrimSpace(d.Product)
	}
	if name == "" {
		name = strings.TrimSpace(d.Serial)
	}
	state := strings.TrimSpace(d.State)
	if state == "" {
		state = "unknown"
	}
	return &adbDeviceInfo{
		Serial: strings.TrimSpace(d.Serial),
		Name:   name,
		State:  state,
	}
}

func (c *ADBScreenClient) captureDeviceInfo(ctx context.Context, adbPath, serial string) *adbDeviceInfo {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return nil
	}
	devices, err := c.listDevices(ctx, adbPath)
	if err == nil {
		for _, device := range devices {
			if device.Serial == serial {
				return device.info()
			}
		}
	}
	return &adbDeviceInfo{
		Serial: serial,
		Name:   serial,
		State:  "device",
	}
}

func (c *ADBScreenClient) recordLastCaptureInfo(info screenCaptureInfo) {
	c.mu.Lock()
	c.lastCaptureInfo = cloneScreenCaptureInfo(info)
	c.mu.Unlock()
}

func (c *ADBScreenClient) capturePNG(ctx context.Context, adbPath, serial string) ([]byte, error) {
	args := make([]string, 0, 5)
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "exec-out", "screencap", "-p")

	stdout, stderr, err := runADBCommand(ctx, adbPath, args...)
	if err != nil {
		if trimmed := strings.TrimSpace(string(stderr)); trimmed != "" {
			return nil, fmt.Errorf("adb screencap failed: %s", trimmed)
		}
		return nil, err
	}
	if len(stdout) == 0 {
		return nil, fmt.Errorf("adb screencap returned empty image")
	}
	return stdout, nil
}

func runADBCommand(ctx context.Context, adbPath string, args ...string) ([]byte, []byte, error) {
	cmd := adbCommandContext(ctx, adbPath, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if trimmed := strings.TrimSpace(stderr.String()); trimmed != "" {
			return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("adb %s: %s", strings.Join(args, " "), trimmed)
		}
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("adb %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}
