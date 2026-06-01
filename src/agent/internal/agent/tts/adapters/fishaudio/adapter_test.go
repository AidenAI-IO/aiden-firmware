package fishaudio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"

	"aiden-agent/internal/agent/tts"
)

func TestFishAudioOmitsEmptyReferenceIDForDefaultVoice(t *testing.T) {
	startMsg := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("model") != "s2-pro" {
			t.Errorf("model header = %q, want s2-pro", r.Header.Get("model"))
		}
		conn, err := websocket.Upgrade(w, r, nil, 1024, 1024)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read start: %v", err)
			return
		}
		var msg map[string]any
		if err := msgpack.Unmarshal(raw, &msg); err != nil {
			t.Errorf("unmarshal start: %v", err)
			return
		}
		startMsg <- msg
		finish, _ := msgpack.Marshal(map[string]any{"event": "finish"})
		_ = conn.WriteMessage(websocket.BinaryMessage, finish)
	}))
	defer server.Close()

	provider, err := New(tts.ProviderConfig{APIKey: "test-key", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer provider.Close()

	session, err := provider.BeginStream(context.Background(), noopSink{format: tts.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	defer session.Close()

	msg := <-startMsg
	request, ok := msg["request"].(map[string]any)
	if !ok {
		t.Fatalf("request = %#v, want map", msg["request"])
	}
	if _, ok := request["reference_id"]; ok {
		t.Fatalf("reference_id was sent for default voice: %#v", request)
	}
	if request["text"] != "" {
		t.Fatalf("text = %#v, want empty string", request["text"])
	}
	if request["latency"] != "balanced" {
		t.Fatalf("latency = %#v, want balanced", request["latency"])
	}
	if _, ok := request["channels"]; ok {
		t.Fatalf("channels should not be sent in Fish Audio TTSRequest: %#v", request)
	}
	if _, ok := request["speed"]; ok {
		t.Fatalf("top-level speed should not be sent in Fish Audio TTSRequest: %#v", request)
	}
}

func TestFishAudioCloseReturnsWhenServerNeverFinishes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r, nil, 1024, 1024)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()

	provider, err := New(tts.ProviderConfig{APIKey: "test-key", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	session, err := provider.BeginStream(ctx, noopSink{format: tts.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Close() }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		server.CloseClientConnections()
		t.Fatal("Close() blocked waiting for server finish")
	}
}

type noopSink struct{ format tts.AudioFormat }

func (s noopSink) Format() tts.AudioFormat { return s.format }
func (noopSink) WritePCM([]byte) error     { return nil }
func (noopSink) Drain(context.Context) error {
	return nil
}
func (noopSink) Stop() error { return nil }
