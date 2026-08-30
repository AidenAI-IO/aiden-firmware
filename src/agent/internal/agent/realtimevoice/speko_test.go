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

func TestSpekoProviderMintsAndUsesProviderDirectGeminiCredential(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	setupSeen := make(chan map[string]any, 1)
	audioSeen := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sessions":
			var request spekoSessionCreate
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if r.Header.Get("Authorization") != "Bearer speko-key" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
				t.Error("missing Idempotency-Key")
			}
			if request.Mode != "s2s" || request.AgentID != "agent-1" || request.S2S.Provider != "google" || request.S2S.Model != "gemini-3.1-flash-live-preview" {
				t.Errorf("mint request = %+v", request)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mode": "s2s", "transport": "provider_direct", "sessionId": "speko-session", "attemptId": "attempt-1",
				"provider": "google", "adapter": "google.live.v1", "providerTransport": "websocket",
				"model":           "gemini-3.1-flash-live-preview",
				"expiresAt":       "2099-01-01T00:00:00Z",
				"endpoint":        "ws://" + r.Host + "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent",
				"credential":      map[string]any{"kind": "bearer", "value": "delegated-gemini", "expiresAt": "2099-01-01T00:00:00Z"},
				"inputSampleRate": 16000, "outputSampleRate": 24000,
			})
		case "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContentConstrained":
			if r.URL.Query().Get("access_token") != "delegated-gemini" || r.URL.Query().Get("key") != "" {
				t.Errorf("gemini credential query = %q", r.URL.Query().Get("key"))
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			if err := conn.WriteJSON(map[string]any{"setupComplete": map[string]any{}}); err != nil {
				return
			}
			for {
				_, body, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var frame map[string]any
				if json.Unmarshal(body, &frame) != nil {
					return
				}
				if _, ok := frame["realtimeInput"]; ok {
					audioSeen <- true
					_ = conn.WriteJSON(map[string]any{"serverContent": map[string]any{
						"modelTurn":    map[string]any{"parts": []map[string]any{{"inlineData": map[string]any{"mimeType": "audio/pcm;rate=24000", "data": base64.StdEncoding.EncodeToString([]byte{4, 5})}}}},
						"turnComplete": true,
					}})
				}
				if content, ok := frame["clientContent"].(map[string]any); ok {
					setupSeen <- content
				}
			}
		}
	}))
	defer server.Close()

	session, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, AgentID: "agent-1", UpstreamProvider: "google"}).Open(context.Background(), SessionConfig{
		APIKey: "speko-key", Model: "gemini-3.1-flash-live-preview", Voice: "Aoede", Instructions: "be concise",
		InputSampleRate: 16000, OutputSampleRate: 24000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Info(); got.ID != "speko-session" || got.InputSampleRate != 16000 || got.OutputSampleRate != 24000 {
		t.Fatalf("session info = %+v", got)
	}
	if err := session.SendAudio(context.Background(), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-audioSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for provider-direct audio")
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-session.Events():
			if event.Kind == EventAudio {
				if len(event.PCM) != 2 {
					t.Fatalf("audio event = %+v", event)
				}
				goto audioReceived
			}
		case <-timer.C:
			t.Fatal("timed out waiting for provider audio")
		}
	}
audioReceived:
	textSession, ok := session.(TextSession)
	if !ok {
		t.Fatal("Speko Gemini session lost TextSession capability")
	}
	if err := textSession.SendText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	select {
	case content := <-setupSeen:
		if content["turnComplete"] != false {
			t.Fatalf("client content = %#v", content)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for text frame")
	}
}

func TestSpekoCredentialExpiryValidation(t *testing.T) {
	if _, err := spekoCredentialExpiry(json.RawMessage(`{"kind":"bearer","value":"token","expiresAt":"2099-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := spekoCredentialExpiry(json.RawMessage(`{"kind":"bearer","value":"token"}`)); err == nil {
		t.Fatal("missing credential expiry should fail")
	}
	if _, err := spekoCredentialExpiry(json.RawMessage(`{"kind":"bearer","value":"token","expiresAt":"2000-01-01T00:00:00Z"}`)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired credential should fail: %v", err)
	}
}

func TestSpekoCredentialTokenShapes(t *testing.T) {
	cases := []string{
		`"plain-token"`,
		`{"token":"token-1"}`,
		`{"value":"token-2"}`,
		`{"providerCredential":{"access_token":"token-3"}}`,
	}
	for _, raw := range cases {
		got, err := spekoCredentialToken(json.RawMessage(raw))
		if err != nil || got == "" || !strings.HasPrefix(got, "token") && got != "plain-token" {
			t.Fatalf("raw=%s token=%q err=%v", raw, got, err)
		}
	}
	if _, err := spekoCredentialToken(json.RawMessage(`{"expiresAt":"later"}`)); err == nil {
		t.Fatal("missing credential token should fail")
	}
}

func TestSpekoProviderRejectsRelayResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mode": "s2s", "transport": "provider_direct", "sessionId": "speko-session", "provider": "google", "adapter": "google.live.v1", "providerTransport": "websocket",
			"model": "gemini-3.1-flash-live-preview", "endpoint": "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent",
			"inputSampleRate": 16000, "outputSampleRate": 24000, "credential": nil,
		})
	}))
	defer server.Close()
	_, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL, UpstreamProvider: "google"}).Open(context.Background(), SessionConfig{APIKey: "speko-key", Model: "gemini-3.1-flash-live-preview"})
	if err == nil || !strings.Contains(err.Error(), "missing credential") {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestValidateSpekoMintResponseAllowsAutomaticProvider(t *testing.T) {
	minted := spekoMintResponse{Mode: "s2s", Transport: "provider_direct", SessionID: "speko-session", Model: "gemini-3.1-flash-live-preview", Provider: "google", Adapter: "google.live.v1", ProviderTransport: "websocket", Endpoint: "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent", Credential: json.RawMessage(`{"kind":"bearer","value":"token","expiresAt":"2099-01-01T00:00:00Z"}`), InputSampleRate: 16000, OutputSampleRate: 24000, ExpiresAt: "2099-01-01T00:00:00Z"}
	if err := validateSpekoMintResponse(minted, ""); err != nil {
		t.Fatalf("automatic provider response rejected: %v", err)
	}
}

func TestSpekoProviderOmitsAutomaticRoutingHints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		s2s, ok := body["s2s"].(map[string]any)
		if !ok {
			t.Error("missing s2s request")
			return
		}
		if _, ok := s2s["provider"]; ok {
			t.Error("automatic routing request should omit s2s.provider")
		}
		if _, ok := s2s["model"]; ok {
			t.Error("automatic routing request should omit s2s.model")
		}
		http.Error(w, "routing probe", http.StatusBadRequest)
	}))
	defer server.Close()
	_, err := (SpekoProvider{HTTPClient: server.Client(), BaseURL: server.URL}).Open(context.Background(), SessionConfig{APIKey: "speko-key"})
	if err == nil || !strings.Contains(err.Error(), "routing probe") {
		t.Fatalf("Open() error = %v", err)
	}
}

func TestNormalizeSpekoProvider(t *testing.T) {
	if got, err := normalizeSpekoProvider("gemini"); err != nil || got != "google" {
		t.Fatalf("gemini = %q, %v", got, err)
	}
	if got, err := normalizeSpekoProvider(""); err != nil || got != "" {
		t.Fatalf("empty upstream provider = %q, %v; want empty and nil", got, err)
	}
}
