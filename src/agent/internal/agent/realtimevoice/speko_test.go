package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
				"reservation":     map[string]any{"leaseExpiresAt": "2099-01-01T00:00:00Z"},
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

func TestSpekoProviderRejectsAutomaticRoutingBeforeMint(t *testing.T) {
	_, err := (SpekoProvider{}).Open(context.Background(), SessionConfig{APIKey: "speko-key"})
	if err == nil || !strings.Contains(err.Error(), "automatic routing is disabled") {
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

func TestSpekoRejectsOpenAIRouteRegardlessOfTransport(t *testing.T) {
	base := spekoMintResponse{
		Mode: "s2s", Transport: "provider_direct", SessionID: "sess_1",
		Model: "gpt-realtime", Provider: "openai", Adapter: "openai.realtime.v1",
		ProviderTransport: "websocket", Endpoint: "wss://api.openai.com/v1/realtime",
		InputSampleRate: 24000, OutputSampleRate: 24000,
		ExpiresAt:  "2099-01-01T00:00:00Z",
		Credential: json.RawMessage(`{"kind":"bearer","value":"ek_test","expiresAt":"2099-01-01T00:00:00Z"}`),
	}
	if err := validateSpekoMintResponse(base, ""); err == nil || !strings.Contains(err.Error(), "unsupported upstream provider") {
		t.Fatalf("automatic OpenAI route must be rejected before adapter dispatch, got %v", err)
	}

	unknown := spekoMintResponse{
		Mode: "s2s", Transport: "provider_direct", SessionID: "sess_1",
		Model: "grok-voice-latest", Provider: "xai", Adapter: "xai.realtime.v1",
		ProviderTransport: "quic", Endpoint: "wss://api.x.ai/v1/realtime",
		InputSampleRate: 24000, OutputSampleRate: 24000,
		ExpiresAt:  "2099-01-01T00:00:00Z",
		Credential: json.RawMessage(`{"kind":"bearer","value":"token","expiresAt":"2099-01-01T00:00:00Z"}`),
	}
	unknown.ProviderTransport = "quic"
	if err := validateSpekoMintResponse(unknown, "xai"); err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("an unknown transport must be refused distinctly, got %v", err)
	}
}

func TestSpekoLeaseRotatesBeforeExpiry(t *testing.T) {
	raw := newManagedBaseSession()
	expiresAt := time.Now().Add(300 * time.Millisecond)
	session := newLeasedSession(raw, expiresAt, 200*time.Millisecond)
	defer session.Close()
	select {
	case event := <-session.Events():
		if event.Kind != EventError || !errors.Is(event.Error, ErrSessionRotated) {
			t.Fatalf("lease event = %+v, want rotation", event)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("lease did not rotate before expiry")
	}
}

func TestSpekoLeaseExpiryUsesEarliestBoundary(t *testing.T) {
	now := time.Now()
	credentialExpiry := now.Add(2 * time.Hour)
	credential, err := json.Marshal(map[string]any{
		"kind": "bearer", "value": "token", "expiresAt": credentialExpiry.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	minted := spekoMintResponse{
		ExpiresAt:  now.Add(3 * time.Hour).Format(time.RFC3339Nano),
		Credential: credential,
	}
	leaseExpiry := now.Add(time.Hour)
	minted.Reservation.LeaseExpiresAt = leaseExpiry.Format(time.RFC3339Nano)
	got, err := spekoLeaseExpiry(minted)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(leaseExpiry) {
		t.Fatalf("lease expiry = %s, want earliest reservation expiry %s", got, leaseExpiry)
	}
}

func TestValidateSpekoEndpointRequiresSecureAllowlistedRoute(t *testing.T) {
	geminiPath := "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContentConstrained"
	if err := validateSpekoEndpoint("wss://generativelanguage.googleapis.com"+geminiPath, "google", false); err != nil {
		t.Fatalf("official Gemini route rejected: %v", err)
	}
	if err := validateSpekoEndpoint("wss://api.x.ai/v1/realtime?model=grok-voice-latest", "xai", false); err != nil {
		t.Fatalf("official xAI route rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"plaintext production": "ws://generativelanguage.googleapis.com" + geminiPath,
		"arbitrary path":       "wss://generativelanguage.googleapis.com/attacker-controlled",
		"loopback production":  "ws://127.0.0.1:8080" + geminiPath,
		"unexpected query":     "wss://generativelanguage.googleapis.com" + geminiPath + "?redirect=evil",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSpekoEndpoint(raw, "google", false); err == nil {
				t.Fatalf("endpoint %q accepted", raw)
			}
		})
	}
	if err := validateSpekoEndpoint("ws://127.0.0.1:8080"+geminiPath, "google", true); err != nil {
		t.Fatalf("explicit loopback test route rejected: %v", err)
	}
}
