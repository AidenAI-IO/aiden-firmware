package minimax

import (
	"net/http"
	"net/url"
	"testing"

	"aiden-agent/internal/agent/tts"
)

func TestNewHTTPUsesConfiguredHTTPSProxy(t *testing.T) {
	provider, err := NewHTTP(tts.ProviderConfig{
		APIKey: "test-key",
		Proxy:  tts.ProxyConfig{HTTPSProxy: "http://127.0.0.1:7890"},
	})
	if err != nil {
		t.Fatalf("NewHTTP() error = %v", err)
	}
	adapter := provider.(*HTTPAdapter)
	transport, ok := adapter.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		t.Fatalf("expected HTTP adapter transport with proxy, got %#v", adapter.httpClient.Transport)
	}

	proxyURL, err := transport.Proxy(&http.Request{URL: mustURL(t, "https://api.minimaxi.com/v1/t2a_v2")})
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("proxy URL = %v, want http://127.0.0.1:7890", proxyURL)
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
