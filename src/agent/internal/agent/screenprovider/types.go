package screenprovider

import "time"

const (
	DefaultFrameSocket = "/run/frame_service/frame_service.sock"
	DefaultJPEGQuality = 80
	DefaultTimeout     = 30 * time.Second
	Path               = "/api/providers/screenshot"
	TaskIDHeader       = "benchmark-task-id"
)

// Provider captures the latest screen frame in a requested encoding.
type Provider interface {
	LatestFrameWithFormat(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, CaptureInfo, error)
}

// Source is a raw capture backend used by Fallback (frame_service or adb).
// LatestFrameWithFormat does not attach CaptureInfo; Fallback adds it.
type Source interface {
	LatestFrame() (*FrameMetadata, []byte, error)
	LatestFrameWithFormat(format string, quality int, cropBlack bool, hint CropHint) (*FrameMetadata, []byte, error)
}

// CropHint carries optional geometry for black-bar cropping. ScreenWidth and
// ScreenHeight describe the current phone orientation; MinimalWidth preserves
// compatibility with callers that only know a horizontal crop width.
type CropHint struct {
	MinimalWidth int `json:"minimal_width,omitempty"`
	ScreenWidth  int `json:"screen_width,omitempty"`
	ScreenHeight int `json:"screen_height,omitempty"`
}

type FrameMetadata struct {
	Seq          uint64 `json:"seq"`
	Width        uint32 `json:"width"`
	Height       uint32 `json:"height"`
	SourceWidth  uint32 `json:"source_width,omitempty"`
	SourceHeight uint32 `json:"source_height,omitempty"`
	CropX        uint32 `json:"crop_x,omitempty"`
	CropY        uint32 `json:"crop_y,omitempty"`
	CropWidth    uint32 `json:"crop_width,omitempty"`
	CropHeight   uint32 `json:"crop_height,omitempty"`
	PixelFormat  string `json:"pixel_format"`
	Stride       uint32 `json:"stride"`
	Bytes        uint64 `json:"bytes"`
	Stale        bool   `json:"stale"`
}

type DeviceInfo struct {
	Serial string `json:"serial,omitempty"`
	Name   string `json:"name,omitempty"`
	State  string `json:"state,omitempty"`
}

type CaptureInfo struct {
	Backend   string      `json:"capture_backend,omitempty"`
	ADBDevice *DeviceInfo `json:"adb_device,omitempty"`
}

type HealthResult struct {
	State                   string  `json:"state"`
	CaptureMode             string  `json:"capture_mode"`
	LatestSeq               uint64  `json:"latest_seq"`
	FrameAgeMs              uint64  `json:"frame_age_ms"`
	RingBufferSize          uint32  `json:"ring_buffer_size"`
	RingBufferUsed          uint32  `json:"ring_buffer_used"`
	ConsecutiveFailures     uint32  `json:"consecutive_failures"`
	LastError               string  `json:"last_error"`
	LastRecoveryTs          uint64  `json:"last_recovery_ts"`
	AvgFrameServeLatencyMs  float64 `json:"avg_frame_serve_latency_ms"`
	AvgCaptureCopyLatencyMs float64 `json:"avg_capture_copy_latency_ms"`
}

func CloneDeviceInfo(info *DeviceInfo) *DeviceInfo {
	if info == nil {
		return nil
	}
	copy := *info
	return &copy
}

func CloneCaptureInfo(info CaptureInfo) CaptureInfo {
	info.ADBDevice = CloneDeviceInfo(info.ADBDevice)
	return info
}
