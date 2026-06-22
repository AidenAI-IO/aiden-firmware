package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewSTTClientFromConfigOpenRouter(t *testing.T) {
	client, err := NewSTTClientFromConfig(Config{
		STT: STTConfig{
			Provider: " openrouter ",
			APIKey:   "sk-or",
		},
	})
	if err != nil {
		t.Fatalf("NewSTTClientFromConfig() error = %v", err)
	}

	stt, ok := client.(*OpenRouterSTT)
	if !ok {
		t.Fatalf("client type = %T, want *OpenRouterSTT", client)
	}
	if stt.model != openRouterSTTModel {
		t.Fatalf("model = %q, want %q", stt.model, openRouterSTTModel)
	}
}

func TestOpenRouterSTTTranscribeWAVSendsJSONAudioPayload(t *testing.T) {
	wavData := []byte("RIFF wav data")
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
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or" {
			fail("Authorization = %q, want bearer token", got)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			fail("Content-Type = %q, want application/json", got)
			return
		}

		var body struct {
			Model      string `json:"model"`
			InputAudio struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail("decode request body: %v", err)
			return
		}
		if body.Model != "custom/asr" {
			fail("model = %q, want custom/asr", body.Model)
			return
		}
		if body.InputAudio.Data != base64.StdEncoding.EncodeToString(wavData) {
			fail("input_audio.data = %q, want base64 wav data", body.InputAudio.Data)
			return
		}
		if body.InputAudio.Format != "wav" {
			fail("input_audio.format = %q, want wav", body.InputAudio.Format)
			return
		}

		errCh <- nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello world"}`))
	}))
	defer server.Close()

	client := NewOpenRouterSTT("sk-or", "custom/asr", server.URL+"/", server.Client())
	text, err := client.TranscribeWAV(wavData)
	select {
	case handlerErr := <-errCh:
		if handlerErr != nil {
			t.Fatal(handlerErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OpenRouter STT test handler")
	}
	if err != nil {
		t.Fatalf("TranscribeWAV() error = %v", err)
	}
	if text != "hello world" {
		t.Fatalf("text = %q, want hello world", text)
	}
}

func TestOpenRouterSTTTranscribeWAVReturnsAPIErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad token"}`)
	}))
	defer server.Close()

	client := NewOpenRouterSTT("bad-token", "custom/asr", server.URL, server.Client())
	_, err := client.TranscribeWAV([]byte("wav"))
	if err == nil {
		t.Fatal("expected API error")
	}
	if !strings.Contains(err.Error(), "API error 401") || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAIWhisperSTTWithLanguage(t *testing.T) {
	wavData := []byte("RIFF wav data")
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

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			fail("parse multipart form: %v", err)
			return
		}

		if model := r.FormValue("model"); model != "whisper-1" {
			fail("model = %q, want whisper-1", model)
			return
		}
		if language := r.FormValue("language"); language != "zh" {
			fail("language = %q, want zh", language)
			return
		}

		errCh <- nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"你好世界"}`))
	}))
	defer server.Close()

	client := NewOpenAIWhisperSTT("sk-test", "whisper-1", server.URL, "zh", server.Client())
	text, err := client.TranscribeWAV(wavData)
	select {
	case handlerErr := <-errCh:
		if handlerErr != nil {
			t.Fatal(handlerErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OpenAI Whisper STT test handler")
	}
	if err != nil {
		t.Fatalf("TranscribeWAV() error = %v", err)
	}
	if text != "你好世界" {
		t.Fatalf("text = %q, want 你好世界", text)
	}
}

func TestTencentASRLanguageMapping(t *testing.T) {
	tests := []struct {
		name            string
		language        string
		engineModelType string
		wantEngine      string
	}{
		{"zh maps to 16k_zh", "zh", "", "16k_zh"},
		{"en maps to 16k_en", "en", "", "16k_en"},
		{"language overrides explicit engine", "zh", "8k_en", "16k_zh"},
		{"unknown language uses default", "ja", "", "16k_zh"},
		{"empty language uses explicit engine", "", "8k_en", "8k_en"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewTencentASRSTT("id", "key", "ap-guangzhou", tt.engineModelType, tt.language)
			if client.engineModelType != tt.wantEngine {
				t.Fatalf("engineModelType = %q, want %q", client.engineModelType, tt.wantEngine)
			}
		})
	}
}

