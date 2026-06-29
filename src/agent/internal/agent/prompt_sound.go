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

const (
	promptSoundRecordingStart promptSoundKind = iota
	promptSoundAgentSend

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
	default:
		return synthPromptPCM([]promptTone{
			{frequency: 980, duration: 70 * time.Millisecond},
			{frequency: 0, duration: 18 * time.Millisecond},
			{frequency: 1240, duration: 70 * time.Millisecond},
		})
	}
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
