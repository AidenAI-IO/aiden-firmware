package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunSTTTranscriptionTestUsesRequestOverrides(t *testing.T) {
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(format string, args ...any) {
			errCh <- fmt.Errorf(format, args...)
			http.Error(w, "bad request", http.StatusBadRequest)
		}

		if r.Method != http.MethodPost {
			fail("method = %s, want POST", r.Method)
			return
		}
		if r.URL.Path != "/audio/transcriptions" {
			fail("path = %s, want /audio/transcriptions", r.URL.Path)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-stt-test" {
			fail("authorization = %q, want Bearer sk-stt-test", got)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			fail("parse multipart form: %v", err)
			return
		}
		if model := r.FormValue("model"); model != "whisper-1" {
			fail("model = %q, want whisper-1", model)
			return
		}
		if language := r.FormValue("language"); language != "en" {
			fail("language = %q, want en", language)
			return
		}

		errCh <- nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"test transcript"}`))
	}))
	defer server.Close()

	result, err := RunSTTTranscriptionTest(context.Background(), Config{}, STTTranscriptionTestRequest{
		Provider: "openai-whisper",
		APIKey:   "sk-stt-test",
		Model:    "whisper-1",
		BaseURL:  server.URL,
		Language: "en",
		WAVData:  []byte("RIFF wav data"),
	})
	select {
	case handlerErr := <-errCh:
		if handlerErr != nil {
			t.Fatal(handlerErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for STT test handler")
	}
	if err != nil {
		t.Fatalf("RunSTTTranscriptionTest() error = %v", err)
	}
	if result.Provider != "openai-whisper" {
		t.Fatalf("provider = %q, want openai-whisper", result.Provider)
	}
	if result.Transcript != "test transcript" {
		t.Fatalf("transcript = %q, want test transcript", result.Transcript)
	}
}

func TestRunSTTTranscriptionTestRequiresProvider(t *testing.T) {
	_, err := RunSTTTranscriptionTest(context.Background(), Config{}, STTTranscriptionTestRequest{
		WAVData: []byte("RIFF wav data"),
	})
	if err == nil || err.Error() != "stt.provider is required" {
		t.Fatalf("error = %v, want stt.provider is required", err)
	}
}

func TestRunSTTTranscriptionTestRequiresAudio(t *testing.T) {
	_, err := RunSTTTranscriptionTest(context.Background(), Config{
		STT: STTConfig{
			Provider: "openai-whisper",
		},
	}, STTTranscriptionTestRequest{})
	if err == nil || err.Error() != "audio data is required" {
		t.Fatalf("error = %v, want audio data is required", err)
	}
}

func TestRunSTTProviderTestUsesRuntimeDefaultEndpoint(t *testing.T) {
	cfg := Config{
		STTProviders: map[string]STTProvider{
			"qwen-main": {
				Type:   "qwen-asr",
				APIKey: "test-key",
			},
		},
	}
	result, err := RunSTTProviderTest(context.Background(), cfg, STTTranscriptionTestRequest{
		Provider: "qwen-main",
		Language: "zh",
	})
	if err != nil {
		t.Fatalf("RunSTTProviderTest() error = %v", err)
	}
	if result.Provider != "qwen-asr" {
		t.Fatalf("provider = %q, want qwen-asr", result.Provider)
	}
}

func TestRunSTTProviderTestAcceptsTencentWithoutAppID(t *testing.T) {
	result, err := RunSTTProviderTest(context.Background(), Config{}, STTTranscriptionTestRequest{
		Provider:  "tencent-asr",
		SecretID:  "test-id",
		SecretKey: "test-key",
		Region:    "ap-shanghai",
		Language:  "zh",
	})
	if err != nil {
		t.Fatalf("RunSTTProviderTest() error = %v", err)
	}
	if result.Provider != "tencent-asr" {
		t.Fatalf("provider = %q, want tencent-asr", result.Provider)
	}
}
