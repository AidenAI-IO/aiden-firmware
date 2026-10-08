package agent

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type warmupRecordingBackend struct{}

func (warmupRecordingBackend) StartRecording(AudioFormat) (*RecordStartResult, error) {
	return &RecordStartResult{SessionID: 42}, nil
}

func (warmupRecordingBackend) ReadRecordChunk(uint64, uint32) (*AudioChunkResult, error) {
	return &AudioChunkResult{}, nil
}

func (warmupRecordingBackend) StopRecording(uint64) error { return nil }

func TestAudioDialogWarmupFollowsModelReload(t *testing.T) {
	clearAgentProxyEnv(t)
	for _, tc := range []struct {
		name                         string
		initialModel, nextModel, stt bool
	}{
		{name: "replace endpoint and retain STT", initialModel: true, nextModel: true, stt: true},
		{name: "enable initially absent endpoint", nextModel: true},
		{name: "clear endpoint and retain STT", initialModel: true, stt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [3]atomic.Int32
			var urls [3]string
			for i := range calls {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls[i].Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(server.Close)
				urls[i] = server.URL + "/v1"
			}
			cfg := Config{
				ConfigDir: t.TempDir(), InputMode: "stt",
				Model: ModelConfig{Provider: "fake"},
				STT:   STTConfig{Provider: "openai-whisper"},
				Audio: AudioConfig{Backend: AudioBackendLocal, SampleRate: 16000},
			}
			if tc.initialModel {
				cfg.Model.BaseURL = urls[0]
			}
			if tc.stt {
				cfg.STT.BaseURL = urls[2]
			}
			runtime := &Runtime{config: cfg, models: NewModelManager(cfg.Model, ProxyConfig{})}
			t.Cleanup(func() { _ = runtime.Close() })
			dialog, err := NewAudioDialog(runtime)
			if err != nil {
				t.Fatal(err)
			}
			// Exercise recording and real HTTP warmups without VAD hardware or STT uploads.
			_ = dialog.vad.Close()
			dialog.vad, dialog.sttClient = nil, nil
			dialog.recordBackend = warmupRecordingBackend{}
			dialog.ttsPlaybackBackend = noopTTSPlaybackBackend{}
			t.Cleanup(func() { _ = dialog.Close() })
			record := func() {
				t.Helper()
				if err := dialog.StartRecording(); err != nil {
					t.Fatal(err)
				}
				waitForConnectionWarmup(t, dialog.connWarmer)
				if err := dialog.StopRecording(); err != nil {
					t.Fatal(err)
				}
			}
			record()
			for i := range calls {
				calls[i].Store(0)
			}
			next := cfg
			next.Model.BaseURL = ""
			if tc.nextModel {
				next.Model.BaseURL = urls[1]
			}
			if err := runtime.ApplyConfigSnapshot(next); err != nil {
				t.Fatal(err)
			}
			record()
			want := [3]int32{}
			if tc.nextModel {
				want[1] = 1
			}
			if tc.stt {
				want[2] = 1
			}
			for i := range calls {
				if got := calls[i].Load(); got != want[i] {
					t.Errorf("endpoint %d received %d warmups after reload, want %d", i, got, want[i])
				}
			}
		})
	}
}
