package realtimevoice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type capturedSpekoFrame struct {
	typ  int
	body []byte
}

func TestSpekoProviderWireProtocol(t *testing.T) {
	frames := make(chan capturedSpekoFrame, 8)
	var requestMu sync.Mutex
	var gotAuth string
	var gotRequest spekoSessionCreate
	upgrader := websocket.Upgrader{Subprotocols: []string{"short-token"}, CheckOrigin: func(*http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions":
			requestMu.Lock()
			gotAuth = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
				requestMu.Unlock()
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			requestMu.Unlock()
			wsURL := "ws://" + r.Host + "/ws"
			_ = json.NewEncoder(w).Encode(spekoCredentials{SessionID: "session-1", WSURL: wsURL, WSToken: "short-token", InputSampleRate: 16000, OutputSampleRate: 24000})
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			if conn.Subprotocol() != "short-token" {
				return
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"ready","inputSampleRate":16000,"outputSampleRate":24000}`))
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte{9, 8, 7})
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"transcript","role":"assistant","text":"hello","final":false}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"interruption","at":"user"}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"tool_call","callId":"call-1","name":"clock","arguments":"{}"}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"transcript","role":"assistant","text":"hello","final":true}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"usage","inputAudioTokens":11,"outputAudioTokens":7}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":"error","code":"UPSTREAM","message":"upstream failed"}`))
			for i := 0; i < 4; i++ {
				typ, body, err := conn.ReadMessage()
				if err != nil {
					return
				}
				frames <- capturedSpekoFrame{typ: typ, body: body}
			}
		}
	}))
	defer server.Close()

	provider := SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, AgentID: "agent-1", UpstreamProvider: "openai"}
	session, err := provider.Open(context.Background(), SessionConfig{APIKey: "secret", Model: "gpt-realtime", Voice: "alloy", Instructions: "be concise", InputSampleRate: 16000, OutputSampleRate: 24000, Tools: []Tool{{Name: "clock", Parameters: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	info := session.Info()
	if info.ID != "session-1" || info.InputSampleRate != 16000 || info.OutputSampleRate != 24000 {
		t.Fatalf("session info = %+v", info)
	}
	requestMu.Lock()
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotRequest.Mode != "s2s" || gotRequest.AgentID != "agent-1" || gotRequest.S2S.Provider != "openai" || gotRequest.S2S.Model != "gpt-realtime" {
		t.Fatalf("session request = %+v", gotRequest)
	}
	requestMu.Unlock()

	inputFrame := bytes.Repeat([]byte{1, 2}, 320)
	if err := session.SendAudio(context.Background(), inputFrame); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := session.SendToolResult(context.Background(), "call-1", `{"now":"12:00"}`); err != nil {
		t.Fatal(err)
	}

	gotFrames := make([]capturedSpekoFrame, 0, 4)
	for len(gotFrames) < 4 {
		select {
		case frame := <-frames:
			gotFrames = append(gotFrames, frame)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for client frames")
		}
	}
	if gotFrames[0].typ != websocket.BinaryMessage || !bytes.Equal(gotFrames[0].body, inputFrame) {
		t.Fatalf("audio frame = %+v", gotFrames[0])
	}
	wantJSON := []string{`"action":"commit"`, `"t":"interrupt"`, `"t":"tool_result"`}
	for i, want := range wantJSON {
		if gotFrames[i+1].typ != websocket.TextMessage || !strings.Contains(string(gotFrames[i+1].body), want) {
			t.Fatalf("frame %d = %s, want %s", i+1, gotFrames[i+1].body, want)
		}
	}

	wantKinds := []EventKind{EventReady, EventAudio, EventTranscriptDelta, EventInterruption, EventToolCall, EventTranscriptFinal, EventResponseDone, EventError}
	for _, want := range wantKinds {
		select {
		case event := <-session.Events():
			if event.Kind != want {
				t.Fatalf("event kind = %s, want %s", event.Kind, want)
			}
			if want == EventAudio && !bytes.Equal(event.PCM, []byte{9, 8, 7}) {
				t.Fatalf("audio = %v", event.PCM)
			}
			if want == EventToolCall && (event.CallID != "call-1" || event.Name != "clock") {
				t.Fatalf("tool event = %+v", event)
			}
			if want == EventResponseDone && (event.Status != "completed" || event.Usage.InputTokens != 11 || event.Usage.OutputTokens != 7) {
				t.Fatalf("response done event = %+v", event)
			}
			if want == EventError && (event.Error == nil || event.Error.Error() != "upstream failed") {
				t.Fatalf("error event = %+v", event)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestSpekoProviderRejectsUnsupportedSampleRate(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"token"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			_ = json.NewEncoder(w).Encode(spekoCredentials{
				WSURL:            "ws://" + r.Host + "/ws",
				WSToken:          "token",
				InputSampleRate:  8000,
				OutputSampleRate: 24000,
			})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()

	_, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "openai"}).Open(
		context.Background(), SessionConfig{APIKey: "secret"},
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported input sample rate 8000") {
		t.Fatalf("Open() error = %v, want unsupported input sample rate", err)
	}
}

func TestSpekoSessionFramesInputAudioAtTwentyMilliseconds(t *testing.T) {
	frameSizes := make(chan []int, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{"token"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			_ = json.NewEncoder(w).Encode(spekoCredentials{
				WSURL:            "ws://" + r.Host + "/ws",
				WSToken:          "token",
				InputSampleRate:  16000,
				OutputSampleRate: 24000,
			})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		sizes := make([]int, 0, 2)
		for len(sizes) < 2 {
			typ, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ == websocket.BinaryMessage {
				sizes = append(sizes, len(body))
			}
		}
		frameSizes <- sizes
	}))
	defer server.Close()

	session, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "openai"}).Open(
		context.Background(), SessionConfig{APIKey: "secret", InputSampleRate: 16000},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.SendAudio(context.Background(), bytes.Repeat([]byte{1}, 700)); err != nil {
		t.Fatal(err)
	}
	if err := session.SendAudio(context.Background(), bytes.Repeat([]byte{2}, 580)); err != nil {
		t.Fatal(err)
	}
	select {
	case sizes := <-frameSizes:
		if len(sizes) != 2 || sizes[0] != 640 || sizes[1] != 640 {
			t.Fatalf("input frame sizes = %v, want two 640-byte PCM16 frames", sizes)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for framed input audio")
	}
}

func TestSpekoCommitFlushesPartialInputFrame(t *testing.T) {
	frameSizes := make(chan []byte, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{"token"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			_ = json.NewEncoder(w).Encode(spekoCredentials{WSURL: "ws://" + r.Host + "/ws", WSToken: "token", InputSampleRate: 16000, OutputSampleRate: 24000})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		typ, body, err := conn.ReadMessage()
		if err == nil && typ == websocket.BinaryMessage {
			frameSizes <- body
		}
	}))
	defer server.Close()

	session, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "openai"}).Open(
		context.Background(), SessionConfig{APIKey: "secret", InputSampleRate: 16000},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	partial := bytes.Repeat([]byte{7}, 100)
	if err := session.SendAudio(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frameSizes:
		if len(frame) != 640 || !bytes.Equal(frame[:100], partial) || !bytes.Equal(frame[100:], make([]byte, 540)) {
			t.Fatalf("flushed frame = len:%d prefix:%v suffix_nonzero:%v", len(frame), frame[:min(len(frame), 100)], !bytes.Equal(frame[100:], make([]byte, 540)))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for partial frame flush")
	}
}

func TestSpekoSessionSerializesConcurrentWrites(t *testing.T) {
	const writes = 32
	received := make(chan int, 1)
	upgrader := websocket.Upgrader{Subprotocols: []string{"token"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			_ = json.NewEncoder(w).Encode(spekoCredentials{WSURL: "ws://" + r.Host + "/ws", WSToken: "token", InputSampleRate: 16000, OutputSampleRate: 24000})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		count := 0
		for count < writes {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
			count++
		}
		received <- count
	}))
	defer server.Close()

	session, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "openai"}).Open(context.Background(), SessionConfig{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var wg sync.WaitGroup
	errs := make(chan error, writes)
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func(value byte) {
			defer wg.Done()
			errs <- session.SendAudio(context.Background(), bytes.Repeat([]byte{value}, 640))
		}(byte(i))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	select {
	case count := <-received:
		if count != writes {
			t.Fatalf("received %d frames, want %d", count, writes)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for concurrent frames")
	}
}

func TestSpekoProviderRejectsStaleCascadeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"transportUrl": "wss://example.test", "transportToken": "old"})
	}))
	defer server.Close()
	_, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "openai"}).Open(context.Background(), SessionConfig{APIKey: "secret"})
	if err == nil || !strings.Contains(err.Error(), "wsUrl/wsToken") {
		t.Fatalf("error = %v", err)
	}
}

func TestSpekoSessionReportsMalformedTextFrame(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{"token"}, CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions" {
			_ = json.NewEncoder(w).Encode(spekoCredentials{
				WSURL:            "ws://" + r.Host + "/ws",
				WSToken:          "token",
				InputSampleRate:  16000,
				OutputSampleRate: 24000,
			})
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"t":`))
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	session, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "openai"}).Open(
		context.Background(), SessionConfig{APIKey: "secret"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	select {
	case err := <-session.Errors():
		if err == nil || !strings.Contains(err.Error(), "decode text frame") {
			t.Fatalf("session error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for malformed frame error")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("session stayed open after malformed frame")
	}
}
