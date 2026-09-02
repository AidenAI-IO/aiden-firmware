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

func TestXAIProviderNormalizesRealtimeSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if got := r.URL.Query().Get("model"); got != "grok-voice-latest" {
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
		write(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess_1", "model": "grok-voice-latest"}})
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
		if session, ok := update["session"].(map[string]any); ok {
			if session["type"] != "realtime" || session["model"] != "grok-voice-latest" {
				t.Errorf("session identity = %#v", session)
			}
			if session["voice"] != "eve" {
				t.Errorf("session voice = %#v, want top-level eve", session["voice"])
			}
			audio := session["audio"].(map[string]any)
			input := audio["input"].(map[string]any)
			output := audio["output"].(map[string]any)
			if input["format"].(map[string]any)["rate"] != float64(16000) || output["format"].(map[string]any)["rate"] != float64(24000) {
				t.Errorf("audio rates = %#v", audio)
			}
			if input["transcription"].(map[string]any)["model"] != "grok-transcribe" {
				t.Errorf("transcription = %#v", input["transcription"])
			}
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
				write(map[string]any{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})})
			case "response.create":
				response, ok := event["response"].(map[string]any)
				if !ok {
					t.Errorf("response.create missing response object: %#v", event)
					return
				}
				metadata, ok := response["metadata"].(map[string]any)
				clientEventID, _ := metadata["client_event_id"].(string)
				if !ok || strings.TrimSpace(clientEventID) == "" {
					t.Errorf("response.create missing metadata.client_event_id: %#v", event)
					return
				}
				write(map[string]any{"type": "response.audio_transcript.done", "transcript": "done"})
			}
		}
	}))
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/realtime"
	session, err := (XAIProvider{Endpoint: endpoint}).Open(context.Background(), SessionConfig{
		APIKey: "xai-key", Model: "grok-voice-latest", Voice: "eve", Instructions: "be concise", InputSampleRate: 16000, OutputSampleRate: 24000,
		TurnDetection: "server_vad", Tools: []Tool{{Name: "clock", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Info(); got.ID != "sess_1" || got.InputSampleRate != 16000 || got.OutputSampleRate != 24000 || got.InputAudioFormat != (AudioFormat{Encoding: "pcm_s16le", SampleRate: 16000, Channels: 1, BitDepth: 16}) || got.OutputAudioFormat != (AudioFormat{Encoding: "pcm_s16le", SampleRate: 24000, Channels: 1, BitDepth: 16}) {
		t.Fatalf("session info = %+v", got)
	}
	if err := session.SendAudio(context.Background(), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := session.(ResponseInterrupter).Interrupt(context.Background(), ResponseInterruption{}); err != nil {
		t.Fatal(err)
	}
	if err := session.(ToolResultSender).SendToolResult(context.Background(), "call_1", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	textSession, ok := session.(TextSession)
	if !ok {
		t.Fatal("XAI session does not support text input")
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

func TestXAITranscriptionUpdatedNormalizesCumulativeText(t *testing.T) {
	s := &xAISession{transcripts: make(map[string]string)}
	first, ok := s.translateXAIEventForSession([]byte(`{"type":"conversation.item.input_audio_transcription.updated","item_id":"item_1","transcript":"hel"}`))
	if !ok || first.Kind != EventTranscriptDelta || first.Text != "hel" {
		t.Fatalf("first = %+v, ok=%t", first, ok)
	}
	second, ok := s.translateXAIEventForSession([]byte(`{"type":"conversation.item.input_audio_transcription.updated","item_id":"item_1","transcript":"hello"}`))
	if !ok || second.Kind != EventTranscriptDelta || second.Text != "lo" {
		t.Fatalf("second = %+v, ok=%t", second, ok)
	}
	if _, ok := s.translateXAIEventForSession([]byte(`{"type":"conversation.item.input_audio_transcription.updated","item_id":"item_1","transcript":"he"}`)); ok {
		t.Fatal("correction should not be appended as a delta")
	}
	final, ok := s.translateXAIEventForSession([]byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"hello"}`))
	if !ok || final.Kind != EventTranscriptFinal || final.Text != "hello" {
		t.Fatalf("final = %+v, ok=%t", final, ok)
	}
}

func TestXAIErrorIncludesRawPayload(t *testing.T) {
	event, ok := translateXAIEventBase([]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_event","event_id":"evt_1"}}`))
	if !ok || event.Kind != EventError {
		t.Fatalf("event = %+v, ok=%t", event, ok)
	}
	message := event.Error.Error()
	for _, want := range []string{"invalid_event", "event_id=evt_1", "raw={\"type\":\"error\""} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func TestXAIEndpointAcceptsHTTPBaseURL(t *testing.T) {
	provider := XAIProvider{Endpoint: "https://api.example.test/v1/realtime"}
	got, err := provider.endpoint("grok-voice-latest")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "wss" || u.Query().Get("model") != "grok-voice-latest" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestXAISpekoCredentialUsesClientSecretSubprotocol(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"xai-client-secret.delegated"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-WebSocket-Protocol") != "xai-client-secret.delegated" {
			t.Errorf("subprotocol = %q", r.Header.Get("Sec-WebSocket-Protocol"))
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess_1"}})
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteJSON(map[string]any{"type": "session.updated", "session": map[string]any{"id": "sess_1"}})
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/realtime"
	if _, err := (XAIProvider{Endpoint: endpoint, AuthSubprotocol: true}).Open(context.Background(), SessionConfig{APIKey: "delegated", Model: "grok-voice-latest"}); err != nil {
		t.Fatal(err)
	}
}

func TestXAIIntermediateCompletedDoesNotFinalizeUserTranscript(t *testing.T) {
	// Frame sequence captured from api.x.ai for one spoken turn. xAI re-emits
	// .completed while refining, so only the status=completed frame may become a
	// final transcript; the rest are cumulative and must behave like .updated.
	s := &xAISession{transcripts: make(map[string]string)}
	frames := []string{
		`{"type":"conversation.item.input_audio_transcription.updated","item_id":"item_1","transcript":"今天是。"}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"今天是。","status":"in_progress"}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"今天是几号？","status":"in_progress"}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"今天是几号？","status":"completed"}`,
	}
	var finals []Event
	for i, frame := range frames {
		event, ok := s.translateXAIEventForSession([]byte(frame))
		if !ok {
			continue
		}
		if event.Kind == EventTranscriptFinal {
			finals = append(finals, event)
		} else if event.Kind != EventTranscriptDelta {
			t.Fatalf("frame %d produced %v, want delta or final", i, event.Kind)
		}
	}
	if len(finals) != 1 {
		t.Fatalf("got %d final transcripts, want exactly 1: %+v", len(finals), finals)
	}
	if finals[0].Text != "今天是几号？" {
		t.Fatalf("final text = %q, want the corrected transcript", finals[0].Text)
	}
	if _, present := s.transcripts["item_1"]; present {
		t.Fatal("terminal .completed must clear accumulated transcript state")
	}
}

func TestXAIInProgressCompletedYieldsCumulativeDelta(t *testing.T) {
	s := &xAISession{transcripts: make(map[string]string)}
	first, ok := s.translateXAIEventForSession([]byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"he","status":"in_progress"}`))
	if !ok || first.Kind != EventTranscriptDelta || first.Text != "he" {
		t.Fatalf("first = %+v, ok=%t", first, ok)
	}
	second, ok := s.translateXAIEventForSession([]byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"hello","status":"in_progress"}`))
	if !ok || second.Kind != EventTranscriptDelta || second.Text != "llo" {
		t.Fatalf("second = %+v, ok=%t", second, ok)
	}
}

func TestXAICompletedWithoutStatusStaysTerminal(t *testing.T) {
	// The documented contract has no status field and one terminal .completed.
	// Absent status must keep that behavior so OpenAI-shaped payloads still work.
	event, ok := translateXAIEventBase([]byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","transcript":"hello"}`))
	if !ok || event.Kind != EventTranscriptFinal || event.Text != "hello" || !event.Final {
		t.Fatalf("event = %+v, ok=%t", event, ok)
	}
}

func TestXAIResponseDoneFailureIsError(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			event, ok := translateXAIEventBase([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"` + status + `"}}`))
			if !ok || event.Kind != EventError || event.Error == nil {
				t.Fatalf("event = %+v, ok=%t; want provider failure", event, ok)
			}
			if !strings.Contains(event.Error.Error(), status) {
				t.Fatalf("error = %v, want status %q", event.Error, status)
			}
		})
	}
}
