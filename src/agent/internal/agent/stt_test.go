package agent

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("path = %s, want /audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}

		var body struct {
			Model      string `json:"model"`
			InputAudio struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Model != "custom/asr" {
			t.Fatalf("model = %q, want custom/asr", body.Model)
		}
		if body.InputAudio.Data != base64.StdEncoding.EncodeToString(wavData) {
			t.Fatalf("input_audio.data = %q, want base64 wav data", body.InputAudio.Data)
		}
		if body.InputAudio.Format != "wav" {
			t.Fatalf("input_audio.format = %q, want wav", body.InputAudio.Format)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello world"}`))
	}))
	defer server.Close()

	client := NewOpenRouterSTT("sk-or", "custom/asr", server.URL+"/", server.Client())
	text, err := client.TranscribeWAV(wavData)
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
