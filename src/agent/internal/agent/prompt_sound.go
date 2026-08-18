package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

type promptSoundKind int

// The first two values are load-bearing: existing call sites and tests depend on
// recording_start and agent_send keeping their iota positions, so new kinds are
// only ever appended.
const (
	promptSoundRecordingStart promptSoundKind = iota
	promptSoundAgentSend
	promptSoundQuickCaptureThreshold
	promptSoundQuickCaptureSuccess
	promptSoundQuickCaptureFailed
)

// String is the log label for a tone.
func (k promptSoundKind) String() string {
	switch k {
	case promptSoundRecordingStart:
		return "recording_start"
	case promptSoundAgentSend:
		return "agent_send"
	case promptSoundQuickCaptureThreshold:
		return "quick_capture_threshold"
	case promptSoundQuickCaptureSuccess:
		return "quick_capture_success"
	case promptSoundQuickCaptureFailed:
		return "quick_capture_failed"
	default:
		return "unknown"
	}
}

const (
	promptSoundSampleRate = 16000
	promptSoundChannels   = 1
	promptSoundBitWidth   = 16

	promptSoundDrainTimeout = 2 * time.Second
	promptSoundSettleDelay  = 450 * time.Millisecond
	promptSoundRetryDelay   = 150 * time.Millisecond
)

func playPromptSound(ctx context.Context, audio *AudioServiceClient, kind promptSoundKind, wait bool) error {
	if audio == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	pcm := promptSoundPCM(kind)
	format := AudioFormat{
		SampleRate: promptSoundSampleRate,
		Channels:   promptSoundChannels,
		BitWidth:   promptSoundBitWidth,
	}
	playback, err := audio.startPlaybackOnce(format)
	if err != nil && ctx.Err() == nil && retryablePromptStartPlaybackError(err) {
		timer := time.NewTimer(promptSoundRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			playback, err = audio.StartPlayback(format)
		}
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("start prompt playback: %w", err)
	}

	stopPlayback := true
	defer func() {
		if stopPlayback {
			_ = stopPlaybackIgnoringEnded(audio, playback.SessionID)
		}
	}()

	if err := writePlaybackPCMContext(ctx, audio, playback.SessionID, pcm); err != nil {
		return fmt.Errorf("write prompt playback: %w", err)
	}
	if err := audio.WritePlayChunk(playback.SessionID, nil, true); err != nil {
		return fmt.Errorf("finish prompt playback: %w", err)
	}

	if wait {
		if err := waitPromptPlayback(ctx, audio); err != nil {
			return err
		}
	}
	stopPlayback = false
	return nil
}
func waitPromptPlayback(ctx context.Context, audio *AudioServiceClient) error {
	waitCtx, cancel := context.WithTimeout(ctx, promptSoundDrainTimeout)
	defer cancel()
	if err := waitForPlaybackDrain(waitCtx, audio, promptSoundDrainTimeout); err != nil {
		return err
	}

	timer := time.NewTimer(promptSoundSettleDelay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryablePromptStartPlaybackError(err error) bool {
	var statusErr *audioStatusError
	return errors.As(err, &statusErr) && statusErr.op == "start_playback" && statusErr.status == "SERVICE_RECOVERING"
}

func promptSoundPCM(kind promptSoundKind) []byte {
	switch kind {
	case promptSoundAgentSend:
		return synthPromptPCM([]promptTone{
			{frequency: 740, duration: 55 * time.Millisecond},
			{frequency: 0, duration: 20 * time.Millisecond},
			{frequency: 980, duration: 70 * time.Millisecond},
		})
	case promptSoundQuickCaptureThreshold:
		// Plays immediately when the long-press threshold is reached, while the
		// button is still held. A single mid-frequency chirp: distinctive
		// without being jarring.
		return synthPromptPCM([]promptTone{
			{frequency: 880, duration: 60 * time.Millisecond},
		})
	case promptSoundQuickCaptureSuccess:
		// Plays after the pipeline completes. Rising two-tone to signal success.
		return synthPromptPCM([]promptTone{
			{frequency: 660, duration: 50 * time.Millisecond},
			{frequency: 0, duration: 15 * time.Millisecond},
			{frequency: 880, duration: 65 * time.Millisecond},
		})
	case promptSoundQuickCaptureFailed:
		// Plays when capture or vision fails. Descending to signal failure.
		return synthPromptPCM([]promptTone{
			{frequency: 740, duration: 50 * time.Millisecond},
			{frequency: 0, duration: 15 * time.Millisecond},
			{frequency: 440, duration: 65 * time.Millisecond},
		})
	default:
		// promptSoundRecordingStart
		return synthPromptPCM([]promptTone{
			{frequency: 980, duration: 70 * time.Millisecond},
			{frequency: 0, duration: 18 * time.Millisecond},
			{frequency: 1240, duration: 70 * time.Millisecond},
		})
	}
}

func promptSoundDurationMS(kind promptSoundKind) int64 {
	pcm := promptSoundPCM(kind)
	bytesPerSample := promptSoundBitWidth / 8
	frameBytes := promptSoundChannels * bytesPerSample
	if frameBytes <= 0 || promptSoundSampleRate <= 0 {
		return 0
	}
	frames := len(pcm) / frameBytes
	return int64(frames) * 1000 / promptSoundSampleRate
}

type promptTone struct {
	frequency float64
	duration  time.Duration
}

func synthPromptPCM(tones []promptTone) []byte {
	totalSamples := 0
	for _, tone := range tones {
		totalSamples += int(tone.duration * promptSoundSampleRate / time.Second)
	}
	pcm := make([]byte, totalSamples*2)

	offset := 0
	for _, tone := range tones {
		samples := int(tone.duration * promptSoundSampleRate / time.Second)
		for i := 0; i < samples; i++ {
			var sample int16
			if tone.frequency > 0 {
				phase := 2 * math.Pi * tone.frequency * float64(i) / promptSoundSampleRate
				envelope := promptEnvelope(i, samples)
				sample = int16(math.Sin(phase) * envelope * 9000)
			}
			binary.LittleEndian.PutUint16(pcm[offset:], uint16(sample))
			offset += 2
		}
	}
	return pcm
}

func promptEnvelope(i, samples int) float64 {
	const fadeSamples = promptSoundSampleRate / 200 // 5ms
	if samples <= fadeSamples*2 {
		return 1
	}
	if i < fadeSamples {
		return float64(i) / fadeSamples
	}
	if remaining := samples - i - 1; remaining < fadeSamples {
		return float64(remaining) / fadeSamples
	}
	return 1
}
