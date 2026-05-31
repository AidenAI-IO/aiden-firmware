package minimax

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"

	"aiden-agent/internal/agent/tts"
)

func TestWebSocketRequestsSinkSampleRate(t *testing.T) {
	sampleRates := make(chan int, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{"event": "connected_success"}); err != nil {
			t.Errorf("write connected_success: %v", err)
			return
		}

		var start map[string]any
		if err := conn.ReadJSON(&start); err != nil {
			t.Errorf("read task_start: %v", err)
			return
		}
		audioSetting, ok := start["audio_setting"].(map[string]any)
		if !ok {
			t.Errorf("audio_setting = %#v", start["audio_setting"])
			return
		}
		sampleRates <- int(audioSetting["sample_rate"].(float64))
		if err := conn.WriteJSON(map[string]any{"event": "task_started"}); err != nil {
			t.Errorf("write task_started: %v", err)
			return
		}

		var cont map[string]any
		if err := conn.ReadJSON(&cont); err != nil {
			t.Errorf("read task_continue: %v", err)
			return
		}
		if err := conn.WriteJSON(map[string]any{"is_final": true}); err != nil {
			t.Errorf("write final audio response: %v", err)
			return
		}

		var finish map[string]any
		_ = conn.ReadJSON(&finish)
		_ = conn.WriteJSON(map[string]any{"event": "task_finished"})
	}))
	defer server.Close()

	provider, err := NewWebSocket(tts.ProviderConfig{
		APIKey:   "test-key",
		Endpoint: "ws" + server.URL[len("http"):],
	})
	if err != nil {
		t.Fatalf("NewWebSocket() error = %v", err)
	}
	session, err := provider.BeginStream(context.Background(), noopSink{format: tts.AudioFormat{SampleRate: 32000, Channels: 1, BitWidth: 16}})
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}
	if err := session.WriteText("hello"); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if got := <-sampleRates; got != 32000 {
		t.Fatalf("sample_rate = %d, want sink sample rate 32000", got)
	}
}

func TestWebSocketDialerUsesConfiguredProxy(t *testing.T) {
	dialer, err := websocketDialerForConfig(commonConfig{
		proxy: tts.ProxyConfig{AllProxy: "http://127.0.0.1:7890"},
	})
	if err != nil {
		t.Fatalf("websocketDialerForConfig() error = %v", err)
	}
	if dialer.Proxy == nil {
		t.Fatal("expected websocket dialer proxy to be configured")
	}

	proxyURL, err := dialer.Proxy(&http.Request{URL: mustURL(t, "https://api.minimaxi.com/ws/v1/t2a_v2")})
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("proxy URL = %v, want http://127.0.0.1:7890", proxyURL)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return u
}

type noopSink struct{ format tts.AudioFormat }

func (s noopSink) Format() tts.AudioFormat { return s.format }
func (s noopSink) WritePCM([]byte) error   { return nil }
func (s noopSink) Drain(context.Context) error {
	return nil
}
func (s noopSink) Stop() error { return nil }
