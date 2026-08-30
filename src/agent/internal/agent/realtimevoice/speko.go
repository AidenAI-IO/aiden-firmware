package realtimevoice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const DefaultSpekoBaseURL = "https://api.speko.dev"

// SpekoProvider mints a provider-direct S2S entitlement. Speko is deliberately
// absent from the media path: after minting, the selected native adapter opens
// the provider connection with the short-lived delegated credential.
type SpekoProvider struct {
	HTTPClient       *http.Client
	BaseURL          string
	AgentID          string
	UpstreamProvider string
	Dialer           *websocket.Dialer
	EventBuffer      int
	IdempotencyKey   string
}

type spekoSessionCreate struct {
	Mode    string         `json:"mode"`
	AgentID string         `json:"agentId,omitempty"`
	S2S     spekoS2SConfig `json:"s2s"`
}

type spekoS2SConfig struct {
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Voice            string `json:"voice,omitempty"`
	SystemPrompt     string `json:"systemPrompt,omitempty"`
	InputSampleRate  int    `json:"inputSampleRate,omitempty"`
	OutputSampleRate int    `json:"outputSampleRate,omitempty"`
	Tools            []Tool `json:"tools,omitempty"`
}

// spekoMintResponse intentionally keeps Credential opaque. Speko has used
// different credential object shapes as provider authentication evolved; the
// token extractor below accepts the documented token/value/apiKey variants
// without exposing those wire details to the shared session interface.
type spekoMintResponse struct {
	Mode              string          `json:"mode"`
	Transport         string          `json:"transport"`
	SessionID         string          `json:"sessionId"`
	SessionIDSnake    string          `json:"session_id"`
	AttemptID         string          `json:"attemptId"`
	Model             string          `json:"model"`
	Provider          string          `json:"provider"`
	Adapter           string          `json:"adapter"`
	ProviderTransport string          `json:"providerTransport"`
	Endpoint          string          `json:"endpoint"`
	ProviderEndpoint  string          `json:"providerEndpoint"`
	Credential        json.RawMessage `json:"credential"`
	InputSampleRate   int             `json:"inputSampleRate"`
	OutputSampleRate  int             `json:"outputSampleRate"`
	ExpiresAt         string          `json:"expiresAt"`
}

func (p SpekoProvider) Open(ctx context.Context, cfg SessionConfig) (Session, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("speko: APIKey is required")
	}
	upstream, err := normalizeSpekoProvider(p.UpstreamProvider)
	if err != nil {
		return nil, err
	}
	if (upstream == "") != (strings.TrimSpace(cfg.Model) == "") {
		return nil, errors.New("speko: upstream provider and model must either both be set or both be omitted")
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		base = DefaultSpekoBaseURL
	}
	hc := p.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	body, err := json.Marshal(spekoSessionCreate{
		Mode: "s2s", AgentID: p.AgentID,
		S2S: spekoS2SConfig{
			Provider: upstream, Model: cfg.Model, Voice: cfg.Voice,
			SystemPrompt:     cfg.Instructions,
			InputSampleRate:  requestedRate(cfg.InputSampleRate, cfg.InputAudioFormat),
			OutputSampleRate: requestedRate(cfg.OutputSampleRate, cfg.OutputAudioFormat),
			Tools:            cfg.Tools,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("speko session mint request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/sessions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("speko session mint request: %w", err)
	}
	req.Header.Set("Authorization", bearer(cfg.APIKey))
	req.Header.Set("Content-Type", "application/json")
	idempotencyKey := strings.TrimSpace(p.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey, err = spekoIdempotencyKey()
		if err != nil {
			return nil, fmt.Errorf("speko session mint: generate idempotency key: %w", err)
		}
	}
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("speko session mint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("speko session mint: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var minted spekoMintResponse
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		return nil, fmt.Errorf("speko session mint response: %w", err)
	}
	if err := validateSpekoMintResponse(minted, upstream); err != nil {
		return nil, err
	}
	token, err := spekoCredentialToken(minted.Credential)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = strings.TrimSpace(minted.Model)
	}
	selectedProvider, err := normalizeSpekoProvider(minted.Provider)
	if err != nil {
		return nil, fmt.Errorf("speko: minted provider: %w", err)
	}
	if upstream != "" && selectedProvider != upstream {
		return nil, fmt.Errorf("speko: minted provider %q does not match requested upstream %q", selectedProvider, upstream)
	}
	endpoint := strings.TrimSpace(minted.Endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(minted.ProviderEndpoint)
	}
	if endpoint == "" {
		return nil, errors.New("speko session mint response missing provider endpoint")
	}
	sessionID := minted.SessionID
	if sessionID == "" {
		sessionID = minted.SessionIDSnake
	}
	inputRate, outputRate := spekoRates(selectedProvider, minted.InputSampleRate, minted.OutputSampleRate, cfg.InputSampleRate, cfg.OutputSampleRate)
	if !supportedSpekoSampleRate(inputRate) {
		return nil, fmt.Errorf("speko session mint response has unsupported input sample rate %d", inputRate)
	}
	if !supportedSpekoSampleRate(outputRate) {
		return nil, fmt.Errorf("speko session mint response has unsupported output sample rate %d", outputRate)
	}
	cfg.APIKey = token
	cfg.SessionID = sessionID
	cfg.InputSampleRate = inputRate
	cfg.OutputSampleRate = outputRate
	cfg.Model = strings.TrimSpace(cfg.Model)
	switch selectedProvider {
	case "google":
		return (GeminiProvider{Endpoint: endpoint, Dialer: p.Dialer, EventBuffer: p.EventBuffer, DelegatedCredential: true}).Open(ctx, cfg)
	case "openai":
		return (OpenAIProvider{Endpoint: endpoint, Dialer: p.Dialer, EventBuffer: p.EventBuffer}).Open(ctx, cfg)
	case "xai":
		return (XAIProvider{Endpoint: endpoint, Dialer: p.Dialer, EventBuffer: p.EventBuffer, AuthSubprotocol: true, AuthSubprotocolPrefix: "xai-client-secret."}).Open(ctx, cfg)
	default:
		return nil, fmt.Errorf("speko: unsupported upstream provider %q", upstream)
	}
}

func validateSpekoMintResponse(minted spekoMintResponse, upstream string) error {
	if minted.Mode != "s2s" {
		return fmt.Errorf("speko: session mode %q is not s2s", minted.Mode)
	}
	if minted.Transport != "provider_direct" {
		return fmt.Errorf("speko: session transport %q is not provider_direct", minted.Transport)
	}
	if strings.TrimSpace(minted.SessionID) == "" && strings.TrimSpace(minted.SessionIDSnake) == "" {
		return errors.New("speko session mint response missing sessionId")
	}
	responseProvider, err := normalizeSpekoProvider(minted.Provider)
	if err != nil {
		return fmt.Errorf("speko: minted provider: %w", err)
	}
	if responseProvider == "" {
		return errors.New("speko: session mint response missing provider")
	}
	if upstream != "" && responseProvider != upstream {
		return fmt.Errorf("speko: minted provider %q does not match requested upstream %q", responseProvider, upstream)
	}
	expectedAdapter := map[string]string{"google": "google.live.v1", "openai": "openai.realtime.v1", "xai": "xai.realtime.v1"}[responseProvider]
	if strings.TrimSpace(minted.Adapter) != expectedAdapter {
		return fmt.Errorf("speko: adapter %q does not match %s", minted.Adapter, expectedAdapter)
	}
	transport := strings.ToLower(strings.TrimSpace(minted.ProviderTransport))
	if (responseProvider == "openai" && transport != "webrtc") || (responseProvider != "openai" && transport != "websocket") {
		return fmt.Errorf("speko: %s provider-direct transport %q is invalid", responseProvider, transport)
	}
	endpoint := strings.TrimSpace(minted.Endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(minted.ProviderEndpoint)
	}
	if err := validateSpekoEndpoint(endpoint, responseProvider, transport); err != nil {
		return err
	}
	if _, err := spekoCredentialExpiry(minted.Credential); err != nil {
		return err
	}
	if strings.TrimSpace(minted.ExpiresAt) == "" {
		return errors.New("speko session mint response missing expiresAt")
	}
	if err := validateSpekoExpiry(minted.ExpiresAt, "expiresAt"); err != nil {
		return err
	}
	if minted.InputSampleRate <= 0 || !supportedSpekoSampleRate(minted.InputSampleRate) {
		return fmt.Errorf("speko session mint response has unsupported input sample rate %d", minted.InputSampleRate)
	}
	if minted.OutputSampleRate <= 0 || !supportedSpekoSampleRate(minted.OutputSampleRate) {
		return fmt.Errorf("speko session mint response has unsupported output sample rate %d", minted.OutputSampleRate)
	}
	if strings.TrimSpace(minted.Model) == "" {
		return errors.New("speko session mint response missing model")
	}
	if responseProvider == "openai" {
		return errors.New("speko: OpenAI provider-direct transport is WebRTC; Go adapter does not implement WebRTC")
	}
	return nil
}

func validateSpekoEndpoint(raw, provider, transport string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("speko session mint response missing provider endpoint")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("speko: invalid provider endpoint %q", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	if transport == "websocket" {
		if scheme != "ws" && scheme != "wss" {
			return fmt.Errorf("speko: %s endpoint must use ws:// or wss://", provider)
		}
	} else if scheme != "http" && scheme != "https" {
		return fmt.Errorf("speko: %s WebRTC endpoint must use http:// or https://", provider)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if isSpekoLoopbackHost(host) {
		return nil
	}
	allowed := map[string][]string{
		"google": {"googleapis.com"},
		"xai":    {"x.ai"},
		"openai": {"openai.com"},
	}[provider]
	for _, suffix := range allowed {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return nil
		}
	}
	return fmt.Errorf("speko: provider endpoint host %q is not allowlisted for %s", u.Hostname(), provider)
}

func isSpekoLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func spekoCredentialExpiry(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", errors.New("speko session mint response missing credential")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("speko session mint response credential: %w", err)
	}
	if object, ok := value.(map[string]any); ok {
		if kind, ok := object["kind"].(string); ok && strings.TrimSpace(kind) != "" && strings.ToLower(strings.TrimSpace(kind)) != "bearer" {
			return "", fmt.Errorf("speko session mint response credential kind %q is not bearer", kind)
		}
		for _, key := range []string{"expiresAt", "expires_at"} {
			if expires, ok := object[key].(string); ok && strings.TrimSpace(expires) != "" {
				if err := validateSpekoExpiry(expires, "credential expiresAt"); err != nil {
					return "", err
				}
				return expires, nil
			}
		}
	}
	return "", errors.New("speko session mint response credential missing expiresAt")
}

func validateSpekoExpiry(raw, field string) error {
	expires, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("speko session mint response %s: %w", field, err)
	}
	if !expires.After(time.Now()) {
		return fmt.Errorf("speko session mint response %s is expired", field)
	}
	return nil
}

func normalizeSpekoProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return "", nil
	case "google", "gemini":
		return "google", nil
	case "openai":
		return "openai", nil
	case "xai":
		return "xai", nil
	default:
		return "", fmt.Errorf("speko: unsupported upstream provider %q (want google, openai, or xai)", provider)
	}
}

func spekoRates(provider string, input, output, requestedInput, requestedOutput int) (int, int) {
	if input <= 0 {
		input = requestedInput
	}
	if output <= 0 {
		output = requestedOutput
	}
	if input <= 0 {
		if provider == "openai" || provider == "xai" {
			input = 24000
		} else {
			input = 16000
		}
	}
	if output <= 0 {
		output = 24000
	}
	return input, output
}

func supportedSpekoSampleRate(rate int) bool { return rate == 16000 || rate == 24000 }

func spekoIdempotencyKey() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func requestedRate(rate int, format string) int {
	if rate == 16000 || rate == 24000 {
		return rate
	}
	if strings.Contains(strings.ToLower(format), "16") {
		return 16000
	}
	if strings.Contains(strings.ToLower(format), "24") {
		return 24000
	}
	return 0
}

func spekoCredentialToken(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("speko session mint response missing credential")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("speko session mint response credential: %w", err)
	}
	if token := findSpekoToken(value); token != "" {
		return token, nil
	}
	return "", errors.New("speko session mint response missing credential token")
}

func findSpekoToken(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"token", "accessToken", "access_token", "apiKey", "api_key", "key", "value"} {
		if token := findSpekoToken(object[key]); token != "" {
			return token
		}
	}
	for _, key := range []string{"credential", "providerCredential", "provider_credential", "auth"} {
		if token := findSpekoToken(object[key]); token != "" {
			return token
		}
	}
	return ""
}

func bearer(key string) string {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(strings.ToLower(key), "bearer ") {
		return key
	}
	return "Bearer " + key
}

func buffer(n int) int {
	if n <= 0 {
		return 64
	}
	return n
}

func normalClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, websocket.ErrCloseSent)
}
