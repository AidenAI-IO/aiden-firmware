package volcengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"aiden-agent/internal/agent/tts"
)

func TestVolcengineStreamsTextAndAudio(t *testing.T) {
	var sawAPIKey, sawResourceID bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "test-key" {
			sawAPIKey = true
		}
		if r.Header.Get("X-Api-Resource-Id") == "seed-tts-2.0" {
			sawResourceID = true
		}
		conn, err := websocket.Upgrade(w, r, nil, 1024, 1024)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read start connection: %v", err)
			return
		}
		msg, err := parseServerFrame(raw)
		if err != nil || msg.event != eventStartConnection {
			t.Errorf("start connection = %#v err=%v", msg, err)
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeServerJSONFrame(eventConnectionStarted, "conn-1", "", []byte("{}")))

		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Errorf("read start session: %v", err)
			return
		}
		msg, err = parseServerFrame(raw)
		if err != nil || msg.event != eventStartSession || !strings.Contains(string(msg.payload), "test-speaker") {
			t.Errorf("start session = %#v payload=%s err=%v", msg, msg.payload, err)
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeServerJSONFrame(eventSessionStarted, "", msg.sessionID, []byte(`{"status_code":20000000,"message":"ok"}`)))

		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Errorf("read task request: %v", err)
			return
		}
		msg, err = parseServerFrame(raw)
		if err != nil || msg.event != eventTaskRequest || !strings.Contains(string(msg.payload), "hello") {
			t.Errorf("task request = %#v payload=%s err=%v", msg, msg.payload, err)
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeServerAudioFrame(eventTTSResponse, msg.sessionID, []byte{1, 2, 3, 4}))

		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Errorf("read finish session: %v", err)
			return
		}
		msg, err = parseServerFrame(raw)
		if err != nil || msg.event != eventFinishSession {
			t.Errorf("finish session = %#v err=%v", msg, err)
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeServerJSONFrame(eventSessionFinished, "", msg.sessionID, []byte(`{"status_code":20000000,"message":"ok"}`)))

		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Errorf("read finish connection: %v", err)
			return
		}
		msg, err = parseServerFrame(raw)
		if err != nil || msg.event != eventFinishConnection {
			t.Errorf("finish connection = %#v err=%v", msg, err)
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, encodeServerJSONFrame(eventConnectionFinished, "conn-1", "", []byte("{}")))
	}))
	defer server.Close()

	provider, err := New(tts.ProviderConfig{Provider: ProviderName, APIKey: "test-key", Voice: "test-speaker", Extra: map[string]any{"model": "seed-tts-2.0"}, Endpoint: "ws" + strings.TrimPrefix(server.URL, "http")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sink := &recordingSink{format: tts.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}}
	session, err := provider.BeginStream(context.Background(), sink)
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	if err := session.WriteText("hello"); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !sawAPIKey || !sawResourceID {
		t.Fatalf("headers saw api_key=%v resource_id=%v", sawAPIKey, sawResourceID)
	}
	if string(sink.pcm) != string([]byte{1, 2, 3, 4}) || !sink.drained {
		t.Fatalf("sink pcm=%v drained=%v", sink.pcm, sink.drained)
	}
}

type recordingSink struct {
	format  tts.AudioFormat
	pcm     []byte
	drained bool
}

func (s *recordingSink) Format() tts.AudioFormat { return s.format }

func (s *recordingSink) WritePCM(data []byte) error {
	s.pcm = append(s.pcm, data...)
	return nil
}

func (s *recordingSink) Drain(context.Context) error {
	s.drained = true
	return nil
}

func (s *recordingSink) Stop() error { return nil }
