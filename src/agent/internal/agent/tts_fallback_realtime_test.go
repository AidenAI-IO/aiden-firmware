package agent

import (
	"context"
	"errors"
	"testing"
)

// newFallbackOnlyNotificationServer builds a Server whose only speech path is
// the prerecorded TTS-unavailable clip: no TTS provider is configured, which is
// the normal shape of an input_mode=realtime config.
func newFallbackOnlyNotificationServer(t *testing.T, ops *recordedAudioOps, locale string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(ttsUnavailableFallbackDirEnv, dir)
	filename := ttsUnavailableFallbackChinese
	if locale == "en-US" {
		filename = ttsUnavailableFallbackEnglish
	}
	writeTestTTSFallback(t, dir, filename)

	cfg := Config{Locale: locale}
	return &Server{
		runtime: &Runtime{
			config: cfg,
			voiceNotifications: NewVoiceNotificationManager(
				DefaultConfig().VoiceNotifications,
				WithVoiceNotificationLocale(locale),
			),
		},
		audioClient: NewAudioServiceClient(startRecordedTTSPlaybackAudioSocket(t, ops)),
	}
}

func TestCanSpeakVoiceNotificationCountsFallbackClipOnlyWhenAllowed(t *testing.T) {
	server := newFallbackOnlyNotificationServer(t, &recordedAudioOps{}, "zh-CN")

	if !server.CanSpeakVoiceNotification(true) {
		t.Fatal("CanSpeakVoiceNotification(true) = false, want true with the prerecorded clip available")
	}
	// Without the clip allowance the answer must stay false: no TTS provider is
	// configured, so nothing can carry the notification text.
	if server.CanSpeakVoiceNotification(false) {
		t.Fatal("CanSpeakVoiceNotification(false) = true, want false without a TTS provider")
	}
}

func TestSpeakVoiceNotificationPlaysFallbackClipWithoutTTSProvider(t *testing.T) {
	ops := &recordedAudioOps{}
	server := newFallbackOnlyNotificationServer(t, ops, "zh-CN")

	clipPlayed, err := server.SpeakVoiceNotification(context.Background(), "设备存储空间不足。", true)
	if !clipPlayed {
		t.Fatalf("clip played = false, want true; error = %v", err)
	}
	// The clip only announces that speech is unavailable, so the original TTS
	// error must survive and keep the notification pending.
	if !errors.Is(err, errTTSNotConfigured) {
		t.Fatalf("SpeakVoiceNotification() error = %v, want errTTSNotConfigured", err)
	}
	if got := ops.countOp("start_playback"); got != 1 {
		t.Fatalf("start_playback count = %d, want 1", got)
	}
}

func TestSpeakVoiceNotificationWithoutClipAllowanceStaysSilent(t *testing.T) {
	ops := &recordedAudioOps{}
	server := newFallbackOnlyNotificationServer(t, ops, "zh-CN")

	clipPlayed, err := server.SpeakVoiceNotification(context.Background(), "设备存储空间不足。", false)
	if clipPlayed {
		t.Fatal("clip played = true, want false when the clip is not allowed")
	}
	if !errors.Is(err, errStandaloneTTSUnavailable) {
		t.Fatalf("SpeakVoiceNotification() error = %v, want errStandaloneTTSUnavailable", err)
	}
	if got := ops.countOp("start_playback"); got != 0 {
		t.Fatalf("start_playback count = %d, want 0", got)
	}
}

func TestSpeakTurnFailurePlaysFallbackClipWithoutTTSProvider(t *testing.T) {
	ops := &recordedAudioOps{}
	server := newFallbackOnlyNotificationServer(t, ops, "zh-CN")

	clipPlayed, err := server.SpeakTurnFailure(context.Background(), &TurnFailure{Code: TurnFailureNetworkUnavailable})
	if !clipPlayed {
		t.Fatalf("clip played = false, want true; error = %v", err)
	}
	if !errors.Is(err, errTTSNotConfigured) {
		t.Fatalf("SpeakTurnFailure() error = %v, want errTTSNotConfigured", err)
	}
	if got := ops.countOp("start_playback"); got != 1 {
		t.Fatalf("start_playback count = %d, want 1", got)
	}
}

func TestSpeakTurnFailureIgnoresMissingFailure(t *testing.T) {
	ops := &recordedAudioOps{}
	server := newFallbackOnlyNotificationServer(t, ops, "zh-CN")

	if clipPlayed, err := server.SpeakTurnFailure(context.Background(), nil); clipPlayed || err != nil {
		t.Fatalf("SpeakTurnFailure(nil) = (%v, %v), want (false, nil)", clipPlayed, err)
	}
	if got := ops.countOp("start_playback"); got != 0 {
		t.Fatalf("start_playback count = %d, want 0", got)
	}
}
