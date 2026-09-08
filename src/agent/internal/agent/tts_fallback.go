package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aiden-agent/internal/agent/tts"
)

const (
	defaultTTSUnavailableFallbackDir = "/oem/usr/share/aiden/audio/voice-notifications"
	ttsUnavailableFallbackDirEnv     = "AIDEN_TTS_FALLBACK_DIR"
	ttsUnavailableFallbackEnglish    = "tts-unavailable.en-US.wav"
	ttsUnavailableFallbackChinese    = "tts-unavailable.zh-CN.wav"
)

var (
	errTTSNotConfigured = errors.New("tts is not configured")
	errTTSNoAudio       = errors.New("tts completed without writing audio")

	errStandaloneTTSUnavailable = errors.New("standalone TTS is unavailable")
)

func ttsUnavailableFallbackPath(cfg Config) string {
	if !cfg.VoiceNotifications.EnabledOrDefault() {
		return ""
	}
	dir := strings.TrimSpace(os.Getenv(ttsUnavailableFallbackDirEnv))
	if dir == "" {
		dir = defaultTTSUnavailableFallbackDir
	}
	filename := ttsUnavailableFallbackChinese
	if strings.HasPrefix(resolvedVoiceNotificationLocale(cfg), "en") {
		filename = ttsUnavailableFallbackEnglish
	}
	return filepath.Join(dir, filename)
}

func canPlayTTSUnavailableFallback(cfg Config) bool {
	path := ttsUnavailableFallbackPath(cfg)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 44
}

func playTTSUnavailableFallback(ctx context.Context, audio tts.AudioServiceBackend, cfg Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if audio == nil {
		return errors.New("audio backend is not configured")
	}
	path := ttsUnavailableFallbackPath(cfg)
	if path == "" {
		return errors.New("local TTS fallback is disabled")
	}
	wavData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read local TTS fallback %s: %w", path, err)
	}
	pcm, sampleRate, err := extractPCMFromWAV(wavData)
	if err != nil {
		return fmt.Errorf("decode local TTS fallback %s: %w", path, err)
	}
	if len(pcm) == 0 {
		return fmt.Errorf("local TTS fallback %s contains no PCM audio", path)
	}

	sink := tts.NewAudioServiceSink(audio, tts.AudioFormat{
		SampleRate: sampleRate,
		Channels:   1,
		BitWidth:   16,
	})
	if err := sink.WritePCM(pcm); err != nil {
		_ = sink.Stop()
		return fmt.Errorf("start local TTS fallback playback: %w", err)
	}
	if err := sink.Drain(ctx); err != nil {
		_ = sink.Stop()
		return fmt.Errorf("finish local TTS fallback playback: %w", err)
	}
	return nil
}

// attemptTTSUnavailableFallback preserves the original TTS error even when
// the local recording plays successfully. Callers use the non-nil error to
// avoid acknowledging response-tail delivery for speech that was not spoken.
func attemptTTSUnavailableFallback(ctx context.Context, audio tts.AudioServiceBackend, cfg Config, speechStarted bool, ttsErr error) (bool, error) {
	if ttsErr == nil || speechStarted || !canPlayTTSUnavailableFallback(cfg) {
		return false, ttsErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, ttsErr
	}
	if errors.Is(ttsErr, context.Canceled) {
		return false, ttsErr
	}
	if err := playTTSUnavailableFallback(ctx, audio, cfg); err != nil {
		return false, errors.Join(ttsErr, err)
	}
	return true, ttsErr
}
