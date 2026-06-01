package tts

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	drainPollInterval = 50 * time.Millisecond
	drainTimeout      = 30 * time.Second
	chunkBytes        = 4096 // 2048 samples @ 16kHz s16le mono ≈ 128ms
)

// AudioServiceSink implements AudioSink by writing PCM to AudioServiceClient.
type AudioServiceSink struct {
	audio     AudioServiceBackend
	format    AudioFormat
	sessionID uint64
	started   bool
	stopped   bool
	pcmBytes  int
}

// AudioServiceBackend is a minimal interface over *AudioServiceClient used by the sink.
// This avoids a hard import cycle while still letting adapters use the real client.
type AudioServiceBackend interface {
	StartPlayback(format AudioFormat) (sessionID uint64, err error)
	WritePlayChunk(sessionID uint64, data []byte, isFinal bool) error
	StopPlayback(sessionID uint64) error
	PlaybackSessionCount() (int, error)
}

// NewAudioServiceSink creates a sink that opens a playback session on first write.
func NewAudioServiceSink(audio AudioServiceBackend, format AudioFormat) *AudioServiceSink {
	return &AudioServiceSink{audio: audio, format: format}
}

func (s *AudioServiceSink) Format() AudioFormat { return s.format }

func (s *AudioServiceSink) WritePCM(data []byte) error {
	if s.stopped {
		return ErrSessionClosed
	}
	if !s.started {
		id, err := s.audio.StartPlayback(s.format)
		if err != nil {
			return fmt.Errorf("start playback: %w", err)
		}
		s.sessionID = id
		s.started = true
	}
	for off := 0; off < len(data); off += chunkBytes {
		end := off + chunkBytes
		if end > len(data) {
			end = len(data)
		}
		if err := s.audio.WritePlayChunk(s.sessionID, data[off:end], false); err != nil {
			return fmt.Errorf("write play chunk: %w", err)
		}
		s.pcmBytes += end - off
	}
	return nil
}

// PCMBytes returns how many PCM bytes were accepted by the audio backend.
func (s *AudioServiceSink) PCMBytes() int { return s.pcmBytes }

func (s *AudioServiceSink) Drain(ctx context.Context) error {
	if !s.started || s.stopped {
		return nil
	}
	// Send final chunk to signal end of stream.
	if err := s.audio.WritePlayChunk(s.sessionID, nil, true); err != nil {
		return fmt.Errorf("send final chunk: %w", err)
	}
	s.stopped = true

	// Wait for playback to drain.
	deadline := time.Now().Add(drainTimeout)
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()
	for {
		count, err := s.audio.PlaybackSessionCount()
		if err == nil && count == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("drain timeout after %s", drainTimeout)
			}
		}
	}
}

func (s *AudioServiceSink) Stop() error {
	if !s.started || s.stopped {
		return nil
	}
	s.stopped = true
	err := s.audio.StopPlayback(s.sessionID)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "session_not_found") || strings.Contains(msg, "not found") {
		return nil
	}
	return err
}
