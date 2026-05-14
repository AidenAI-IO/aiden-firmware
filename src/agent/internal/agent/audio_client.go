package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// AudioFormat specifies audio stream parameters
type AudioFormat struct {
	SampleRate uint32 `json:"sample_rate"`
	Channels   uint32 `json:"channels"`
	BitWidth   uint32 `json:"bit_width"`
}

// RecordStartResult contains the session ID for a recording session
type RecordStartResult struct {
	SessionID uint64 `json:"session_id"`
}

// AudioChunkResult contains PCM data from a recording session
type AudioChunkResult struct {
	PCM         []byte `json:"pcm"`
	EndOfStream bool   `json:"end_of_stream"`
}

// PlaybackStartResult contains the session ID for a playback session
type PlaybackStartResult struct {
	SessionID uint64 `json:"session_id"`
}

// AudioHealthResult contains audio service health status
type AudioHealthResult struct {
	RecordingActive   bool   `json:"recording_active"`
	PlaybackActive    bool   `json:"playback_active"`
	RecordSessions    uint32 `json:"record_sessions"`
	PlaybackSessions  uint32 `json:"playback_sessions"`
}

// AudioServiceClient is a client for the audio_service Unix socket
type AudioServiceClient struct {
	socketPath string
}

// NewAudioServiceClient creates a new audio service client
func NewAudioServiceClient(socketPath string) *AudioServiceClient {
	return &AudioServiceClient{socketPath: socketPath}
}

// audioRequest is the wire format for requests
type audioRequest struct {
	Op        string       `json:"op"`
	Format    *AudioFormat `json:"format,omitempty"`
	SessionID uint64       `json:"session_id,omitempty"`
	TimeoutMs uint32       `json:"timeout_ms,omitempty"`
	Data      []byte       `json:"data,omitempty"`
	IsFinal   bool         `json:"is_final,omitempty"`
}

// audioResponse is the wire format for responses
type audioResponse struct {
	Status          string `json:"status"`
	SessionID       uint64 `json:"session_id,omitempty"`
	PCM             []byte `json:"pcm,omitempty"`
	EndOfStream     bool   `json:"end_of_stream,omitempty"`
	RecordingActive bool   `json:"recording_active,omitempty"`
	PlaybackActive  bool   `json:"playback_active,omitempty"`
	RecordSessions  uint32 `json:"record_sessions,omitempty"`
	PlaybackSessions uint32 `json:"playback_sessions,omitempty"`
}

// sendRequest sends a request and receives a response
func (c *AudioServiceClient) sendRequest(req audioRequest, timeout time.Duration) (*audioResponse, error) {
	conn, err := net.DialTimeout("unix", c.socketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if timeout > 0 {
		conn.SetDeadline(time.Now().Add(timeout))
	}

	// Send request
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := conn.Write(reqData); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response
	buf := make([]byte, 1024*1024) // 1MB buffer for audio chunks
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp audioResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// StartRecording starts a new recording session
func (c *AudioServiceClient) StartRecording(format AudioFormat) (*RecordStartResult, error) {
	req := audioRequest{
		Op:     "start_recording",
		Format: &format,
	}

	resp, err := c.sendRequest(req, 5*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.Status != "ok" {
		return nil, fmt.Errorf("start_recording failed: %s", resp.Status)
	}

	return &RecordStartResult{SessionID: resp.SessionID}, nil
}

// ReadRecordChunk reads a PCM chunk from a recording session (long-poll)
func (c *AudioServiceClient) ReadRecordChunk(sessionID uint64, timeoutMs uint32) (*AudioChunkResult, error) {
	req := audioRequest{
		Op:        "read_record_chunk",
		SessionID: sessionID,
		TimeoutMs: timeoutMs,
	}

	timeout := time.Duration(timeoutMs+5000) * time.Millisecond
	resp, err := c.sendRequest(req, timeout)
	if err != nil {
		return nil, err
	}

	if resp.Status == "timeout" {
		return &AudioChunkResult{}, nil
	}

	if resp.Status != "ok" {
		return nil, fmt.Errorf("read_record_chunk failed: %s", resp.Status)
	}

	return &AudioChunkResult{
		PCM:         resp.PCM,
		EndOfStream: resp.EndOfStream,
	}, nil
}

// StopRecording stops a recording session
func (c *AudioServiceClient) StopRecording(sessionID uint64) error {
	req := audioRequest{
		Op:        "stop_recording",
		SessionID: sessionID,
	}

	resp, err := c.sendRequest(req, 5*time.Second)
	if err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("stop_recording failed: %s", resp.Status)
	}

	return nil
}

// StartPlayback starts a new playback session
func (c *AudioServiceClient) StartPlayback(format AudioFormat) (*PlaybackStartResult, error) {
	req := audioRequest{
		Op:     "start_playback",
		Format: &format,
	}

	resp, err := c.sendRequest(req, 5*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.Status != "ok" {
		return nil, fmt.Errorf("start_playback failed: %s", resp.Status)
	}

	return &PlaybackStartResult{SessionID: resp.SessionID}, nil
}

// WritePlayChunk writes a PCM chunk to a playback session
func (c *AudioServiceClient) WritePlayChunk(sessionID uint64, data []byte, isFinal bool) error {
	req := audioRequest{
		Op:        "write_play_chunk",
		SessionID: sessionID,
		Data:      data,
		IsFinal:   isFinal,
	}

	resp, err := c.sendRequest(req, 5*time.Second)
	if err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("write_play_chunk failed: %s", resp.Status)
	}

	return nil
}

// StopPlayback stops a playback session
func (c *AudioServiceClient) StopPlayback(sessionID uint64) error {
	req := audioRequest{
		Op:        "stop_playback",
		SessionID: sessionID,
	}

	resp, err := c.sendRequest(req, 5*time.Second)
	if err != nil {
		return err
	}

	if resp.Status != "ok" {
		return fmt.Errorf("stop_playback failed: %s", resp.Status)
	}

	return nil
}

// Health checks the audio service health
func (c *AudioServiceClient) Health() (*AudioHealthResult, error) {
	req := audioRequest{
		Op: "health",
	}

	resp, err := c.sendRequest(req, 5*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.Status != "ok" {
		return nil, fmt.Errorf("health check failed: %s", resp.Status)
	}

	return &AudioHealthResult{
		RecordingActive:  resp.RecordingActive,
		PlaybackActive:   resp.PlaybackActive,
		RecordSessions:   resp.RecordSessions,
		PlaybackSessions: resp.PlaybackSessions,
	}, nil
}
