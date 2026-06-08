package tts

import (
	"context"
	"time"
)

const (
	// Matches kPlaybackDrainGraceMs in audio_playback_session.cpp.
	playbackDrainGrace = 300 * time.Millisecond
	// Extra margin for AO queue tail and scheduling jitter on embedded devices.
	playbackDrainMargin = 150 * time.Millisecond
)

// EstimatedPlaybackDrainDuration returns how long to wait for a playback
// session to finish after its final chunk was accepted by audio_service.
func EstimatedPlaybackDrainDuration(format AudioFormat, pcmBytes int) time.Duration {
	bytesPerSecond := format.BytesPerSecond()
	if bytesPerSecond <= 0 {
		bytesPerSecond = 16000 * 1 * 2
	}
	playback := time.Duration(pcmBytes) * time.Second / time.Duration(bytesPerSecond)
	return playback + playbackDrainGrace + playbackDrainMargin
}

func (f AudioFormat) BytesPerSecond() int {
	channels := f.Channels
	if channels <= 0 {
		channels = 1
	}
	bitWidth := f.BitWidth
	if bitWidth <= 0 {
		bitWidth = 16
	}
	sampleRate := f.SampleRate
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	return sampleRate * channels * (bitWidth / 8)
}

func waitForEstimatedDrain(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
