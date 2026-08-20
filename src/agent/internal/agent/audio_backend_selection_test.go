package agent

import "testing"

func TestConfiguredAudioBackendsSelectLocalImplementations(t *testing.T) {
	cfg := Config{Audio: AudioConfig{Backend: AudioBackendLocal}}
	service := NewAudioServiceClient("/tmp/unused-audio-service.sock")

	recording := NewConfiguredAudioRecordingBackend(cfg, service, nil)
	if _, ok := recording.(*localAudioRecordingBackend); !ok {
		t.Fatalf("recording backend = %T, want *localAudioRecordingBackend", recording)
	}

	playback := NewConfiguredAudioPlaybackBackend(cfg, service, nil)
	if _, ok := playback.(*localAudioPlaybackBackend); !ok {
		t.Fatalf("playback backend = %T, want *localAudioPlaybackBackend", playback)
	}
}

func TestConfiguredAudioBackendsKeepAudioServiceOnBoard(t *testing.T) {
	cfg := Config{Audio: AudioConfig{Backend: AudioBackendAudioService}}
	service := NewAudioServiceClient("/tmp/audio-service.sock")

	if recording := NewConfiguredAudioRecordingBackend(cfg, service, nil); recording != service {
		t.Fatalf("recording backend = %T, want shared *AudioServiceClient", recording)
	}
	if playback := NewConfiguredAudioPlaybackBackend(cfg, service, nil); playback == nil {
		t.Fatal("playback backend is nil")
	} else if backend, ok := playback.(*audioBackend); !ok || backend.c != service {
		t.Fatalf("playback backend = %T, want audioBackend wrapping shared service", playback)
	}
}
