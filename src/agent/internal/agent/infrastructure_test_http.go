package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"aiden-agent/internal/agent/mnk"
	"aiden-agent/internal/agent/screenprovider"
)

const (
	infrastructureTestMaxBodyBytes          = 64 * 1024
	infrastructureHIDDefaultKey             = "escape"
	infrastructureHIDInputKey               = "h"
	infrastructureHIDEnterKey               = "enter"
	infrastructureHIDDefaultClickCoordinate = 500
	infrastructureHIDDefaultHoldMs          = 80
	infrastructureHDMIDefaultTimeout        = 3 * time.Second
	infrastructureHDMIMaxTimeout            = 15 * time.Second
	infrastructureAudioDefaultDuration      = 1 * time.Second
	infrastructureAudioMaxDuration          = 3 * time.Second
	infrastructureAudioReadPoll             = 250 * time.Millisecond
	infrastructureAudioStopDrainTimeout     = 1 * time.Second
	infrastructureAudioDrainTimeout         = 4 * time.Second
)

type infrastructureTestResponse struct {
	OK         bool                     `json:"ok"`
	Target     string                   `json:"target"`
	Message    string                   `json:"message"`
	Error      string                   `json:"error,omitempty"`
	Steps      []infrastructureTestStep `json:"steps,omitempty"`
	Details    map[string]any           `json:"details,omitempty"`
	DurationMS int64                    `json:"duration_ms"`
	Timestamp  time.Time                `json:"timestamp"`

	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	SourceWidth  int    `json:"source_width,omitempty"`
	SourceHeight int    `json:"source_height,omitempty"`
	Format       string `json:"format,omitempty"`
	Size         int    `json:"size,omitempty"`
	Data         string `json:"data,omitempty"`
}

type infrastructureTestStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type infrastructureAudioContentStats struct {
	SampleCount    int     `json:"sample_count"`
	NonZeroSamples int     `json:"nonzero_samples"`
	PeakAbs        int     `json:"peak_abs"`
	MeanAbs        float64 `json:"mean_abs"`
	RMS            float64 `json:"rms"`
}

type infrastructureHIDRequest struct {
	Mode   string   `json:"mode,omitempty"`
	Key    string   `json:"key,omitempty"`
	Keys   []string `json:"keys,omitempty"`
	Click  *bool    `json:"click,omitempty"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Button string   `json:"button,omitempty"`
	HoldMs int      `json:"hold_ms,omitempty"`
}

type infrastructureHDMIRequest struct {
	TimeoutMS int `json:"timeout_ms,omitempty"`
	Quality   int `json:"quality,omitempty"`
}

type infrastructureAudioRequest struct {
	DurationMS int   `json:"duration_ms,omitempty"`
	Playback   *bool `json:"playback,omitempty"`
}

type infrastructureFrameClient interface {
	Health() (*screenprovider.HealthResult, error)
	LatestFrameWithFormatSince(format string, quality int, cropBlack bool, minimalWidth int, sinceSeq uint64, timeout time.Duration) (*screenprovider.FrameMetadata, []byte, error)
}

type infrastructureAudioClient interface {
	Health() (*AudioHealthResult, error)
	GetPlaybackVolume() (int, error)
	StartRecording(AudioFormat) (*RecordStartResult, error)
	ReadRecordChunk(sessionID uint64, timeoutMs uint32) (*AudioChunkResult, error)
	StopRecording(sessionID uint64) error
	StartPlayback(AudioFormat) (*PlaybackStartResult, error)
	WritePlayChunk(sessionID uint64, data []byte, isFinal bool) error
	StopPlayback(sessionID uint64) error
}

func (s *Server) handleInfrastructureTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeInfrastructureJSON(w, http.StatusMethodNotAllowed, infrastructureTestResponse{
			OK:        false,
			Message:   "method not allowed",
			Error:     "method not allowed",
			Timestamp: time.Now(),
		})
		return
	}

	target := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/infrastructure-test/"), "/")
	if target == "" || strings.Contains(target, "/") {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, infrastructureTestMaxBodyBytes))
	if err != nil {
		writeInfrastructureJSON(w, http.StatusBadRequest, infrastructureTestFailure(target, "failed to read request body", nil, nil, time.Now()))
		return
	}

	startedAt := time.Now()
	var result infrastructureTestResponse
	switch target {
	case "hid":
		result = s.runInfrastructureHIDTest(r.Context(), body, startedAt)
	case "hdmi":
		socketPath := defaultFrameServiceSocket
		if s != nil && s.runtime != nil {
			socketPath = s.runtime.config.HID.FrameSocketOrDefault()
		}
		result = runInfrastructureHDMITest(r.Context(), body, screenprovider.NewFrameService(socketPath), startedAt)
	case "audio-record", "audio-recording", "record", "recording":
		result = runInfrastructureAudioRecordTest(r.Context(), body, s.audioClient, s.infrastructureAudioFormat(), startedAt)
	case "audio-playback", "playback":
		result = runInfrastructureAudioPlaybackTest(r.Context(), body, s.audioClient, s.infrastructureAudioFormat(), startedAt)
	case "audio", "voice":
		result = runInfrastructureAudioTest(r.Context(), body, s.audioClient, s.infrastructureAudioFormat(), startedAt)
	default:
		http.NotFound(w, r)
		return
	}

	writeInfrastructureJSON(w, http.StatusOK, result)
}

func (s *Server) infrastructureAudioFormat() AudioFormat {
	format := AudioFormat{SampleRate: defaultAudioSampleRate, Channels: defaultAudioChannels, BitWidth: defaultAudioBitWidth}
	if s != nil && s.runtime != nil {
		format = AudioFormat{
			SampleRate: uint32(s.runtime.config.Audio.SampleRateOrDefault()),
			Channels:   uint32(s.runtime.config.Audio.ChannelsOrDefault()),
			BitWidth:   uint32(s.runtime.config.Audio.BitWidthOrDefault()),
		}
	}
	return format
}

func (s *Server) runInfrastructureHIDTest(ctx context.Context, body []byte, startedAt time.Time) infrastructureTestResponse {
	var req infrastructureHIDRequest
	if err := decodeInfrastructureJSON(body, &req); err != nil {
		return infrastructureTestFailure("hid", err.Error(), nil, nil, startedAt)
	}

	mode := normalizeInfrastructureHIDMode(req.Mode)
	if mode == "" {
		return infrastructureTestFailure("hid", "mode must be click or input", nil, nil, startedAt)
	}
	keySequences := normalizeInfrastructureHIDKeySequences(req, mode)
	clickEnabled := infrastructureHIDModeSendsClick(req, mode)
	x := float64(infrastructureHIDDefaultClickCoordinate)
	y := float64(infrastructureHIDDefaultClickCoordinate)
	if req.X != nil {
		x = *req.X
	}
	if req.Y != nil {
		y = *req.Y
	}
	button := strings.ToLower(strings.TrimSpace(req.Button))
	if button == "" {
		button = mnk.ButtonLeft
	}
	holdMs := req.HoldMs
	if holdMs <= 0 {
		holdMs = infrastructureHIDDefaultHoldMs
	}

	if len(keySequences) == 0 && !clickEnabled {
		return infrastructureTestFailure("hid", "HID key is required", nil, nil, startedAt)
	}
	for _, keys := range keySequences {
		if len(keys) > 6 {
			return infrastructureTestFailure("hid", "HID supports at most 6 simultaneous keys", nil, nil, startedAt)
		}
	}
	if button != mnk.ButtonLeft && button != mnk.ButtonRight && button != mnk.ButtonMiddle {
		return infrastructureTestFailure("hid", "button must be left, right, or middle", nil, nil, startedAt)
	}
	if clickEnabled && (invalidInfrastructureCoordinate(x) || invalidInfrastructureCoordinate(y)) {
		return infrastructureTestFailure("hid", "click coordinates must use the normalized 0-1000 scale", nil, nil, startedAt)
	}
	if holdMs < 1 || holdMs > 3000 {
		return infrastructureTestFailure("hid", "hold_ms must be between 1 and 3000", nil, nil, startedAt)
	}

	steps := make([]infrastructureTestStep, 0, 8)
	details := map[string]any{
		"mode":       mode,
		"keys":       keySequences,
		"click":      clickEnabled,
		"hold_ms":    holdMs,
		"ecm_device": "usb0",
	}
	if clickEnabled {
		details["click_point"] = map[string]any{"x": x, "y": y, "button": button}
	}

	hidCfg := HIDConfig{}
	if s != nil && s.runtime != nil {
		hidCfg = s.runtime.config.HIDConfigForDevice()
	}
	requiredDevices := []struct {
		name string
		path string
	}{}
	if len(keySequences) > 0 {
		requiredDevices = append(requiredDevices, struct {
			name string
			path string
		}{"keyboard HID device", hidCfg.KeyboardDeviceOrDefault()})
	}
	if clickEnabled {
		requiredDevices = append(requiredDevices, struct {
			name string
			path string
		}{"pointer HID device", hidCfg.MouseDeviceOrDefault()})
	}
	preconditionOK := true
	for _, device := range requiredDevices {
		if err := checkInfrastructureDevicePath(device.path); err != nil {
			preconditionOK = false
			steps = append(steps, infrastructureStep(device.name, "error", fmt.Sprintf("%s: %v", device.path, err), 0))
		} else {
			steps = append(steps, infrastructureStep(device.name, "ok", device.path, 0))
		}
	}
	androidDevice := hidCfg.AndroidKeyboardDeviceOrDefault()
	if err := checkInfrastructureDevicePath(androidDevice); err != nil {
		steps = append(steps, infrastructureStep("consumer/control HID device", "warn", fmt.Sprintf("%s: %v", androidDevice, err), 0))
	} else {
		steps = append(steps, infrastructureStep("consumer/control HID device", "ok", androidDevice, 0))
	}

	udc, udcErr := readInfrastructureUDC()
	if udcErr != nil {
		steps = append(steps, infrastructureStep("USB gadget UDC", "warn", udcErr.Error(), 0))
	} else if udc == "" {
		preconditionOK = false
		steps = append(steps, infrastructureStep("USB gadget UDC", "error", "UDC is empty; USB HID gadget is not bound", 0))
	} else {
		details["udc"] = udc
		steps = append(steps, infrastructureStep("USB gadget UDC", "ok", udc, 0))
	}

	if addresses, ok := infrastructureInterfaceAddresses("usb0"); ok {
		details["usb0_addresses"] = addresses
		steps = append(steps, infrastructureStep("USB ECM interface", "ok", strings.Join(addresses, ", "), 0))
	} else {
		steps = append(steps, infrastructureStep("USB ECM interface", "warn", "usb0 is not available or has no addresses", 0))
	}

	if !preconditionOK {
		return infrastructureTestFailure("hid", "HID precondition failed; no HID report was sent", steps, details, startedAt)
	}

	provider := mnkProviderFromRuntime(nil)
	if s != nil && s.runtime != nil {
		provider = mnkProviderFromRuntime(s.runtime)
	}
	if provider == nil {
		return infrastructureTestFailure("hid", "HID provider is not configured; no HID report was sent", steps, details, startedAt)
	}

	for _, keys := range keySequences {
		keyStarted := time.Now()
		if err := provider.Keypress(ctx, keys); err != nil {
			steps = append(steps, infrastructureStep("keyboard HID report", "error", err.Error(), time.Since(keyStarted)))
			return infrastructureTestFailure("hid", "keyboard HID report failed", steps, details, startedAt)
		}
		steps = append(steps, infrastructureStep("keyboard HID report", "ok", "sent keypress "+strings.Join(keys, "+"), time.Since(keyStarted)))
	}

	if clickEnabled {
		clickStarted := time.Now()
		if err := provider.Click(ctx, x, y, button, holdMs); err != nil {
			steps = append(steps, infrastructureStep("pointer HID report", "error", err.Error(), time.Since(clickStarted)))
			return infrastructureTestFailure("hid", "pointer HID report failed", steps, details, startedAt)
		}
		steps = append(steps, infrastructureStep("pointer HID report", "ok", fmt.Sprintf("sent %s click at %.0f,%.0f", button, x, y), time.Since(clickStarted)))
	}

	return infrastructureTestSuccess("hid", infrastructureHIDSuccessMessage(mode), steps, details, startedAt)
}

func runInfrastructureHDMITest(ctx context.Context, body []byte, client infrastructureFrameClient, startedAt time.Time) infrastructureTestResponse {
	_ = ctx
	var req infrastructureHDMIRequest
	if err := decodeInfrastructureJSON(body, &req); err != nil {
		return infrastructureTestFailure("hdmi", err.Error(), nil, nil, startedAt)
	}
	timeout := infrastructureHDMIDefaultTimeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	if timeout < 100*time.Millisecond || timeout > infrastructureHDMIMaxTimeout {
		return infrastructureTestFailure("hdmi", "timeout_ms must be between 100 and 15000", nil, nil, startedAt)
	}
	quality := req.Quality
	if quality <= 0 {
		quality = screenshotJPEGQuality
	}
	if quality < 1 || quality > 100 {
		return infrastructureTestFailure("hdmi", "quality must be between 1 and 100", nil, nil, startedAt)
	}
	if client == nil {
		return infrastructureTestFailure("hdmi", "frame_service client is not configured", nil, nil, startedAt)
	}

	steps := make([]infrastructureTestStep, 0, 4)
	details := map[string]any{"timeout_ms": int(timeout / time.Millisecond), "quality": quality}

	healthStarted := time.Now()
	healthBefore, err := client.Health()
	if err != nil {
		steps = append(steps, infrastructureStep("frame_service health", "error", err.Error(), time.Since(healthStarted)))
		return infrastructureTestFailure("hdmi", "frame_service health failed", steps, details, startedAt)
	}
	details["health_before"] = healthBefore
	steps = append(steps, infrastructureStep("frame_service health", "ok", formatFrameHealthSummary(healthBefore), time.Since(healthStarted)))
	if healthBefore.State != "RUNNING" {
		return infrastructureTestFailure("hdmi", "frame_service is not RUNNING", steps, details, startedAt)
	}

	sinceSeq := healthBefore.LatestSeq
	captureStarted := time.Now()
	meta, image, err := client.LatestFrameWithFormatSince("jpeg", quality, false, 0, sinceSeq, timeout)
	if err != nil {
		steps = append(steps, infrastructureStep("fresh HDMI frame", "error", err.Error(), time.Since(captureStarted)))
		if healthAfter, healthErr := client.Health(); healthErr == nil {
			details["health_after"] = healthAfter
		}
		return infrastructureTestFailure("hdmi", "HDMI 没有返回实时帧: "+err.Error(), steps, details, startedAt)
	}
	if meta == nil {
		steps = append(steps, infrastructureStep("fresh HDMI frame", "error", "missing frame metadata", time.Since(captureStarted)))
		return infrastructureTestFailure("hdmi", "HDMI 返回缺少帧元数据", steps, details, startedAt)
	}
	details["frame"] = meta
	if meta.Stale {
		steps = append(steps, infrastructureStep("fresh HDMI frame", "error", "frame marked stale", time.Since(captureStarted)))
		return infrastructureTestFailure("hdmi", "HDMI 返回的是 stale frame，不是实时截图", steps, details, startedAt)
	}
	if meta.Seq <= sinceSeq {
		steps = append(steps, infrastructureStep("fresh HDMI frame", "error", fmt.Sprintf("seq did not advance: before=%d after=%d", sinceSeq, meta.Seq), time.Since(captureStarted)))
		return infrastructureTestFailure("hdmi", "HDMI 截图不是实时帧: frame seq 没有推进", steps, details, startedAt)
	}
	if len(image) == 0 {
		steps = append(steps, infrastructureStep("fresh HDMI frame", "error", "empty image payload", time.Since(captureStarted)))
		return infrastructureTestFailure("hdmi", "HDMI 截图为空", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("fresh HDMI frame", "ok", fmt.Sprintf("seq %d -> %d", sinceSeq, meta.Seq), time.Since(captureStarted)))

	healthAfterStarted := time.Now()
	if healthAfter, err := client.Health(); err != nil {
		steps = append(steps, infrastructureStep("frame_service post health", "warn", err.Error(), time.Since(healthAfterStarted)))
	} else {
		details["health_after"] = healthAfter
		steps = append(steps, infrastructureStep("frame_service post health", "ok", formatFrameHealthSummary(healthAfter), time.Since(healthAfterStarted)))
	}

	result := infrastructureTestSuccess("hdmi", "HDMI 实时截图成功", steps, details, startedAt)
	result.Width = int(meta.Width)
	result.Height = int(meta.Height)
	result.SourceWidth = int(meta.SourceWidth)
	result.SourceHeight = int(meta.SourceHeight)
	result.Format = "jpeg"
	result.Size = len(image)
	result.Data = base64.StdEncoding.EncodeToString(image)
	return result
}

func runInfrastructureAudioRecordTest(ctx context.Context, body []byte, client infrastructureAudioClient, format AudioFormat, startedAt time.Time) infrastructureTestResponse {
	_, duration, err := parseInfrastructureAudioRequest(body)
	if err != nil {
		return infrastructureTestFailure("audio-record", err.Error(), nil, nil, startedAt)
	}
	if client == nil {
		return infrastructureTestFailure("audio-record", "audio_service client is not configured", nil, nil, startedAt)
	}

	steps := make([]infrastructureTestStep, 0, 6)
	details := infrastructureAudioDetails(format, duration)

	healthStarted := time.Now()
	healthBefore, err := client.Health()
	if err != nil {
		steps = append(steps, infrastructureStep("audio_service health", "error", err.Error(), time.Since(healthStarted)))
		return infrastructureTestFailure("audio-record", "audio_service health failed", steps, details, startedAt)
	}
	details["health_before"] = healthBefore
	steps = append(steps, infrastructureStep("audio_service health", "ok", formatAudioHealthSummary(healthBefore), time.Since(healthStarted)))

	recordStarted := time.Now()
	recording, err := client.StartRecording(format)
	if err != nil {
		steps = append(steps, infrastructureStep("audio recording start", "error", err.Error(), time.Since(recordStarted)))
		return infrastructureTestFailure("audio-record", "audio recording start failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio recording start", "ok", fmt.Sprintf("session %d", recording.SessionID), time.Since(recordStarted)))

	pcm, readErr := readInfrastructureAudioPCM(ctx, client, recording.SessionID, duration)
	stopStarted := time.Now()
	stopErr := stopInfrastructureRecordingIgnoringEnded(client, recording.SessionID)
	stopDuration := time.Since(stopStarted)
	if stopErr != nil {
		steps = append(steps, infrastructureStep("audio recording stop", "error", stopErr.Error(), stopDuration))
	}
	if readErr != nil {
		steps = append(steps, infrastructureStep("audio recording read", "error", readErr.Error(), 0))
		return infrastructureTestFailure("audio-record", "audio recording read failed", steps, details, startedAt)
	}
	if stopErr != nil {
		return infrastructureTestFailure("audio-record", "audio recording stop failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio recording stop", "ok", "recording stopped", stopDuration))

	drainStarted := time.Now()
	drainedPCM, sawEOF, drainErr := drainInfrastructureRecordingAfterStop(ctx, client, recording.SessionID, infrastructureAudioStopDrainTimeout)
	if len(drainedPCM) > 0 {
		pcm = append(pcm, drainedPCM...)
	}
	if drainErr != nil {
		steps = append(steps, infrastructureStep("audio recording drain", "error", drainErr.Error(), time.Since(drainStarted)))
		return infrastructureTestFailure("audio-record", "audio recording drain failed", steps, details, startedAt)
	}
	if sawEOF {
		steps = append(steps, infrastructureStep("audio recording drain", "ok", "recording session reached EOF", time.Since(drainStarted)))
	} else {
		steps = append(steps, infrastructureStep("audio recording drain", "warn", "recording session EOF not observed before drain timeout", time.Since(drainStarted)))
	}
	if len(pcm) == 0 {
		steps = append(steps, infrastructureStep("audio recording read", "error", "no PCM bytes returned before timeout", 0))
		return infrastructureTestFailure("audio-record", "录音没有返回 PCM 数据", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio recording read", "ok", fmt.Sprintf("%d PCM bytes", len(pcm)), 0))
	details["recorded_pcm_bytes"] = len(pcm)

	stats, statsErr := analyzeInfrastructurePCM16(pcm, format)
	if stats.SampleCount > 0 {
		details["recorded_audio_stats"] = stats
	}
	if statsErr != nil {
		steps = append(steps, infrastructureStep("audio recording signal", "warn", statsErr.Error(), 0))
	} else {
		steps = append(steps, infrastructureStep("audio recording signal", "ok", fmt.Sprintf("peak=%d rms=%.1f nonzero=%d/%d", stats.PeakAbs, stats.RMS, stats.NonZeroSamples, stats.SampleCount), 0))
	}

	addInfrastructureAudioPostHealth(client, &steps, details)
	return infrastructureTestSuccess("audio-record", "录音链路成功，已读到 PCM 数据", steps, details, startedAt)
}

func runInfrastructureAudioPlaybackTest(ctx context.Context, body []byte, client infrastructureAudioClient, format AudioFormat, startedAt time.Time) infrastructureTestResponse {
	_, duration, err := parseInfrastructureAudioRequest(body)
	if err != nil {
		return infrastructureTestFailure("audio-playback", err.Error(), nil, nil, startedAt)
	}
	if client == nil {
		return infrastructureTestFailure("audio-playback", "audio_service client is not configured", nil, nil, startedAt)
	}

	steps := make([]infrastructureTestStep, 0, 7)
	details := infrastructureAudioDetails(format, duration)

	healthStarted := time.Now()
	healthBefore, err := client.Health()
	if err != nil {
		steps = append(steps, infrastructureStep("audio_service health", "error", err.Error(), time.Since(healthStarted)))
		return infrastructureTestFailure("audio-playback", "audio_service health failed", steps, details, startedAt)
	}
	details["health_before"] = healthBefore
	steps = append(steps, infrastructureStep("audio_service health", "ok", formatAudioHealthSummary(healthBefore), time.Since(healthStarted)))

	volumeStarted := time.Now()
	volume, err := client.GetPlaybackVolume()
	if err != nil {
		steps = append(steps, infrastructureStep("audio_service volume", "error", err.Error(), time.Since(volumeStarted)))
		return infrastructureTestFailure("audio-playback", "audio_service get-volume failed", steps, details, startedAt)
	}
	details["volume"] = volume
	steps = append(steps, infrastructureStep("audio_service volume", "ok", fmt.Sprintf("%d", volume), time.Since(volumeStarted)))

	playbackPCM, err := generateInfrastructurePlaybackPCM(format, duration)
	if err != nil {
		steps = append(steps, infrastructureStep("audio playback tone", "error", err.Error(), 0))
		return infrastructureTestFailure("audio-playback", "audio playback test tone failed", steps, details, startedAt)
	}
	details["playback_pcm_bytes"] = len(playbackPCM)
	details["tone_hz"] = 660

	playbackStarted := time.Now()
	playback, err := client.StartPlayback(format)
	if err != nil {
		steps = append(steps, infrastructureStep("audio playback start", "error", err.Error(), time.Since(playbackStarted)))
		return infrastructureTestFailure("audio-playback", "audio playback start failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio playback start", "ok", fmt.Sprintf("session %d", playback.SessionID), time.Since(playbackStarted)))

	writeStarted := time.Now()
	if err := writeInfrastructureAudioPCM(ctx, client, playback.SessionID, playbackPCM); err != nil {
		_ = stopInfrastructurePlaybackIgnoringEnded(client, playback.SessionID)
		steps = append(steps, infrastructureStep("audio playback write", "error", err.Error(), time.Since(writeStarted)))
		return infrastructureTestFailure("audio-playback", "audio playback write failed", steps, details, startedAt)
	}
	if err := client.WritePlayChunk(playback.SessionID, nil, true); err != nil {
		_ = stopInfrastructurePlaybackIgnoringEnded(client, playback.SessionID)
		steps = append(steps, infrastructureStep("audio playback write", "error", err.Error(), time.Since(writeStarted)))
		return infrastructureTestFailure("audio-playback", "audio playback final chunk failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio playback write", "ok", fmt.Sprintf("%d PCM bytes", len(playbackPCM)), time.Since(writeStarted)))

	drainStarted := time.Now()
	if err := waitInfrastructurePlaybackDrain(ctx, client, infrastructureAudioDrainTimeout); err != nil {
		_ = stopInfrastructurePlaybackIgnoringEnded(client, playback.SessionID)
		steps = append(steps, infrastructureStep("audio playback drain", "error", err.Error(), time.Since(drainStarted)))
		return infrastructureTestFailure("audio-playback", "audio playback drain failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio playback drain", "ok", "playback drained", time.Since(drainStarted)))

	addInfrastructureAudioPostHealth(client, &steps, details)
	return infrastructureTestSuccess("audio-playback", "播放链路成功", steps, details, startedAt)
}

func runInfrastructureAudioTest(ctx context.Context, body []byte, client infrastructureAudioClient, format AudioFormat, startedAt time.Time) infrastructureTestResponse {
	req, duration, err := parseInfrastructureAudioRequest(body)
	if err != nil {
		return infrastructureTestFailure("audio", err.Error(), nil, nil, startedAt)
	}
	playbackEnabled := true
	if req.Playback != nil {
		playbackEnabled = *req.Playback
	}
	if client == nil {
		return infrastructureTestFailure("audio", "audio_service client is not configured", nil, nil, startedAt)
	}

	steps := make([]infrastructureTestStep, 0, 8)
	details := infrastructureAudioDetails(format, duration)
	details["playback"] = playbackEnabled

	healthStarted := time.Now()
	healthBefore, err := client.Health()
	if err != nil {
		steps = append(steps, infrastructureStep("audio_service health", "error", err.Error(), time.Since(healthStarted)))
		return infrastructureTestFailure("audio", "audio_service health failed", steps, details, startedAt)
	}
	details["health_before"] = healthBefore
	steps = append(steps, infrastructureStep("audio_service health", "ok", formatAudioHealthSummary(healthBefore), time.Since(healthStarted)))

	volumeStarted := time.Now()
	volume, err := client.GetPlaybackVolume()
	if err != nil {
		steps = append(steps, infrastructureStep("audio_service volume", "error", err.Error(), time.Since(volumeStarted)))
		return infrastructureTestFailure("audio", "audio_service get-volume failed", steps, details, startedAt)
	}
	details["volume"] = volume
	steps = append(steps, infrastructureStep("audio_service volume", "ok", fmt.Sprintf("%d", volume), time.Since(volumeStarted)))

	recordStarted := time.Now()
	recording, err := client.StartRecording(format)
	if err != nil {
		steps = append(steps, infrastructureStep("audio recording start", "error", err.Error(), time.Since(recordStarted)))
		return infrastructureTestFailure("audio", "audio recording start failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio recording start", "ok", fmt.Sprintf("session %d", recording.SessionID), time.Since(recordStarted)))

	pcm, readErr := readInfrastructureAudioPCM(ctx, client, recording.SessionID, duration)
	stopStarted := time.Now()
	stopErr := stopInfrastructureRecordingIgnoringEnded(client, recording.SessionID)
	stopDuration := time.Since(stopStarted)
	if stopErr != nil {
		steps = append(steps, infrastructureStep("audio recording stop", "error", stopErr.Error(), stopDuration))
	}
	if readErr != nil {
		steps = append(steps, infrastructureStep("audio recording read", "error", readErr.Error(), 0))
		return infrastructureTestFailure("audio", "audio recording read failed", steps, details, startedAt)
	}
	if stopErr != nil {
		return infrastructureTestFailure("audio", "audio recording stop failed", steps, details, startedAt)
	}
	steps = append(steps, infrastructureStep("audio recording stop", "ok", "recording stopped", stopDuration))

	drainStarted := time.Now()
	drainedPCM, sawEOF, drainErr := drainInfrastructureRecordingAfterStop(ctx, client, recording.SessionID, infrastructureAudioStopDrainTimeout)
	if len(drainedPCM) > 0 {
		pcm = append(pcm, drainedPCM...)
	}
	if drainErr != nil {
		steps = append(steps, infrastructureStep("audio recording drain", "error", drainErr.Error(), time.Since(drainStarted)))
		return infrastructureTestFailure("audio", "audio recording drain failed", steps, details, startedAt)
	}
	if sawEOF {
		steps = append(steps, infrastructureStep("audio recording drain", "ok", "recording session reached EOF", time.Since(drainStarted)))
	} else {
		steps = append(steps, infrastructureStep("audio recording drain", "warn", "recording session EOF not observed before drain timeout", time.Since(drainStarted)))
	}
	if len(pcm) == 0 {
		steps = append(steps, infrastructureStep("audio recording read", "warn", "no PCM bytes returned before timeout", 0))
	} else {
		steps = append(steps, infrastructureStep("audio recording read", "ok", fmt.Sprintf("%d PCM bytes", len(pcm)), 0))
	}
	details["recorded_pcm_bytes"] = len(pcm)
	if len(pcm) > 0 {
		if stats, err := analyzeInfrastructurePCM16(pcm, format); err == nil {
			details["recorded_audio_stats"] = stats
		}
	}

	if playbackEnabled {
		playbackStarted := time.Now()
		playback, err := client.StartPlayback(format)
		if err != nil {
			steps = append(steps, infrastructureStep("audio playback start", "error", err.Error(), time.Since(playbackStarted)))
			return infrastructureTestFailure("audio", "audio playback start failed", steps, details, startedAt)
		}
		steps = append(steps, infrastructureStep("audio playback start", "ok", fmt.Sprintf("session %d", playback.SessionID), time.Since(playbackStarted)))

		playbackPCM := pcm
		if len(playbackPCM) == 0 {
			playbackPCM = make([]byte, int(format.SampleRate)*int(format.Channels)*int(format.BitWidth/8)/10)
		}
		writeStarted := time.Now()
		if err := writeInfrastructureAudioPCM(ctx, client, playback.SessionID, playbackPCM); err != nil {
			_ = stopInfrastructurePlaybackIgnoringEnded(client, playback.SessionID)
			steps = append(steps, infrastructureStep("audio playback write", "error", err.Error(), time.Since(writeStarted)))
			return infrastructureTestFailure("audio", "audio playback write failed", steps, details, startedAt)
		}
		if err := client.WritePlayChunk(playback.SessionID, nil, true); err != nil {
			_ = stopInfrastructurePlaybackIgnoringEnded(client, playback.SessionID)
			steps = append(steps, infrastructureStep("audio playback write", "error", err.Error(), time.Since(writeStarted)))
			return infrastructureTestFailure("audio", "audio playback final chunk failed", steps, details, startedAt)
		}
		steps = append(steps, infrastructureStep("audio playback write", "ok", fmt.Sprintf("%d PCM bytes", len(playbackPCM)), time.Since(writeStarted)))

		drainStarted := time.Now()
		if err := waitInfrastructurePlaybackDrain(ctx, client, infrastructureAudioDrainTimeout); err != nil {
			_ = stopInfrastructurePlaybackIgnoringEnded(client, playback.SessionID)
			steps = append(steps, infrastructureStep("audio playback drain", "error", err.Error(), time.Since(drainStarted)))
			return infrastructureTestFailure("audio", "audio playback drain failed", steps, details, startedAt)
		}
		steps = append(steps, infrastructureStep("audio playback drain", "ok", "playback drained", time.Since(drainStarted)))
	}

	addInfrastructureAudioPostHealth(client, &steps, details)

	return infrastructureTestSuccess("audio", "语音/音频服务录放链路成功", steps, details, startedAt)
}

func parseInfrastructureAudioRequest(body []byte) (infrastructureAudioRequest, time.Duration, error) {
	var req infrastructureAudioRequest
	if err := decodeInfrastructureJSON(body, &req); err != nil {
		return req, 0, err
	}
	duration := infrastructureAudioDefaultDuration
	if req.DurationMS > 0 {
		duration = time.Duration(req.DurationMS) * time.Millisecond
	}
	if duration < 200*time.Millisecond || duration > infrastructureAudioMaxDuration {
		return req, 0, fmt.Errorf("duration_ms must be between 200 and 3000")
	}
	return req, duration, nil
}

func infrastructureAudioDetails(format AudioFormat, duration time.Duration) map[string]any {
	return map[string]any{
		"duration_ms": int(duration / time.Millisecond),
		"format": map[string]any{
			"sample_rate": format.SampleRate,
			"channels":    format.Channels,
			"bit_width":   format.BitWidth,
		},
	}
}

func addInfrastructureAudioPostHealth(client infrastructureAudioClient, steps *[]infrastructureTestStep, details map[string]any) {
	healthAfterStarted := time.Now()
	healthAfter, err := client.Health()
	if err != nil {
		*steps = append(*steps, infrastructureStep("audio_service post health", "warn", err.Error(), time.Since(healthAfterStarted)))
		return
	}
	details["health_after"] = healthAfter
	status := "ok"
	if healthAfter.RecordingActive || healthAfter.RecordSessions > 0 || healthAfter.PlaybackActive || healthAfter.PlaybackSessions > 0 {
		status = "warn"
	}
	*steps = append(*steps, infrastructureStep("audio_service post health", status, formatAudioHealthSummary(healthAfter), time.Since(healthAfterStarted)))
}

func generateInfrastructurePlaybackPCM(format AudioFormat, duration time.Duration) ([]byte, error) {
	if format.SampleRate == 0 || format.Channels == 0 || format.BitWidth != 16 {
		return nil, fmt.Errorf("only PCM16 playback test tone is supported")
	}
	totalFrames := int((uint64(format.SampleRate) * uint64(duration/time.Millisecond)) / 1000)
	if totalFrames <= 0 {
		return nil, fmt.Errorf("playback duration produced no samples")
	}
	channels := int(format.Channels)
	out := make([]byte, totalFrames*channels*2)
	const (
		frequency = 660.0
		amplitude = 0.20 * 32767.0
	)
	for frame := 0; frame < totalFrames; frame++ {
		sample := int16(math.Sin(2*math.Pi*frequency*float64(frame)/float64(format.SampleRate)) * amplitude)
		value := uint16(sample)
		for ch := 0; ch < channels; ch++ {
			offset := (frame*channels + ch) * 2
			out[offset] = byte(value)
			out[offset+1] = byte(value >> 8)
		}
	}
	return out, nil
}

func analyzeInfrastructurePCM16(pcm []byte, format AudioFormat) (infrastructureAudioContentStats, error) {
	var stats infrastructureAudioContentStats
	if format.BitWidth != 16 {
		return stats, fmt.Errorf("only PCM16 recording content validation is supported")
	}
	if format.Channels == 0 || format.SampleRate == 0 {
		return stats, fmt.Errorf("invalid audio format")
	}
	sampleCount := len(pcm) / 2
	if sampleCount == 0 {
		return stats, fmt.Errorf("no complete PCM16 samples")
	}

	var sumAbs float64
	var sumSquares float64
	for i := 0; i+1 < len(pcm); i += 2 {
		raw := uint16(pcm[i]) | uint16(pcm[i+1])<<8
		sample := int(int16(raw))
		abs := sample
		if abs < 0 {
			abs = -abs
		}
		if abs > stats.PeakAbs {
			stats.PeakAbs = abs
		}
		if sample != 0 {
			stats.NonZeroSamples++
		}
		sumAbs += float64(abs)
		sumSquares += float64(sample) * float64(sample)
	}

	stats.SampleCount = sampleCount
	stats.MeanAbs = sumAbs / float64(sampleCount)
	stats.RMS = math.Sqrt(sumSquares / float64(sampleCount))
	return stats, nil
}

func decodeInfrastructureJSON(body []byte, out any) error {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid request JSON: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid request JSON: expected exactly one JSON object")
	}
	return nil
}

func normalizeInfrastructureHIDMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return "legacy"
	case "click", "tap":
		return "click"
	case "input", "keyboard", "type":
		return "input"
	case "legacy", "combined", "both":
		return "legacy"
	default:
		return ""
	}
}

func infrastructureHIDModeSendsClick(req infrastructureHIDRequest, mode string) bool {
	switch mode {
	case "click":
		return true
	case "input":
		return false
	default:
		clickEnabled := true
		if req.Click != nil {
			clickEnabled = *req.Click
		}
		return clickEnabled
	}
}

func normalizeInfrastructureHIDKeySequences(req infrastructureHIDRequest, mode string) [][]string {
	switch mode {
	case "click":
		return nil
	case "input":
		keys := normalizeInfrastructureHIDKeysWithDefault(req, infrastructureHIDInputKey)
		if len(keys) == 0 {
			keys = []string{infrastructureHIDInputKey}
		}
		return [][]string{keys, []string{infrastructureHIDEnterKey}}
	default:
		keys := normalizeInfrastructureHIDKeysWithDefault(req, infrastructureHIDDefaultKey)
		if len(keys) == 0 {
			return nil
		}
		return [][]string{keys}
	}
}

func normalizeInfrastructureHIDKeys(req infrastructureHIDRequest) []string {
	return normalizeInfrastructureHIDKeysWithDefault(req, infrastructureHIDDefaultKey)
}

func normalizeInfrastructureHIDKeysWithDefault(req infrastructureHIDRequest, defaultKey string) []string {
	if len(req.Keys) > 0 {
		keys := make([]string, 0, len(req.Keys))
		for _, key := range req.Keys {
			key = strings.ToLower(strings.TrimSpace(key))
			if key != "" {
				keys = append(keys, key)
			}
		}
		return keys
	}
	key := strings.ToLower(strings.TrimSpace(req.Key))
	if key == "" {
		key = defaultKey
	}
	return []string{key}
}

func infrastructureHIDSuccessMessage(mode string) string {
	switch mode {
	case "click":
		return "HID 已发送真实点击报告"
	case "input":
		return "HID 已发送真实键盘输入 h 和 enter"
	default:
		return "HID 已发送真实键盘/点击报告"
	}
}

func invalidInfrastructureCoordinate(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1000
}

func checkInfrastructureDevicePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	return nil
}

func readInfrastructureUDC() (string, error) {
	data, err := os.ReadFile("/sys/kernel/config/usb_gadget/aiden_hid/UDC")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func infrastructureInterfaceAddresses(name string) ([]string, bool) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, false
	}
	addrs, err := iface.Addrs()
	if err != nil || len(addrs) == 0 {
		return nil, false
	}
	result := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		result = append(result, addr.String())
	}
	return result, len(result) > 0
}

func readInfrastructureAudioPCM(ctx context.Context, client infrastructureAudioClient, sessionID uint64, duration time.Duration) ([]byte, error) {
	deadline := time.Now().Add(duration)
	var pcm []byte
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		timeout := infrastructureAudioReadPoll
		if remaining < timeout {
			timeout = remaining
		}
		if timeout < time.Millisecond {
			break
		}
		timeoutMs := uint32(timeout / time.Millisecond)
		if timeoutMs == 0 {
			timeoutMs = 1
		}
		chunk, err := client.ReadRecordChunk(sessionID, timeoutMs)
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		if len(chunk.PCM) > 0 {
			pcm = append(pcm, chunk.PCM...)
		}
		if chunk.EndOfStream {
			break
		}
	}
	return pcm, nil
}

func drainInfrastructureRecordingAfterStop(ctx context.Context, client infrastructureAudioClient, sessionID uint64, timeout time.Duration) ([]byte, bool, error) {
	if timeout <= 0 {
		return nil, false, nil
	}
	deadline := time.Now().Add(timeout)
	var pcm []byte
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		remaining := time.Until(deadline)
		poll := infrastructureAudioReadPoll
		if remaining < poll {
			poll = remaining
		}
		if poll < time.Millisecond {
			break
		}
		timeoutMs := uint32(poll / time.Millisecond)
		if timeoutMs == 0 {
			timeoutMs = 1
		}
		chunk, err := client.ReadRecordChunk(sessionID, timeoutMs)
		if err != nil {
			if infrastructureAudioSessionAlreadyGone(err) {
				return pcm, true, nil
			}
			return pcm, false, err
		}
		if chunk == nil {
			continue
		}
		if len(chunk.PCM) > 0 {
			pcm = append(pcm, chunk.PCM...)
		}
		if chunk.EndOfStream {
			return pcm, true, nil
		}
	}
	return pcm, false, nil
}

func stopInfrastructureRecordingIgnoringEnded(client infrastructureAudioClient, sessionID uint64) error {
	err := client.StopRecording(sessionID)
	if err == nil {
		return nil
	}
	if infrastructureAudioSessionAlreadyGone(err) {
		return nil
	}
	return err
}

func infrastructureAudioSessionAlreadyGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "session_not_found") ||
		strings.Contains(msg, "not_found") ||
		strings.Contains(msg, "not found") {
		return true
	}
	return false
}

func writeInfrastructureAudioPCM(ctx context.Context, client infrastructureAudioClient, sessionID uint64, pcm []byte) error {
	for off := 0; off < len(pcm); off += playbackChunkBytes {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := off + playbackChunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := client.WritePlayChunk(sessionID, pcm[off:end], false); err != nil {
			return err
		}
	}
	return nil
}

func waitInfrastructurePlaybackDrain(ctx context.Context, client infrastructureAudioClient, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(playbackDrainPollInterval)
	defer ticker.Stop()

	for {
		health, err := client.Health()
		if err != nil {
			return err
		}
		if health.PlaybackSessions == 0 || !health.PlaybackActive {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func stopInfrastructurePlaybackIgnoringEnded(client infrastructureAudioClient, sessionID uint64) error {
	err := client.StopPlayback(sessionID)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "session_not_found") ||
		strings.Contains(msg, "not_found") ||
		strings.Contains(msg, "not found") {
		return nil
	}
	return err
}

func formatFrameHealthSummary(health *screenprovider.HealthResult) string {
	if health == nil {
		return "missing health"
	}
	return fmt.Sprintf("state=%s mode=%s seq=%d age_ms=%d failures=%d error=%s",
		health.State,
		health.CaptureMode,
		health.LatestSeq,
		health.FrameAgeMs,
		health.ConsecutiveFailures,
		health.LastError,
	)
}

func formatAudioHealthSummary(health *AudioHealthResult) string {
	if health == nil {
		return "missing health"
	}
	return fmt.Sprintf("recording_active=%v playback_active=%v record_sessions=%d playback_sessions=%d",
		health.RecordingActive,
		health.PlaybackActive,
		health.RecordSessions,
		health.PlaybackSessions,
	)
}

func infrastructureStep(name, status, message string, duration time.Duration) infrastructureTestStep {
	step := infrastructureTestStep{
		Name:    name,
		Status:  status,
		Message: message,
	}
	if duration > 0 {
		step.DurationMS = duration.Milliseconds()
	}
	return step
}

func infrastructureTestSuccess(target, message string, steps []infrastructureTestStep, details map[string]any, startedAt time.Time) infrastructureTestResponse {
	return infrastructureTestResponse{
		OK:         true,
		Target:     target,
		Message:    message,
		Steps:      steps,
		Details:    details,
		DurationMS: time.Since(startedAt).Milliseconds(),
		Timestamp:  time.Now(),
	}
}

func infrastructureTestFailure(target, message string, steps []infrastructureTestStep, details map[string]any, startedAt time.Time) infrastructureTestResponse {
	return infrastructureTestResponse{
		OK:         false,
		Target:     target,
		Message:    message,
		Error:      message,
		Steps:      steps,
		Details:    details,
		DurationMS: time.Since(startedAt).Milliseconds(),
		Timestamp:  time.Now(),
	}
}

func writeInfrastructureJSON(w http.ResponseWriter, statusCode int, payload infrastructureTestResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
