package tts

import (
	"context"
	"time"
)

const (
	playbackDrainMinGraceMs   = 300
	playbackDrainPaddingMs    = 300
	playbackDrainMaxGraceMs   = 3000
	playbackDrainAOFrameCount = 4
	playbackDrainAOFrameSize  = 1024
)

// EstimatedPlaybackDrainDuration returns how long to wait for a playback
// session to finish after its final chunk was accepted by audio_service.
func EstimatedPlaybackDrainDuration(format AudioFormat, pcmBytes int) time.Duration {
	bytesPerSecond := format.BytesPerSecond()
	if bytesPerSecond <= 0 {
		bytesPerSecond = 16000 * 1 * 2
	}
	playback := time.Duration(pcmBytes) * time.Second / time.Duration(bytesPerSecond)
	return playback + playbackTailDrainGrace(format, chunkBytes)
}

func playbackTailDrainGrace(format AudioFormat, lastChunkBytes int) time.Duration {
	bytesPerSample := format.BitWidth / 8
	if format.SampleRate <= 0 || format.Channels <= 0 || bytesPerSample <= 0 {
		return time.Duration(playbackDrainMinGraceMs) * time.Millisecond
	}
	bytesPerSecond := int64(format.SampleRate) * int64(format.Channels) * int64(bytesPerSample)
	if bytesPerSecond <= 0 {
		return time.Duration(playbackDrainMinGraceMs) * time.Millisecond
	}

	chunkMs := ceilDiv(int64(lastChunkBytes)*1000, bytesPerSecond)
	frameMs := ceilDiv(int64(playbackDrainAOFrameSize)*1000, int64(format.SampleRate))
	queuedFrameMs := chunkMs
	if frameMs > queuedFrameMs {
		queuedFrameMs = frameMs
	}
	graceMs := queuedFrameMs*playbackDrainAOFrameCount + playbackDrainPaddingMs
	if graceMs < playbackDrainMinGraceMs {
		graceMs = playbackDrainMinGraceMs
	}
	if graceMs > playbackDrainMaxGraceMs {
		graceMs = playbackDrainMaxGraceMs
	}
	return time.Duration(graceMs) * time.Millisecond
}

func ceilDiv(n, d int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
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
