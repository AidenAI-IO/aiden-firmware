package agent

import "testing"

func TestNewMinimaxTTSDefaults(t *testing.T) {
	tts := NewMinimaxTTS("key", "", "", 0)

	if tts.voiceID != "male-qn-qingse" {
		t.Fatalf("voiceID = %q, want default", tts.voiceID)
	}
	if tts.emotion != "happy" {
		t.Fatalf("emotion = %q, want happy", tts.emotion)
	}
	if tts.speed != 1.0 {
		t.Fatalf("speed = %v, want 1.0", tts.speed)
	}
}

func TestMinimaxPlaybackFormatConstants(t *testing.T) {
	if minimaxTTSSampleRate != 32000 {
		t.Fatalf("sample rate = %d, want 32000", minimaxTTSSampleRate)
	}
	if minimaxTTSChannels != 1 {
		t.Fatalf("channels = %d, want 1", minimaxTTSChannels)
	}
	if minimaxTTSBitWidth != 16 {
		t.Fatalf("bit width = %d, want 16", minimaxTTSBitWidth)
	}
}
