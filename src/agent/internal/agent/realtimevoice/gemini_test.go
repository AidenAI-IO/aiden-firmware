package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"net/http/httptest"
)

func TestGeminiProviderNormalizesLiveSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "gemini-key" {
			t.Errorf("api key query = %q", got)
		}
		if got := r.URL.Query().Get("model"); got != "" {
			t.Errorf("unexpected model query = %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		var setup map[string]any
		if err := json.Unmarshal(body, &setup); err != nil {
			t.Error(err)
			return
		}
		payload, ok := setup["setup"].(map[string]any)
		if !ok || payload["model"] != "models/gemini-3.1-flash-live-preview" {
			t.Errorf("setup = %#v", setup)
		}
		if _, ok := payload["tools"]; !ok {
			t.Errorf("setup missing tools = %#v", setup)
		}
		if err := conn.WriteJSON(map[string]any{"setupComplete": map[string]any{}}); err != nil {
			t.Error(err)
			return
		}
		for {
			typ, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ != websocket.TextMessage {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal(body, &event); err != nil {
				t.Error(err)
				return
			}
			switch {
			case event["realtimeInput"] != nil:
				input := event["realtimeInput"].(map[string]any)
				audio := input["audio"].(map[string]any)
				if audio["mimeType"] != "audio/pcm;rate=16000" {
					t.Errorf("audio = %#v", audio)
				}
				_ = conn.WriteJSON(map[string]any{"serverContent": map[string]any{
					"modelTurn":           map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString([]byte{4, 5})}}}},
					"outputTranscription": map[string]any{"text": "hello", "finished": false},
				}})
				_ = conn.WriteJSON(map[string]any{"toolCall": map[string]any{"functionCalls": []any{map[string]any{"id": "call_1", "name": "clock", "args": map[string]any{"timezone": "UTC"}}}}})
			case event["toolResponse"] != nil:
				response := event["toolResponse"].(map[string]any)
				if len(response["functionResponses"].([]any)) != 1 {
					t.Errorf("tool response = %#v", event)
				}
				_ = conn.WriteJSON(map[string]any{"serverContent": map[string]any{"turnComplete": true}, "usageMetadata": map[string]any{"promptTokenCount": 3, "responseTokenCount": 4, "totalTokenCount": 7}})
			case event["clientContent"] != nil:
				_ = conn.WriteJSON(map[string]any{"serverContent": map[string]any{"turnComplete": true}})
			}
		}
	}))
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/live"
	session, err := (GeminiProvider{Endpoint: endpoint}).Open(context.Background(), SessionConfig{
		APIKey: "gemini-key", Model: "gemini-3.1-flash-live-preview", Voice: "Puck", Instructions: "be concise",
		Tools: []Tool{{Name: "clock", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Info(); got.ID != "" || got.InputSampleRate != 16000 || got.OutputSampleRate != 24000 || got.InputAudioFormat != (AudioFormat{Encoding: "pcm_s16le", SampleRate: 16000, Channels: 1, BitDepth: 16}) || got.OutputAudioFormat != (AudioFormat{Encoding: "pcm_s16le", SampleRate: 24000, Channels: 1, BitDepth: 16}) {
		t.Fatalf("session info = %+v", got)
	}
	if err := session.SendAudio(context.Background(), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := session.(ToolResultSender).SendToolResult(context.Background(), "call_1", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}

	want := []EventKind{EventResponseStarted, EventTranscriptDelta, EventAudio, EventToolCall, EventUsage, EventResponseDone}
	for _, kind := range want {
		select {
		case event := <-session.Events():
			if event.Kind != kind {
				t.Fatalf("event kind = %s, want %s (%+v)", event.Kind, kind, event)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func TestGeminiEndpointPreservesDelegatedAccessToken(t *testing.T) {
	provider := GeminiProvider{Endpoint: "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent", DelegatedCredential: true}
	got, err := provider.endpoint("gemini-3.1-flash-live-preview", "delegated")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "key=") || !strings.Contains(got, "access_token=delegated") || !strings.Contains(got, "BidiGenerateContentConstrained") {
		t.Fatalf("endpoint = %q", got)
	}
}
