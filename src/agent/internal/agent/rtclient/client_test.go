package rtclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientConnectAndEvents(t *testing.T) {
	got := make(chan map[string]any, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-DashScope-WorkSpace") != "ws" {
			t.Errorf("workspace header = %q", r.Header.Get("X-DashScope-WorkSpace"))
		}
		if r.URL.Query().Get("model") != "model-a" {
			t.Errorf("model = %q", r.URL.Query().Get("model"))
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"type": "session.created", "event_id": "evt-1"})
		for {
			_, b, err := c.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				got <- m
			}
		}
	}))
	defer srv.Close()

	c, err := New(Config{APIKey: "key", WorkspaceID: "ws", Model: "model-a", Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case e, ok := <-s.Events():
		if !ok {
			select {
			case err := <-s.Errors():
				t.Fatalf("events closed: %v", err)
			default:
				t.Fatal("events closed")
			}
		}
		if e.Type != "session.created" || e.EventID != "evt-1" {
			t.Fatalf("event = %#v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("event timeout")
	}
	if err := s.AppendAudio(context.Background(), []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m["type"] != EventInputAudioAppend || m["audio"] != "AQID" {
			t.Fatalf("append = %#v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("send timeout")
	}
}

func TestSessionConfigPushToTalk(t *testing.T) {
	b, err := json.Marshal(SessionUpdate{Type: EventSessionUpdate, Session: SessionConfig{PushToTalk: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"turn_detection":null`) {
		t.Fatalf("payload = %s", b)
	}
}

func TestSendHonorsCancellationWhileWaitingToWrite(t *testing.T) {
	gate := make(chan struct{}, 1)
	s := &Session{writeGate: gate, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, SimpleEvent{Type: "test"}); err != context.Canceled {
		t.Fatalf("Send error = %v, want context.Canceled", err)
	}
}

func TestSingaporeDefaultEndpoint(t *testing.T) {
	c, err := New(Config{APIKey: "key", Region: "ap-southeast-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.cfg.Endpoint, "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}
