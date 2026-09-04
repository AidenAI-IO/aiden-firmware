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
	interruptEvents := make(chan map[string]any, 2)
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
		sessionSettings, _ := update["session"].(map[string]any)
		audioSettings, _ := sessionSettings["audio"].(map[string]any)
		inputSettings, _ := audioSettings["input"].(map[string]any)
		transcription, _ := inputSettings["transcription"].(map[string]any)
		if transcription["model"] != DefaultOpenAIInputTranscriptionModel {
			t.Errorf("input transcription = %#v", inputSettings["transcription"])
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
				interruptEvents <- event
				write(map[string]any{"type": "response.done", "response": map[string]any{"id": "resp_1", "status": "cancelled"}})
			case "conversation.item.truncate":
				interruptEvents <- event
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
	if got := session.Info(); got.ID != "sess_1" || got.InputSampleRate != 24000 || got.OutputSampleRate != 24000 || got.InputAudioFormat != (AudioFormat{Encoding: "pcm_s16le", SampleRate: 24000, Channels: 1, BitDepth: 16}) || got.OutputAudioFormat != (AudioFormat{Encoding: "pcm_s16le", SampleRate: 24000, Channels: 1, BitDepth: 16}) {
		t.Fatalf("session info = %+v", got)
	}
	if err := session.SendAudio(context.Background(), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := session.(ResponseInterrupter).Interrupt(context.Background(), ResponseInterruption{ItemID: "item_1", AudioEndMS: 125}); err != nil {
		t.Fatal(err)
	}
	for _, wantType := range []string{"response.cancel", "conversation.item.truncate"} {
		select {
		case event := <-interruptEvents:
			if event["type"] != wantType {
				t.Fatalf("interrupt event type = %q, want %q", event["type"], wantType)
			}
			if wantType == "conversation.item.truncate" {
				if event["item_id"] != "item_1" || event["audio_end_ms"] != float64(125) || event["content_index"] != float64(0) {
					t.Fatalf("truncate event = %#v", event)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", wantType)
		}
	}
	if err := session.(ToolResultSender).SendToolResult(context.Background(), "call_1", `{"ok":true}`); err != nil {
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

func TestOpenAISessionUpdateUsesNegotiatedRates(t *testing.T) {
	update := buildOpenAISessionUpdate(SessionConfig{InputSampleRate: 16000, OutputSampleRate: 24000}, "gpt-realtime")
	if update.Session.Audio.Input.Format.Rate != 16000 || update.Session.Audio.Output.Format.Rate != 24000 {
		t.Fatalf("audio rates = %d/%d, want 16000/24000", update.Session.Audio.Input.Format.Rate, update.Session.Audio.Output.Format.Rate)
	}
}

func TestOpenAILegacySessionUpdateUsesBetaFields(t *testing.T) {
	provider := OpenAIProvider{Endpoint: "wss://api.mixroute.ai/v1/realtime"}
	endpoint, err := provider.endpoint("gpt-realtime-2")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("model"); got != "gpt-realtime-2" {
		t.Fatalf("model query = %q, want gpt-realtime-2", got)
	}

	threshold := 0.5
	update := buildOpenAISessionUpdateForProtocol(SessionConfig{
		Instructions: "be concise", Voice: "alloy", InputSampleRate: 16000, OutputSampleRate: 24000,
		TurnDetection: "server_vad", TurnDetectionThresh: &threshold, TurnDetectionSilenceMs: 550,
	}, "gpt-realtime-2", "legacy")
	body, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if model, ok := payload.Session["model"]; ok {
		t.Fatalf("legacy session update must select the model only through the URL, got model=%v: %s", model, body)
	}
	text := string(body)
	for _, forbidden := range []string{"output_modalities", `"audio":`, `"type":"realtime"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("legacy payload contains GA field %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{`"modalities":["audio","text"]`, `"input_audio_format":"pcm16"`, `"output_audio_format":"pcm16"`, `"input_audio_transcription":{"model":"whisper-1"}`, `"silence_duration_ms":550`} {
		if !strings.Contains(text, required) {
			t.Fatalf("legacy payload missing %q: %s", required, text)
		}
	}
}

func TestOpenAIProtocolAliases(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "ga"}, {"ga", "ga"}, {"legacy", "legacy"}, {"beta", "legacy"}, {" LEGACY ", "legacy"},
	} {
		if got := normalizeRealtimeProtocol(tc.input); got != tc.want {
			t.Errorf("normalizeRealtimeProtocol(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestOpenAIContextReplayUsesProtocolSpecificAssistantContentType(t *testing.T) {
	item := ContextItem{Type: "message", Role: "assistant", Content: "prior answer"}
	for _, tc := range []struct {
		name     string
		protocol string
		want     string
	}{
		{name: "GA", protocol: "", want: "output_text"},
		{name: "legacy", protocol: "legacy", want: "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := openAIContextItemPayload(item, tc.protocol)
			content, ok := payload["content"].([]map[string]string)
			if !ok || len(content) != 1 || content[0]["type"] != tc.want || content[0]["text"] != item.Content {
				t.Fatalf("content = %#v, want type %q and text %q", payload["content"], tc.want, item.Content)
			}
		})
	}
}

func TestOpenAIOutputEventAliasesNormalize(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want EventKind
	}{
		{name: "text", raw: `{"type":"response.output_text.delta","delta":"hello"}`, want: EventTranscriptDelta},
		{name: "audio transcript delta", raw: `{"type":"response.output_audio_transcript.delta","delta":"hello"}`, want: EventTranscriptDelta},
		{name: "audio transcript done", raw: `{"type":"response.output_audio_transcript.done","transcript":"hello"}`, want: EventTranscriptFinal},
		{name: "audio", raw: `{"type":"response.output_audio.delta","delta":"AQI="}`, want: EventAudio},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, ok := translateOpenAIEvent([]byte(tc.raw))
			if !ok || event.Kind != tc.want {
				t.Fatalf("event = %+v, ok=%t, want kind %s", event, ok, tc.want)
			}
		})
	}
}

func TestOpenAIResponseDoneFailureIsError(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			event, ok := translateOpenAIEvent([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"` + status + `"}}`))
			if !ok || event.Kind != EventError || event.Error == nil {
				t.Fatalf("event = %+v, ok=%t; want provider failure", event, ok)
			}
			if !strings.Contains(event.Error.Error(), status) {
				t.Fatalf("error = %v, want status %q", event.Error, status)
			}
		})
	}
}
