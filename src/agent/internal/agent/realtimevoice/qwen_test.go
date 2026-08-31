package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestQwenProviderPreservesDashScopeProtocol(t *testing.T) {
	clientEvents := make(chan map[string]any, 16)
	updates := make(chan map[string]any, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer qwen-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("X-DashScope-WorkSpace"); got != "workspace-1" {
			t.Errorf("workspace header = %q", got)
		}
		if got := r.URL.Query().Get("model"); got != DefaultQwenRealtimeModel {
			t.Errorf("model query = %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{"type": "session.created", "session": map[string]any{"id": "qwen-session"}}); err != nil {
			t.Error(err)
			return
		}
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
		updates <- update
		if err := conn.WriteJSON(map[string]any{"type": "session.updated", "session": map[string]any{"id": "qwen-session"}}); err != nil {
			t.Error(err)
			return
		}
		for {
			_, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var event map[string]any
			if err := json.Unmarshal(body, &event); err != nil {
				t.Error(err)
				return
			}
			clientEvents <- event
		}
	}))
	defer server.Close()

	emotion := true
	threshold := 0.25
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	session, err := (QwenProvider{Endpoint: endpoint, WorkspaceID: "workspace-1"}).Open(context.Background(), SessionConfig{
		APIKey:                 "qwen-key",
		Instructions:           "be concise",
		InputAudioFormat:       "pcm",
		OutputAudioFormat:      "pcm",
		MaxHistoryTurns:        12,
		EnableSpeechEmotion:    &emotion,
		TurnDetection:          "server_vad",
		TurnDetectionThresh:    &threshold,
		TurnDetectionSilenceMs: 650,
		Tools:                  []Tool{{Name: "clock", Description: "get time", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Info(); got.ID != "qwen-session" || got.InputSampleRate != 16000 || got.OutputSampleRate != 24000 {
		t.Fatalf("session info = %+v", got)
	}

	select {
	case update := <-updates:
		if update["type"] != "session.update" {
			t.Fatalf("session update = %#v", update)
		}
		settings := update["session"].(map[string]any)
		if settings["voice"] != "longanqian" || settings["enable_speech_emotion"] != true || settings["max_history_turns"] != float64(12) {
			t.Fatalf("qwen settings = %#v", settings)
		}
		turnDetection := settings["turn_detection"].(map[string]any)
		if turnDetection["type"] != "server_vad" || turnDetection["threshold"] != 0.25 || turnDetection["silence_duration_ms"] != float64(650) {
			t.Fatalf("turn detection = %#v", turnDetection)
		}
		tools := settings["tools"].([]any)
		function := tools[0].(map[string]any)["function"].(map[string]any)
		if function["name"] != "clock" || function["description"] != "get time" {
			t.Fatalf("qwen tool = %#v", function)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Qwen session.update")
	}

	assertClientEvent := func(call func() error, wantType string, check func(map[string]any)) {
		t.Helper()
		if err := call(); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-clientEvents:
			if event["type"] != wantType {
				t.Fatalf("client event = %#v, want type %q", event, wantType)
			}
			if check != nil {
				check(event)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", wantType)
		}
	}
	assertClientEvent(func() error { return session.SendAudio(context.Background(), []byte{1, 2, 3}) }, "input_audio_buffer.append", func(event map[string]any) {
		if event["audio"] != "AQID" {
			t.Fatalf("audio event = %#v", event)
		}
	})
	assertClientEvent(func() error { return session.(TurnCommitter).Commit(context.Background()) }, "input_audio_buffer.commit", nil)
	assertClientEvent(func() error {
		return session.(ResponseInterrupter).Interrupt(context.Background(), ResponseInterruption{})
	}, "response.cancel", nil)
	assertClientEvent(func() error {
		return session.(ToolResultSender).SendToolResult(context.Background(), "call-1", `{"ok":true}`)
	}, "conversation.item.create", func(event map[string]any) {
		item := event["item"].(map[string]any)
		if item["type"] != "function_call_output" || item["call_id"] != "call-1" || item["output"] != `{"ok":true}` {
			t.Fatalf("tool result = %#v", item)
		}
	})
	textSession := session.(TextSession)
	assertClientEvent(func() error { return textSession.SendText(context.Background(), "hello") }, "conversation.item.create", func(event map[string]any) {
		item := event["item"].(map[string]any)
		if item["type"] != "message" || item["role"] != "user" {
			t.Fatalf("text item = %#v", item)
		}
	})
	assertClientEvent(func() error { return textSession.CreateResponse(context.Background()) }, "response.create", nil)
	assertClientEvent(func() error {
		return session.(ContextReplayer).ReplayContext(context.Background(), []ContextItem{{Type: "function_call", CallID: "call-2", Name: "clock", Arguments: `{}`}})
	}, "conversation.item.create", func(event map[string]any) {
		item := event["item"].(map[string]any)
		if item["type"] != "function_call" || item["call_id"] != "call-2" || item["name"] != "clock" {
			t.Fatalf("replayed item = %#v", item)
		}
	})
}

func TestQwenProviderEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		provider QwenProvider
		want     string
	}{
		{
			name:     "legacy Beijing endpoint",
			provider: QwenProvider{Region: "cn-beijing"},
			want:     "wss://dashscope.aliyuncs.com/api-ws/v1/realtime?model=qwen-model",
		},
		{
			name:     "Beijing workspace endpoint",
			provider: QwenProvider{Region: "cn-beijing", WorkspaceID: "workspace-1"},
			want:     "wss://workspace-1.cn-beijing.maas.aliyuncs.com/api-ws/v1/realtime?model=qwen-model",
		},
		{
			name:     "legacy Singapore endpoint",
			provider: QwenProvider{Region: "ap-southeast-1"},
			want:     "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime?model=qwen-model",
		},
		{
			name:     "Singapore workspace endpoint",
			provider: QwenProvider{Region: "ap-southeast-1", WorkspaceID: "workspace-1"},
			want:     "wss://workspace-1.ap-southeast-1.maas.aliyuncs.com/api-ws/v1/realtime?model=qwen-model",
		},
		{
			name:     "endpoint override preserves other query parameters",
			provider: QwenProvider{Endpoint: "wss://example.com/realtime?version=1"},
			want:     "wss://example.com/realtime?model=qwen-model&version=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.provider.endpoint("qwen-model")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("endpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQwenProviderRejectsUnsupportedRegion(t *testing.T) {
	_, err := (QwenProvider{Region: "us-west-1"}).endpoint("qwen-model")
	if err == nil || !strings.Contains(err.Error(), "unsupported region") {
		t.Fatalf("endpoint error = %v, want unsupported region", err)
	}
}

func TestTranslateQwenEvents(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		kind  EventKind
		check func(*testing.T, Event)
	}{
		{name: "speech", raw: `{"type":"input_audio_buffer.speech_started"}`, kind: EventSpeechStarted},
		{name: "audio", raw: `{"type":"response.audio.delta","delta":"` + base64.StdEncoding.EncodeToString([]byte{1, 2}) + `"}`, kind: EventAudio, check: func(t *testing.T, e Event) {
			if string(e.PCM) != string([]byte{1, 2}) {
				t.Fatalf("pcm = %v", e.PCM)
			}
		}},
		{name: "transcript", raw: `{"type":"response.audio_transcript.delta","delta":"hi"}`, kind: EventTranscriptDelta, check: func(t *testing.T, e Event) {
			if e.Text != "hi" || e.TextSource != "audio" {
				t.Fatalf("event = %+v", e)
			}
		}},
		{name: "tool", raw: `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"clock","arguments":"{}"}`, kind: EventToolCall, check: func(t *testing.T, e Event) {
			if e.ResponseID != "r1" || e.CallID != "c1" || e.Name != "clock" {
				t.Fatalf("event = %+v", e)
			}
		}},
		{name: "done", raw: `{"type":"response.done","response":{"id":"r1","status":"completed","usage":{"total_tokens":3,"input_tokens":1,"output_tokens":2}}}`, kind: EventResponseDone, check: func(t *testing.T, e Event) {
			if e.Usage.TotalTokens != 3 {
				t.Fatalf("usage = %+v", e.Usage)
			}
		}},
		{name: "error", raw: `{"type":"error","error":{"message":"boom"}}`, kind: EventError, check: func(t *testing.T, e Event) {
			if e.Error == nil || e.Error.Error() != "boom" {
				t.Fatalf("error = %v", e.Error)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, ok := translateQwenEvent([]byte(tt.raw))
			if !ok || event.Kind != tt.kind {
				t.Fatalf("event = %+v, ok=%t", event, ok)
			}
			if tt.check != nil {
				tt.check(t, event)
			}
		})
	}
}

func TestQwenResponseDoneFailureIsError(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			event, ok := translateQwenEvent([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"` + status + `"}}`))
			if !ok || event.Kind != EventError || event.Error == nil {
				t.Fatalf("event = %+v, ok=%t; want provider failure", event, ok)
			}
			if !strings.Contains(event.Error.Error(), status) {
				t.Fatalf("error = %v, want status %q", event.Error, status)
			}
		})
	}
}
