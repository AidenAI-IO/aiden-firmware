package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	LiveActivityStatusRunning   = "running"
	LiveActivityStatusCompleted = "completed"
	LiveActivityStatusFailed    = "failed"
	LiveActivityStatusCanceled  = "canceled"

	liveActivityFinalStateRetention = 5 * time.Minute
)

type LiveActivityState struct {
	RequestID    string     `json:"request_id"`
	Status       string     `json:"status"`
	TaskTitle    string     `json:"task_title"`
	CurrentStep  string     `json:"current_step"`
	CurrentApp   string     `json:"current_app,omitempty"`
	LastToolName string     `json:"last_tool_name,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Progress     float64    `json:"progress,omitempty"`
	CanStop      bool       `json:"can_stop"`
	StartedAt    time.Time  `json:"started_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

type LiveActivityContentState struct {
	RequestID    string  `json:"request_id"`
	Status       string  `json:"status"`
	TaskTitle    string  `json:"task_title"`
	CurrentStep  string  `json:"current_step"`
	CurrentApp   string  `json:"current_app,omitempty"`
	LastToolName string  `json:"last_tool_name,omitempty"`
	LastError    string  `json:"last_error,omitempty"`
	Progress     float64 `json:"progress,omitempty"`
	CanStop      bool    `json:"can_stop"`
	UpdatedAt    string  `json:"updated_at"`
}

type LiveActivityRegistrationRequest struct {
	RequestID  string `json:"request_id"`
	ActivityID string `json:"activity_id,omitempty"`
	PushToken  string `json:"push_token"`
	Platform   string `json:"platform,omitempty"`
}

type LiveActivityRegistrationResponse struct {
	OK      bool               `json:"ok"`
	State   *LiveActivityState `json:"state,omitempty"`
	APNs    string             `json:"apns"`
	Message string             `json:"message,omitempty"`
}

type liveActivityRegistration struct {
	RequestID    string
	ActivityID   string
	PushToken    string
	Platform     string
	RegisteredAt time.Time
}

type LiveActivityManager struct {
	mu            sync.Mutex
	states        map[string]LiveActivityState
	registrations map[string]liveActivityRegistration
	apns          *APNsClient
	logger        *Logger
}

func NewLiveActivityManager(cfg LiveActivityConfig, logger *Logger) *LiveActivityManager {
	if !cfg.EnabledOrDefault() {
		return nil
	}
	manager := &LiveActivityManager{
		states:        make(map[string]LiveActivityState),
		registrations: make(map[string]liveActivityRegistration),
		logger:        logger,
	}
	if cfg.APNsConfigured() {
		client, err := NewAPNsClient(cfg)
		if err != nil {
			if logger != nil {
				logger.Error("live activity: APNs disabled: %v", err)
			}
		} else {
			manager.apns = client
			if logger != nil {
				logger.Info("live activity: APNs enabled environment=%s topic=%s", cfg.EnvironmentOrDefault(), cfg.APNsTopic())
			}
		}
	}
	return manager
}

func (m *LiveActivityManager) APNsStatus() string {
	if m == nil || m.apns == nil {
		return "not_configured"
	}
	return "configured"
}

func (m *LiveActivityManager) StartTask(requestID, title string) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	now := time.Now()
	state := LiveActivityState{
		RequestID:   strings.TrimSpace(requestID),
		Status:      LiveActivityStatusRunning,
		TaskTitle:   truncateLiveActivityText(firstNonEmptyString([]string{title, "Aiden task"}), 80),
		CurrentStep: "Starting",
		Progress:    0.05,
		CanStop:     true,
		StartedAt:   now,
		UpdatedAt:   now,
	}
	m.mu.Lock()
	m.states[state.RequestID] = state
	m.mu.Unlock()
	m.publish(state.RequestID, false)
	return &state
}

func (m *LiveActivityManager) UpdateFromRunEvent(requestID string, event RunEvent) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	m.mu.Lock()
	state, ok := m.states[requestID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	switch event.Type {
	case "role_output":
		if step := truncateLiveActivityText(event.Content, 120); step != "" {
			state.CurrentStep = step
		}
	case "tool_call":
		state.LastToolName = event.ToolName
		state.CurrentStep = truncateLiveActivityText(firstNonEmptyString([]string{
			event.Description,
			event.Content,
			formatToolStep("Using", event.ToolName),
		}), 120)
		state.Progress = bumpLiveActivityProgress(state.Progress)
	case "tool_result":
		state.LastToolName = event.ToolName
		if event.IsError {
			state.LastError = truncateLiveActivityText(event.Content, 160)
			state.CurrentStep = truncateLiveActivityText(formatToolStep("Tool failed", event.ToolName), 120)
		} else {
			state.CurrentStep = truncateLiveActivityText(formatToolStep("Finished", event.ToolName), 120)
			state.Progress = bumpLiveActivityProgress(state.Progress)
		}
	}
	state.UpdatedAt = time.Now()
	m.states[requestID] = state
	m.mu.Unlock()
	m.publish(requestID, false)
	return &state
}

func (m *LiveActivityManager) CompleteTask(requestID, output string) *LiveActivityState {
	return m.finishTask(requestID, LiveActivityStatusCompleted, firstNonEmptyString([]string{
		truncateLiveActivityText(output, 120),
		"Completed",
	}), "")
}

func (m *LiveActivityManager) FailTask(requestID, message string) *LiveActivityState {
	return m.finishTask(requestID, LiveActivityStatusFailed, "Failed", truncateLiveActivityText(message, 160))
}

func (m *LiveActivityManager) CancelTask(requestID string) *LiveActivityState {
	return m.finishTask(requestID, LiveActivityStatusCanceled, "Canceled", "")
}

func (m *LiveActivityManager) finishTask(requestID, status, step, errText string) *LiveActivityState {
	if m == nil || strings.TrimSpace(requestID) == "" {
		return nil
	}
	now := time.Now()
	m.mu.Lock()
	state, ok := m.states[requestID]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	state.Status = status
	state.CurrentStep = truncateLiveActivityText(step, 120)
	state.LastError = errText
	state.Progress = 1
	state.CanStop = false
	state.UpdatedAt = now
	state.EndedAt = &now
	m.states[requestID] = state
	m.mu.Unlock()
	m.publish(requestID, true)
	m.scheduleCleanup(requestID, now, liveActivityFinalStateRetention)
	return &state
}

func (m *LiveActivityManager) Register(req LiveActivityRegistrationRequest) (*LiveActivityState, string, error) {
	if m == nil {
		return nil, "disabled", errors.New("live activity is disabled")
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.PushToken = strings.TrimSpace(req.PushToken)
	if req.RequestID == "" {
		return nil, "invalid", errors.New("request_id is required")
	}
	if req.PushToken == "" {
		return nil, "invalid", errors.New("push_token is required")
	}
	m.mu.Lock()
	m.registrations[req.RequestID] = liveActivityRegistration{
		RequestID:    req.RequestID,
		ActivityID:   strings.TrimSpace(req.ActivityID),
		PushToken:    req.PushToken,
		Platform:     strings.TrimSpace(req.Platform),
		RegisteredAt: time.Now(),
	}
	state, ok := m.states[req.RequestID]
	m.mu.Unlock()
	if ok {
		m.publish(req.RequestID, isFinalLiveActivityStatus(state.Status))
		return &state, m.APNsStatus(), nil
	}
	return nil, m.APNsStatus(), nil
}

func (m *LiveActivityManager) Unregister(requestID string) bool {
	if m == nil {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	m.mu.Lock()
	_, ok := m.registrations[requestID]
	delete(m.registrations, requestID)
	m.mu.Unlock()
	return ok
}

func (m *LiveActivityManager) Snapshot(requestID string) *LiveActivityState {
	if m == nil {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	m.mu.Lock()
	state, ok := m.states[requestID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return &state
}

func (m *LiveActivityManager) publish(requestID string, final bool) {
	if m == nil || m.apns == nil {
		return
	}
	m.mu.Lock()
	state, stateOK := m.states[requestID]
	registration, regOK := m.registrations[requestID]
	apns := m.apns
	m.mu.Unlock()
	if !stateOK || !regOK {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), apns.timeout)
		defer cancel()
		if err := apns.Push(ctx, registration.PushToken, state, final); err != nil && m.logger != nil {
			m.logger.Error("live activity: APNs push failed request_id=%s: %v", requestID, err)
		}
	}()
}

func (m *LiveActivityManager) scheduleCleanup(requestID string, endedAt time.Time, after time.Duration) {
	if m == nil || after <= 0 {
		return
	}
	time.AfterFunc(after, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		state, ok := m.states[requestID]
		if !ok || state.EndedAt == nil || !state.EndedAt.Equal(endedAt) {
			return
		}
		delete(m.states, requestID)
		delete(m.registrations, requestID)
	})
}

func (s LiveActivityState) ContentState() LiveActivityContentState {
	return LiveActivityContentState{
		RequestID:    s.RequestID,
		Status:       s.Status,
		TaskTitle:    s.TaskTitle,
		CurrentStep:  s.CurrentStep,
		CurrentApp:   s.CurrentApp,
		LastToolName: s.LastToolName,
		LastError:    s.LastError,
		Progress:     s.Progress,
		CanStop:      s.CanStop,
		UpdatedAt:    s.UpdatedAt.Format(time.RFC3339),
	}
}

func bumpLiveActivityProgress(current float64) float64 {
	if current <= 0 {
		return 0.1
	}
	next := current + 0.08
	if next > 0.92 {
		return 0.92
	}
	return next
}

func formatToolStep(prefix, tool string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return prefix
	}
	return prefix + " " + tool
}

func truncateLiveActivityText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len([]rune(s)) <= limit {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:limit-1])) + "..."
}

func isFinalLiveActivityStatus(status string) bool {
	switch status {
	case LiveActivityStatusCompleted, LiveActivityStatusFailed, LiveActivityStatusCanceled:
		return true
	default:
		return false
	}
}

type APNsClient struct {
	httpClient *http.Client
	endpoint   string
	topic      string
	teamID     string
	keyID      string
	privateKey *ecdsa.PrivateKey
	timeout    time.Duration

	mu          sync.Mutex
	cachedJWT   string
	cachedJWTAt time.Time
}

func NewAPNsClient(cfg LiveActivityConfig) (*APNsClient, error) {
	key, err := loadAPNsPrivateKey(cfg)
	if err != nil {
		return nil, err
	}
	topic := cfg.APNsTopic()
	if topic == "" {
		return nil, errors.New("missing APNs topic")
	}
	endpoint := "https://api.sandbox.push.apple.com"
	if cfg.EnvironmentOrDefault() == "production" {
		endpoint = "https://api.push.apple.com"
	}
	return &APNsClient{
		httpClient: &http.Client{Timeout: cfg.TimeoutOrDefault()},
		endpoint:   endpoint,
		topic:      topic,
		teamID:     strings.TrimSpace(cfg.TeamID),
		keyID:      strings.TrimSpace(cfg.KeyID),
		privateKey: key,
		timeout:    cfg.TimeoutOrDefault(),
	}, nil
}

func (c *APNsClient) Push(ctx context.Context, pushToken string, state LiveActivityState, final bool) error {
	if c == nil {
		return nil
	}
	pushToken = strings.TrimSpace(pushToken)
	if pushToken == "" {
		return errors.New("missing live activity push token")
	}
	event := "update"
	if final {
		event = "end"
	}
	payload := map[string]interface{}{
		"aps": map[string]interface{}{
			"timestamp":     time.Now().Unix(),
			"event":         event,
			"content-state": state.ContentState(),
		},
	}
	if final {
		payload["aps"].(map[string]interface{})["dismissal-date"] = time.Now().Add(30 * time.Second).Unix()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode APNs payload: %w", err)
	}
	jwt, err := c.providerJWT()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/3/device/"+pushToken, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-push-type", "liveactivity")
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("APNs status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

func (c *APNsClient) providerJWT() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedJWT != "" && time.Since(c.cachedJWTAt) < 50*time.Minute {
		return c.cachedJWT, nil
	}
	header := map[string]string{"alg": "ES256", "kid": c.keyID}
	claims := map[string]interface{}{"iss": c.teamID, "iat": time.Now().Unix()}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims
	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, c.privateKey, sum[:])
	if err != nil {
		return "", err
	}
	signature := append(fixedWidthBigInt(r, 32), fixedWidthBigInt(s, 32)...)
	c.cachedJWT = signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
	c.cachedJWTAt = time.Now()
	return c.cachedJWT, nil
}

func loadAPNsPrivateKey(cfg LiveActivityConfig) (*ecdsa.PrivateKey, error) {
	raw := strings.TrimSpace(cfg.PrivateKeyPEM)
	if raw == "" {
		data, err := os.ReadFile(strings.TrimSpace(cfg.PrivateKeyPath))
		if err != nil {
			return nil, fmt.Errorf("read APNs private key: %w", err)
		}
		raw = string(data)
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("APNs private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if ecKey, ecErr := x509.ParseECPrivateKey(block.Bytes); ecErr == nil {
			return ecKey, nil
		}
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("APNs private key must be an ECDSA key")
	}
	return key, nil
}

func fixedWidthBigInt(v *big.Int, width int) []byte {
	raw := v.Bytes()
	if len(raw) >= width {
		return raw[len(raw)-width:]
	}
	out := make([]byte, width)
	copy(out[width-len(raw):], raw)
	return out
}
