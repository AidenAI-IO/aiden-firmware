package tts

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	drainTimeout                = 30 * time.Second
	chunkBytes                  = 4096 // 2048 samples @ 16kHz s16le mono ~= 128ms
	prebufferDuration           = 500 * time.Millisecond
	playbackTailSilenceDuration = 1 * time.Second
)

// AudioServiceSink implements AudioSink by writing PCM to AudioServiceClient.
type AudioServiceSink struct {
	audio     AudioServiceBackend
	format    AudioFormat
	sessionID uint64
	started   bool
	stopped   bool
	pcmBytes  int
	pending   []byte
}

// AudioServiceBackend is a minimal interface over *AudioServiceClient used by the sink.
// This avoids a hard import cycle while still letting adapters use the real client.
type AudioServiceBackend interface {
	StartPlayback(format AudioFormat) (sessionID uint64, err error)
	WritePlayChunk(sessionID uint64, data []byte, isFinal bool) error
	StopPlayback(sessionID uint64) error
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
	if len(data) == 0 {
		return nil
	}
	if !s.started {
		s.pending = append(s.pending, data...)
		if len(s.pending) < s.prebufferBytes() {
			return nil
		}
		return s.flushPending()
	}
	return s.writePCMChunks(data)
}

func (s *AudioServiceSink) writePCMChunks(data []byte) error {
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

func (s *AudioServiceSink) flushPending() error {
	if len(s.pending) == 0 {
		return nil
	}
	pending := s.pending
	s.pending = nil
	return s.writePCMChunks(pending)
}

func (s *AudioServiceSink) prebufferBytes() int {
	bytes := s.pcmBytesForDuration(prebufferDuration)
	if bytes <= 0 {
		return chunkBytes
	}
	if bytes < chunkBytes {
		return chunkBytes
	}
	return bytes
}

func (s *AudioServiceSink) pcmBytesForDuration(d time.Duration) int {
	bytesPerSample := s.format.BitWidth / 8
	if s.format.SampleRate <= 0 || s.format.Channels <= 0 || bytesPerSample <= 0 {
		return 0
	}
	return int((int64(s.format.SampleRate) * int64(s.format.Channels) * int64(bytesPerSample) * int64(d)) / int64(time.Second))
}

// PCMBytes returns how many PCM bytes were accepted by the audio backend.
func (s *AudioServiceSink) PCMBytes() int { return s.pcmBytes }

func (s *AudioServiceSink) Drain(ctx context.Context) error {
	if s.stopped {
		return nil
	}
	if err := s.flushPending(); err != nil {
		return err
	}
	if !s.started {
		return nil
	}
	if tailBytes := s.pcmBytesForDuration(playbackTailSilenceDuration); tailBytes > 0 {
		if err := s.writePCMChunks(make([]byte, tailBytes)); err != nil {
			return fmt.Errorf("write tail silence: %w", err)
		}
	}
	// Send final chunk to signal end of stream.
	if err := s.audio.WritePlayChunk(s.sessionID, nil, true); err != nil {
		return fmt.Errorf("send final chunk: %w", err)
	}
	s.stopped = true

	wait := EstimatedPlaybackDrainDuration(s.format, s.pcmBytes)
	waitCtx, cancel := context.WithTimeout(ctx, drainTimeout)
	defer cancel()
	if err := waitForEstimatedDrain(waitCtx, wait); err != nil {
		if waitCtx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("drain timeout after %s", drainTimeout)
		}
		return err
	}
	return nil
}

func (s *AudioServiceSink) Stop() error {
	if s.stopped {
		return nil
	}
	s.stopped = true
	s.pending = nil
	if !s.started {
		return nil
	}
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
