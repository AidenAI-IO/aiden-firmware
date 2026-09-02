package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"net/http/httptest"
)

func TestGeminiProviderNormalizesLiveSession(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	clientContents := make(chan map[string]any, 1)
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
				clientContents <- event["clientContent"].(map[string]any)
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
	replayer, ok := session.(ContextReplayer)
	if !ok {
		t.Fatal("Gemini session does not support context replay")
	}
	if err := replayer.ReplayContext(context.Background(), []ContextItem{
		{Type: "message", Role: "user", Content: "question"},
		{Type: "message", Role: "assistant", Content: "answer"},
		{Type: "function_call", CallID: "call_old", Name: "clock", Arguments: `{"timezone":"UTC"}`},
		{Type: "function_call_output", CallID: "call_old", Output: `{"time":"12:00"}`},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case content := <-clientContents:
		turns, ok := content["turns"].([]any)
		if !ok || len(turns) != 4 || content["turnComplete"] != false {
			t.Fatalf("replayed client content = %#v", content)
		}
		if turns[0].(map[string]any)["role"] != "user" || turns[1].(map[string]any)["role"] != "model" {
			t.Fatalf("replayed message roles = %#v", turns)
		}
		functionCall := turns[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
		if functionCall["id"] != "call_old" || functionCall["name"] != "clock" {
			t.Fatalf("replayed function call = %#v", functionCall)
		}
		functionResponse := turns[3].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
		if functionResponse["id"] != "call_old" || functionResponse["name"] != "clock" {
			t.Fatalf("replayed function response = %#v", functionResponse)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replayed Gemini context")
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

func TestGeminiVertexUsesOAuthHeaderAndVertexModelResource(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("authorization = %q", got)
		}
		if r.URL.Query().Get("key") != "" || r.URL.Query().Get("access_token") != "" {
			t.Errorf("Vertex credential leaked into query: %s", r.URL.RawQuery)
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
		payload := setup["setup"].(map[string]any)
		wantModel := "projects/project-1/locations/us-central1/publishers/google/models/gemini-live"
		if payload["model"] != wantModel {
			t.Errorf("Vertex model = %q, want %q", payload["model"], wantModel)
		}
		_ = conn.WriteJSON(map[string]any{"setupComplete": map[string]any{}})
	}))
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent"
	session, err := (GeminiProvider{
		Endpoint: endpoint, AuthMode: "vertex", ProjectID: "project-1", Location: "us-central1",
	}).Open(context.Background(), SessionConfig{APIKey: "vertex-token", Model: "gemini-live"})
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Close()

	provider := GeminiProvider{AuthMode: "vertex", ProjectID: "project-1", Location: "europe-west4"}
	got, err := provider.endpoint("gemini-live", "vertex-token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://europe-west4-aiplatform.googleapis.com/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent" {
		t.Fatalf("default Vertex endpoint = %q", got)
	}
}

func TestGeminiSetupRequestsBothTranscriptions(t *testing.T) {
	// Gemini Live only returns transcripts when these keys are present, and the
	// request for defaults is an empty object. With omitempty the empty map was
	// dropped, so no inputTranscription ever arrived and spoken user turns were
	// missing from history while assistant turns still worked.
	encoded, err := json.Marshal(buildGeminiSetup(SessionConfig{}, "gemini-3.1-flash-live-preview"))
	if err != nil {
		t.Fatalf("marshal setup: %v", err)
	}
	var decoded struct {
		Setup map[string]json.RawMessage `json:"setup"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode setup: %v", err)
	}
	for _, key := range []string{"inputAudioTranscription", "outputAudioTranscription"} {
		raw, present := decoded.Setup[key]
		if !present {
			t.Fatalf("setup is missing %s: %s", key, encoded)
		}
		if string(raw) != "{}" {
			t.Fatalf("%s = %s, want {}", key, raw)
		}
	}
	if got := decoded.Setup["model"]; string(got) != `"models/gemini-3.1-flash-live-preview"` {
		t.Fatalf("model = %s", got)
	}
}

func TestGeminiFinalizesUserTranscriptAtTurnBoundary(t *testing.T) {
	// Gemini Live never sets finished on inputTranscription, so a final user
	// transcript can only be produced at the turn boundary. Frame shapes here
	// match what api.google.com sent on hardware.
	s := &geminiSession{toolNames: map[string]string{}}
	for _, frame := range []string{
		`{"serverContent":{"inputTranscription":{"text":"今天"}}}`,
		`{"serverContent":{"inputTranscription":{"text":"是几号？"}}}`,
	} {
		for _, event := range s.translate([]byte(frame)) {
			if event.Kind == EventTranscriptFinal {
				t.Fatalf("no final transcript is possible before the turn ends: %+v", event)
			}
		}
	}
	events := s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"hi"}]}}}`))
	var finalAt, startedAt = -1, -1
	for i, event := range events {
		switch {
		case event.Kind == EventTranscriptFinal && event.Role == "user":
			if event.Text != "今天是几号？" {
				t.Fatalf("final user transcript = %q, want the joined deltas", event.Text)
			}
			finalAt = i
		case event.Kind == EventResponseStarted:
			startedAt = i
		}
	}
	if finalAt < 0 {
		t.Fatalf("modelTurn did not finalize the user transcript: %+v", events)
	}
	if startedAt < 0 || finalAt > startedAt {
		t.Fatalf("user transcript must precede EventResponseStarted, got final=%d started=%d", finalAt, startedAt)
	}
	if _, ok := s.finalUserTranscriptEvent(); ok {
		t.Fatal("accumulated transcript must be cleared after finalizing")
	}
}

func TestGeminiIgnoresLateTranscriptRefinementDuringResponse(t *testing.T) {
	// modelTurn repeats per audio chunk and Gemini keeps refining the transcript
	// after the answer starts. Observed on hardware: one spoken sentence produced
	// two user records, the second a wrong-language re-transcription.
	s := &geminiSession{toolNames: map[string]string{}}
	finals := 0
	countFinals := func(events []Event) {
		for _, event := range events {
			if event.Kind == EventTranscriptFinal && event.Role == "user" {
				finals++
				if event.Text != "我说的是中文" {
					t.Fatalf("final user transcript = %q", event.Text)
				}
			}
		}
	}
	countFinals(s.translate([]byte(`{"serverContent":{"inputTranscription":{"text":"我说的是中文"}}}`)))
	countFinals(s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"a"}]}}}`)))
	countFinals(s.translate([]byte(`{"serverContent":{"inputTranscription":{"text":"o assunto é chinês"}}}`)))
	countFinals(s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"b"}]}}}`)))
	countFinals(s.translate([]byte(`{"serverContent":{"turnComplete":true}}`)))
	if finals != 1 {
		t.Fatalf("got %d final user transcripts for one spoken turn, want 1", finals)
	}
}

func TestGeminiInterruptionStartsNewUserTurn(t *testing.T) {
	// Speech that cuts off the model must still be recorded, so an interruption
	// has to clear the in-flight response and let accumulation resume.
	s := &geminiSession{toolNames: map[string]string{}}
	s.translate([]byte(`{"serverContent":{"inputTranscription":{"text":"first"}}}`))
	s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"answer"}]}}}`))
	s.translate([]byte(`{"serverContent":{"interrupted":true}}`))
	s.translate([]byte(`{"serverContent":{"inputTranscription":{"text":"cut in"}}}`))
	var got string
	for _, event := range s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"next"}]}}}`)) {
		if event.Kind == EventTranscriptFinal && event.Role == "user" {
			got = event.Text
		}
	}
	if got != "cut in" {
		t.Fatalf("interrupting speech was not recorded as a new turn, got %q", got)
	}
}

func TestGeminiSameFrameInterruptionKeepsInputTranscript(t *testing.T) {
	s := &geminiSession{toolNames: map[string]string{}}
	s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"answer"}]}}}`))
	events := s.translate([]byte(`{"serverContent":{"interrupted":true,"inputTranscription":{"text":"cut in"}}}`))
	if len(events) != 2 || events[0].Kind != EventInterruption || events[1].Kind != EventTranscriptDelta {
		t.Fatalf("same-frame events = %+v, want interruption then user transcript", events)
	}
	var got string
	for _, event := range s.translate([]byte(`{"serverContent":{"modelTurn":{"parts":[{"text":"next"}]}}}`)) {
		if event.Kind == EventTranscriptFinal && event.Role == "user" {
			got = event.Text
		}
	}
	if got != "cut in" {
		t.Fatalf("interrupting transcript = %q, want cut in", got)
	}
}

func TestGeminiToolCallFlushesUserTranscriptFirst(t *testing.T) {
	s := &geminiSession{toolNames: map[string]string{}}
	s.translate([]byte(`{"serverContent":{"inputTranscription":{"text":"what time is it"}}}`))
	events := s.translate([]byte(`{"toolCall":{"functionCalls":[{"id":"call_1","name":"clock","args":{}}]}}`))
	if len(events) != 3 {
		t.Fatalf("tool-call events = %+v", events)
	}
	if events[0].Kind != EventTranscriptFinal || events[0].Role != "user" || events[1].Kind != EventResponseStarted || events[2].Kind != EventToolCall {
		t.Fatalf("tool-call event order = %+v", events)
	}
}

func TestGeminiGoAwayReportsRotationNotFailure(t *testing.T) {
	// Gemini Live caps session lifetime and announces the cutoff. Reporting a
	// generic error made a scheduled handover look like a fault, so long
	// conversations ended with no reconnect.
	s := &geminiSession{toolNames: map[string]string{}}
	events := s.translate([]byte(`{"goAway":{"timeLeft":"50s"}}`))
	if len(events) != 1 || events[0].Kind != EventError {
		t.Fatalf("events = %+v", events)
	}
	err := events[0].Error
	if !errors.Is(err, ErrSessionRotated) {
		t.Fatalf("goAway must wrap ErrSessionRotated so callers can reconnect, got %v", err)
	}
	if !strings.Contains(err.Error(), "50s") {
		t.Fatalf("error should keep the announced budget, got %v", err)
	}
}
