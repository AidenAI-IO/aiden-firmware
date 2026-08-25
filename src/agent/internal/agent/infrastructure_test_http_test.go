package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aiden-agent/internal/agent/screenprovider"
)

type recordingInfrastructureMNKProvider struct {
	keypresses [][]string
	clicks     []recordingInfrastructureClick
}

type recordingInfrastructureClick struct {
	X, Y   float64
	Button string
	HoldMs int
}

func (p *recordingInfrastructureMNKProvider) Click(_ context.Context, x, y float64, button string, holdMs int) error {
	p.clicks = append(p.clicks, recordingInfrastructureClick{X: x, Y: y, Button: button, HoldMs: holdMs})
	return nil
}

func (p *recordingInfrastructureMNKProvider) DoubleClick(context.Context, float64, float64, string) error {
	return nil
}

func (p *recordingInfrastructureMNKProvider) Swipe(context.Context, [][2]float64, string) error {
	return nil
}

func (p *recordingInfrastructureMNKProvider) Drag(context.Context, [][2]float64, string) error {
	return nil
}

func (p *recordingInfrastructureMNKProvider) Keypress(_ context.Context, keys []string) error {
	p.keypresses = append(p.keypresses, append([]string(nil), keys...))
	return nil
}

func (p *recordingInfrastructureMNKProvider) Move(context.Context, float64, float64) error {
	return nil
}

func (p *recordingInfrastructureMNKProvider) Scroll(context.Context, int, int) error {
	return nil
}

type fakeInfrastructureFrameClient struct {
	health       *screenprovider.HealthResult
	frame        *screenprovider.FrameMetadata
	frameData    []byte
	frameErr     error
	lastSinceSeq uint64
	lastTimeout  time.Duration
}

func (f *fakeInfrastructureFrameClient) Health() (*screenprovider.HealthResult, error) {
	if f.health == nil {
		return &screenprovider.HealthResult{State: "RUNNING", CaptureMode: "on_demand"}, nil
	}
	snapshot := *f.health
	return &snapshot, nil
}

func (f *fakeInfrastructureFrameClient) LatestFrameWithFormatSince(_ string, _ int, _ bool, _ int, sinceSeq uint64, timeout time.Duration) (*screenprovider.FrameMetadata, []byte, error) {
	f.lastSinceSeq = sinceSeq
	f.lastTimeout = timeout
	if f.frameErr != nil {
		return nil, nil, f.frameErr
	}
	if f.frame == nil {
		return nil, nil, fmt.Errorf("frame missing")
	}
	snapshot := *f.frame
	return &snapshot, append([]byte(nil), f.frameData...), nil
}

type fakeInfrastructureAudioClient struct {
	health              *AudioHealthResult
	volume              int
	recordChunk         []byte
	playbackActive      bool
	recordingActive     bool
	readCalled          bool
	recordingStopped    bool
	playbackStopped     bool
	recordSessionID     uint64
	playbackSessionID   uint64
	getVolumeCalls      int
	startRecordingCalls int
	startPlaybackCalls  int
	writePlayChunks     int
	finalPlayChunks     int
}

func (c *fakeInfrastructureAudioClient) Health() (*AudioHealthResult, error) {
	if c.health != nil {
		snapshot := *c.health
		snapshot.RecordingActive = c.recordingActive
		snapshot.PlaybackActive = c.playbackActive
		if c.recordingActive {
			snapshot.RecordSessions = 1
		} else {
			snapshot.RecordSessions = 0
		}
		if c.playbackActive {
			snapshot.PlaybackSessions = 1
		} else {
			snapshot.PlaybackSessions = 0
		}
		return &snapshot, nil
	}
	return &AudioHealthResult{
		RecordingActive:  c.recordingActive,
		PlaybackActive:   c.playbackActive,
		RecordSessions:   boolToUint32(c.recordingActive),
		PlaybackSessions: boolToUint32(c.playbackActive),
	}, nil
}

func (c *fakeInfrastructureAudioClient) GetPlaybackVolume() (int, error) {
	c.getVolumeCalls++
	return c.volume, nil
}

func (c *fakeInfrastructureAudioClient) StartRecording(_ AudioFormat) (*RecordStartResult, error) {
	c.startRecordingCalls++
	c.recordingActive = true
	if c.recordSessionID == 0 {
		c.recordSessionID = 101
	}
	return &RecordStartResult{SessionID: c.recordSessionID}, nil
}

func (c *fakeInfrastructureAudioClient) ReadRecordChunk(_ uint64, _ uint32) (*AudioChunkResult, error) {
	if c.readCalled {
		return &AudioChunkResult{EndOfStream: true}, nil
	}
	c.readCalled = true
	return &AudioChunkResult{PCM: append([]byte(nil), c.recordChunk...), EndOfStream: false}, nil
}

func (c *fakeInfrastructureAudioClient) StopRecording(_ uint64) error {
	c.recordingActive = false
	c.recordingStopped = true
	return nil
}

func (c *fakeInfrastructureAudioClient) StartPlayback(_ AudioFormat) (*PlaybackStartResult, error) {
	c.startPlaybackCalls++
	c.playbackActive = true
	if c.playbackSessionID == 0 {
		c.playbackSessionID = 202
	}
	return &PlaybackStartResult{SessionID: c.playbackSessionID}, nil
}

func (c *fakeInfrastructureAudioClient) WritePlayChunk(_ uint64, _ []byte, isFinal bool) error {
	c.writePlayChunks++
	if isFinal {
		c.finalPlayChunks++
		c.playbackActive = false
	}
	return nil
}

func (c *fakeInfrastructureAudioClient) StopPlayback(_ uint64) error {
	c.playbackActive = false
	c.playbackStopped = true
	return nil
}

func TestInfrastructureTestHIDRouteSendsRealHIDReports(t *testing.T) {
	dir := t.TempDir()
	keyboardPath := writeTempInfrastructureDevice(t, dir, "keyboard")
	mousePath := writeTempInfrastructureDevice(t, dir, "mouse")
	androidPath := writeTempInfrastructureDevice(t, dir, "android")
	provider := &recordingInfrastructureMNKProvider{}
	runtime := &Runtime{
		config: Config{
			HID: HIDConfig{
				KeyboardDevice:        keyboardPath,
				MouseDevice:           mousePath,
				AndroidKeyboardDevice: androidPath,
			},
		},
		tools: &ToolSet{mnkProvider: provider},
	}
	server := &Server{runtime: runtime}

	req := infrastructureTestUSBUIRequest(http.MethodPost, "/api/infrastructure-test/hid", strings.NewReader(`{"key":"escape","click":true,"x":500,"y":500,"button":"left","hold_ms":80}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp infrastructureTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Target != "hid" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(provider.keypresses) != 1 || len(provider.clicks) != 1 {
		t.Fatalf("keypresses=%v clicks=%v", provider.keypresses, provider.clicks)
	}
	if got := provider.keypresses[0]; len(got) != 1 || got[0] != "escape" {
		t.Fatalf("keypresses=%v, want escape", provider.keypresses)
	}
	if got := provider.clicks[0]; got.X != 500 || got.Y != 500 || got.Button != "left" || got.HoldMs != 80 {
		t.Fatalf("click = %#v", got)
	}
}

func TestInfrastructureTestHIDInputModeSendsHThenEnter(t *testing.T) {
	dir := t.TempDir()
	keyboardPath := writeTempInfrastructureDevice(t, dir, "keyboard")
	androidPath := writeTempInfrastructureDevice(t, dir, "android")
	provider := &recordingInfrastructureMNKProvider{}
	runtime := &Runtime{
		config: Config{
			HID: HIDConfig{
				KeyboardDevice:        keyboardPath,
				AndroidKeyboardDevice: androidPath,
			},
		},
		tools: &ToolSet{mnkProvider: provider},
	}
	server := &Server{runtime: runtime}

	req := infrastructureTestUSBUIRequest(http.MethodPost, "/api/infrastructure-test/hid", strings.NewReader(`{"mode":"input"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp infrastructureTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Target != "hid" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	want := [][]string{{"h"}, {"enter"}}
	if fmt.Sprint(provider.keypresses) != fmt.Sprint(want) {
		t.Fatalf("keypresses=%v, want %v", provider.keypresses, want)
	}
	if len(provider.clicks) != 0 {
		t.Fatalf("input mode sent click: %v", provider.clicks)
	}
}

func TestInfrastructureTestHIDClickModeOnlyClicks(t *testing.T) {
	dir := t.TempDir()
	mousePath := writeTempInfrastructureDevice(t, dir, "mouse")
	androidPath := writeTempInfrastructureDevice(t, dir, "android")
	provider := &recordingInfrastructureMNKProvider{}
	runtime := &Runtime{
		config: Config{
			HID: HIDConfig{
				MouseDevice:           mousePath,
				AndroidKeyboardDevice: androidPath,
			},
		},
		tools: &ToolSet{mnkProvider: provider},
	}
	server := &Server{runtime: runtime}

	req := infrastructureTestUSBUIRequest(http.MethodPost, "/api/infrastructure-test/hid", strings.NewReader(`{"mode":"click","x":250,"y":750}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp infrastructureTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Target != "hid" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(provider.keypresses) != 0 {
		t.Fatalf("click mode sent keypress: %v", provider.keypresses)
	}
	if len(provider.clicks) != 1 {
		t.Fatalf("clicks=%v, want 1", provider.clicks)
	}
	if got := provider.clicks[0]; got.X != 250 || got.Y != 750 || got.Button != "left" {
		t.Fatalf("click = %#v", got)
	}
}

func TestInfrastructureTestHDMIRequiresFreshFrame(t *testing.T) {
	client := &fakeInfrastructureFrameClient{
		health: &screenprovider.HealthResult{
			State:       "RUNNING",
			CaptureMode: "on_demand",
			LatestSeq:   10,
		},
		frame: &screenprovider.FrameMetadata{
			Seq:         10,
			Width:       2,
			Height:      1,
			PixelFormat: "jpeg",
			Bytes:       3,
		},
		frameData: []byte{1, 2, 3},
	}

	resp := runInfrastructureHDMITest(context.Background(), []byte(`{}`), client, time.Now())
	if resp.OK {
		t.Fatalf("expected HDMI freshness failure, got %#v", resp)
	}
	if !strings.Contains(resp.Message, "实时") {
		t.Fatalf("message = %q, want realtime failure", resp.Message)
	}
	if client.lastSinceSeq != 10 {
		t.Fatalf("since_seq = %d, want 10", client.lastSinceSeq)
	}
}

func TestInfrastructureTestHDMISucceedsWithFreshFrame(t *testing.T) {
	client := &fakeInfrastructureFrameClient{
		health: &screenprovider.HealthResult{
			State:       "RUNNING",
			CaptureMode: "on_demand",
			LatestSeq:   10,
		},
		frame: &screenprovider.FrameMetadata{
			Seq:         11,
			Width:       2,
			Height:      1,
			PixelFormat: "jpeg",
			Bytes:       3,
		},
		frameData: []byte{1, 2, 3},
	}

	resp := runInfrastructureHDMITest(context.Background(), []byte(`{"timeout_ms":500,"quality":80}`), client, time.Now())
	if !resp.OK {
		t.Fatalf("expected HDMI success, got %#v", resp)
	}
	if resp.Data == "" || resp.Width != 2 || resp.Height != 1 || resp.Size != 3 {
		t.Fatalf("unexpected HDMI response: %#v", resp)
	}
	if client.lastSinceSeq != 10 {
		t.Fatalf("since_seq = %d, want 10", client.lastSinceSeq)
	}
	if client.lastTimeout <= 0 {
		t.Fatalf("timeout = %s, want positive", client.lastTimeout)
	}
}

func TestInfrastructureTestAudioSuccess(t *testing.T) {
	client := &fakeInfrastructureAudioClient{
		health:      &AudioHealthResult{},
		volume:      42,
		recordChunk: makeNonSilentPCMChunk(1600, 2000),
	}

	resp := runInfrastructureAudioTest(context.Background(), []byte(`{"duration_ms":1000,"playback":true}`), client, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}, time.Now())
	if !resp.OK {
		t.Fatalf("expected audio success, got %#v", resp)
	}
	if resp.Target != "audio" {
		t.Fatalf("unexpected target: %q", resp.Target)
	}
	if len(resp.Steps) == 0 {
		t.Fatalf("expected steps in response: %#v", resp)
	}
	if got, ok := resp.Details["recorded_pcm_bytes"].(int); !ok || got != len(client.recordChunk) {
		t.Fatalf("recorded_pcm_bytes = %#v, want %d", resp.Details["recorded_pcm_bytes"], len(client.recordChunk))
	}
	if !client.recordingStopped {
		t.Fatal("expected recording to stop")
	}
	stats, ok := resp.Details["recorded_audio_stats"].(infrastructureAudioContentStats)
	if !ok {
		t.Fatalf("recorded_audio_stats missing or wrong type: %#v", resp.Details["recorded_audio_stats"])
	}
	if stats.PeakAbs == 0 || stats.RMS == 0 {
		t.Fatalf("recorded_audio_stats looks silent: %#v", stats)
	}
}

func TestInfrastructureTestAudioRecordRouteOnlyRecords(t *testing.T) {
	client := &fakeInfrastructureAudioClient{
		health:      &AudioHealthResult{},
		volume:      42,
		recordChunk: makeNonSilentPCMChunk(1600, 2000),
	}

	resp := runInfrastructureAudioRecordTest(context.Background(), []byte(`{"duration_ms":1000}`), client, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}, time.Now())
	if !resp.OK || resp.Target != "audio-record" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if client.startRecordingCalls != 1 {
		t.Fatalf("start recording calls = %d, want 1", client.startRecordingCalls)
	}
	if client.startPlaybackCalls != 0 || client.getVolumeCalls != 0 || client.writePlayChunks != 0 {
		t.Fatalf("record route touched playback path: startPlayback=%d getVolume=%d writes=%d", client.startPlaybackCalls, client.getVolumeCalls, client.writePlayChunks)
	}
	if got, ok := resp.Details["recorded_pcm_bytes"].(int); !ok || got != len(client.recordChunk) {
		t.Fatalf("recorded_pcm_bytes = %#v, want %d", resp.Details["recorded_pcm_bytes"], len(client.recordChunk))
	}
}

func TestInfrastructureTestAudioRecordRouteHandlesNilAudioClient(t *testing.T) {
	runtime := &Runtime{config: Config{}}
	server := &Server{runtime: runtime}

	req := infrastructureTestUSBUIRequest(http.MethodPost, "/api/infrastructure-test/audio-record", strings.NewReader(`{"duration_ms":1000}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp infrastructureTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || resp.Target != "audio-record" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if !strings.Contains(resp.Error, "audio_service client is not configured") {
		t.Fatalf("error = %q, want missing client", resp.Error)
	}
}

func TestInfrastructureTestRouteRejectsNonUSBAndCrossOriginRequests(t *testing.T) {
	server := &Server{runtime: &Runtime{config: Config{}}}

	nonUSB := httptest.NewRequest(http.MethodPost, "/api/infrastructure-test/audio-record", strings.NewReader(`{}`))
	nonUSB.RemoteAddr = "192.168.50.140:12345"
	nonUSB.Header.Set("Origin", "http://192.168.42.1:8080")
	nonUSB = nonUSB.WithContext(context.WithValue(
		nonUSB.Context(),
		http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.50.10"), Port: 8080},
	))
	nonUSBRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(nonUSBRec, nonUSB)
	if nonUSBRec.Code != http.StatusForbidden {
		t.Fatalf("non-USB status = %d body=%s, want 403", nonUSBRec.Code, nonUSBRec.Body.String())
	}

	crossOrigin := infrastructureTestUSBUIRequest(http.MethodPost, "/api/infrastructure-test/audio-record", strings.NewReader(`{}`))
	crossOrigin.Header.Set("Origin", "http://evil.example")
	crossOriginRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(crossOriginRec, crossOrigin)
	if crossOriginRec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d body=%s, want 403", crossOriginRec.Code, crossOriginRec.Body.String())
	}
}

func makeNonSilentPCMChunk(samples int, peak int16) []byte {
	if samples <= 0 {
		return nil
	}
	if peak <= 0 {
		peak = 2000
	}
	pcm := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		value := peak
		if i%2 == 1 {
			value = -peak
		}
		pcm[i*2] = byte(uint16(value))
		pcm[i*2+1] = byte(uint16(value) >> 8)
	}
	return pcm
}

func TestInfrastructureTestAudioRecordAcceptsSilentPCM(t *testing.T) {
	client := &fakeInfrastructureAudioClient{
		health:      &AudioHealthResult{},
		volume:      42,
		recordChunk: make([]byte, 4096),
	}

	resp := runInfrastructureAudioRecordTest(context.Background(), []byte(`{"duration_ms":1000}`), client, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}, time.Now())
	if !resp.OK {
		t.Fatalf("expected silent PCM to pass recording-link test, got %#v", resp)
	}
	if !strings.Contains(resp.Message, "PCM") {
		t.Fatalf("message = %q, want PCM success", resp.Message)
	}
}

func TestInfrastructureTestAudioPlaybackRouteOnlyPlaysTone(t *testing.T) {
	client := &fakeInfrastructureAudioClient{
		health: &AudioHealthResult{},
		volume: 42,
	}

	resp := runInfrastructureAudioPlaybackTest(context.Background(), []byte(`{"duration_ms":1000}`), client, AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}, time.Now())
	if !resp.OK || resp.Target != "audio-playback" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if client.startRecordingCalls != 0 {
		t.Fatalf("playback route touched recording path: startRecording=%d", client.startRecordingCalls)
	}
	if client.startPlaybackCalls != 1 {
		t.Fatalf("start playback calls = %d, want 1", client.startPlaybackCalls)
	}
	if client.getVolumeCalls != 1 {
		t.Fatalf("get volume calls = %d, want 1", client.getVolumeCalls)
	}
	if client.finalPlayChunks != 1 {
		t.Fatalf("final playback chunks = %d, want 1", client.finalPlayChunks)
	}
	if got, ok := resp.Details["playback_pcm_bytes"].(int); !ok || got == 0 {
		t.Fatalf("playback_pcm_bytes = %#v, want positive", resp.Details["playback_pcm_bytes"])
	}
}

func TestInfrastructureTestWebUIIncludesPanel(t *testing.T) {
	index := readWebUIResource(t, "index.html")
	for _, want := range []string{
		"基础设施测试",
		">点击<",
		">输出<",
		"data-infrastructure-target=\"hid-click\"",
		"data-infrastructure-target=\"hid-output\"",
		">录音<",
		">播放<",
		"id=\"infrastructureStatus\"",
		"/web-ui/scripts/infrastructure.js",
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("web ui missing %q", want)
		}
	}
	script := readWebUIResource(t, "scripts/infrastructure.js")
	for _, want := range []string{
		"hid-click",
		"hid-output",
		"/api/infrastructure-test/hid",
		"/api/infrastructure-test/hdmi",
		"/api/infrastructure-test/audio-record",
		"/api/infrastructure-test/audio-playback",
		`{ mode: 'input', key: 'h' }`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("infrastructure script missing %q", want)
		}
	}
}

func writeTempInfrastructureDevice(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write temp device: %v", err)
	}
	return path
}

func infrastructureTestUSBUIRequest(method, target string, body *strings.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "192.168.42.2:12345"
	req.Host = "192.168.42.1:8080"
	req.Header.Set("Origin", "http://192.168.42.1:8080")
	return req.WithContext(context.WithValue(
		req.Context(),
		http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.42.1"), Port: 8080},
	))
}

func boolToUint32(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}
