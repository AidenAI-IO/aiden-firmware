package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOpenAIProviderNormalizesRealtimeSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer openai-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if got := r.URL.Query().Get("model"); got != "gpt-realtime" {
			t.Errorf("model query = %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		write := func(v any) {
			if err := conn.WriteJSON(v); err != nil {
				t.Error(err)
			}
		}
		write(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess_1", "model": "gpt-realtime"}})
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		var update map[string]any
		if err := json.Unmarshal(body, &update); err != nil {
			t.Error(err)
			return
		}
		if update["type"] != "session.update" {
			t.Errorf("first client event = %#v", update)
		}
		write(map[string]any{"type": "session.updated", "session": map[string]any{"id": "sess_1"}})

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
			switch event["type"] {
			case "input_audio_buffer.append":
				if _, ok := event["audio"].(string); !ok {
					t.Errorf("audio event = %#v", event)
				}
			case "response.cancel":
				write(map[string]any{"type": "response.done", "response": map[string]any{"id": "resp_1", "status": "cancelled"}})
			case "conversation.item.create":
				write(map[string]any{"type": "response.function_call_arguments.done", "response_id": "resp_1", "call_id": "call_1", "name": "clock", "arguments": `{"timezone":"UTC"}`})
				write(map[string]any{"type": "response.audio.delta", "delta": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})})
			case "response.create":
				write(map[string]any{"type": "response.audio_transcript.done", "transcript": "done"})
			}
		}
	}))
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/realtime"
	session, err := (OpenAIProvider{Endpoint: endpoint}).Open(context.Background(), SessionConfig{
		APIKey: "openai-key", Model: "gpt-realtime", Voice: "alloy", Instructions: "be concise",
		TurnDetection: "server_vad", Tools: []Tool{{Name: "clock", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Info(); got.ID != "sess_1" || got.InputSampleRate != 24000 || got.OutputSampleRate != 24000 || !got.Capabilities.TextInput {
		t.Fatalf("session info = %+v", got)
	}
	if err := session.SendAudio(context.Background(), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.SendToolResult(context.Background(), "call_1", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	textSession, ok := session.(TextSession)
	if !ok {
		t.Fatal("OpenAI session does not support text input")
	}
	if err := textSession.CreateResponse(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []EventKind{EventResponseCancelled, EventToolCall, EventAudio, EventTranscriptFinal}
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

func TestOpenAIEndpointAcceptsHTTPBaseURL(t *testing.T) {
	provider := OpenAIProvider{Endpoint: "https://api.example.test/v1/realtime"}
	got, err := provider.endpoint("gpt-realtime")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "wss" || u.Query().Get("model") != "gpt-realtime" {
		t.Fatalf("endpoint = %q", got)
	}
}
