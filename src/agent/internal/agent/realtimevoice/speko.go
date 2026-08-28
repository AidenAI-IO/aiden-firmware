package realtimevoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const DefaultSpekoBaseURL = "https://api.speko.dev"

type SpekoProvider struct {
	HTTPClient       *http.Client
	BaseURL          string
	AgentID          string
	UpstreamProvider string
	Dialer           *websocket.Dialer
	EventBuffer      int
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
type spekoCredentials struct {
	SessionID        string `json:"sessionId"`
	WSURL            string `json:"wsUrl"`
	WSToken          string `json:"wsToken"`
	InputSampleRate  int    `json:"inputSampleRate"`
	OutputSampleRate int    `json:"outputSampleRate"`
	ExpiresAt        string `json:"expiresAt"`
}

func (p SpekoProvider) Open(ctx context.Context, cfg SessionConfig) (Session, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("speko: APIKey is required")
	}
	if strings.TrimSpace(p.UpstreamProvider) == "" {
		return nil, errors.New("speko: upstream provider is required")
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = DefaultSpekoBaseURL
	}
	hc := p.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	body, err := json.Marshal(spekoSessionCreate{Mode: "s2s", AgentID: p.AgentID, S2S: spekoS2SConfig{Provider: p.UpstreamProvider, Model: cfg.Model, Voice: cfg.Voice, SystemPrompt: cfg.Instructions, InputSampleRate: requestedRate(cfg.InputSampleRate, cfg.InputAudioFormat), OutputSampleRate: requestedRate(cfg.OutputSampleRate, cfg.OutputAudioFormat), Tools: cfg.Tools}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/sessions", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", bearer(cfg.APIKey))
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("speko session mint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("speko session mint: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var cred spekoCredentials
	if err := json.NewDecoder(resp.Body).Decode(&cred); err != nil {
		return nil, fmt.Errorf("speko session mint response: %w", err)
	}
	if cred.WSURL == "" || cred.WSToken == "" {
		return nil, errors.New("speko session mint response missing wsUrl/wsToken")
	}
	d := p.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	dialer := *d
	dialer.Subprotocols = []string{cred.WSToken}
	conn, resp, err := dialer.DialContext(ctx, cred.WSURL, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("speko websocket connect: %w", err)
	}
	if cred.InputSampleRate == 0 {
		cred.InputSampleRate = 16000
	}
	if cred.OutputSampleRate == 0 {
		cred.OutputSampleRate = 24000
	}
	if !supportedSpekoSampleRate(cred.InputSampleRate) {
		_ = conn.Close()
		return nil, fmt.Errorf("speko session mint response has unsupported input sample rate %d", cred.InputSampleRate)
	}
	if !supportedSpekoSampleRate(cred.OutputSampleRate) {
		_ = conn.Close()
		return nil, fmt.Errorf("speko session mint response has unsupported output sample rate %d", cred.OutputSampleRate)
	}
	s := &spekoSession{
		conn: conn,
		info: SessionInfo{
			ID:               cred.SessionID,
			InputSampleRate:  cred.InputSampleRate,
			OutputSampleRate: cred.OutputSampleRate,
			Capabilities:     Capabilities{ManualCommit: true, Interrupt: true, ToolCalls: true},
		},
		inputFrameBytes: cred.InputSampleRate * 2 / 50,
		events:          make(chan Event, buffer(p.EventBuffer)),
		errs:            make(chan error, 1),
		done:            make(chan struct{}),
		writeGate:       make(chan struct{}, 1),
	}
	s.writeGate <- struct{}{}
	go s.readLoop()
	return s, nil
}

func supportedSpekoSampleRate(rate int) bool {
	return rate == 16000 || rate == 24000
}

func requestedRate(rate int, format string) int {
	if rate == 16000 || rate == 24000 {
		return rate
	}
	if strings.Contains(format, "16") {
		return 16000
	}
	if strings.Contains(format, "24") {
		return 24000
	}
	return 0
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

type spekoSession struct {
	conn            *websocket.Conn
	info            SessionInfo
	inputFrameBytes int
	events          chan Event
	errs            chan error
	done            chan struct{}
	once            sync.Once
	writeGate       chan struct{}
	audioMu         sync.Mutex
	audioPending    []byte
}

func (s *spekoSession) Info() SessionInfo     { return s.info }
func (s *spekoSession) Events() <-chan Event  { return s.events }
func (s *spekoSession) Errors() <-chan error  { return s.errs }
func (s *spekoSession) Done() <-chan struct{} { return s.done }
func (s *spekoSession) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	s.audioPending = append(s.audioPending, pcm...)
	for len(s.audioPending) >= s.inputFrameBytes {
		if err := s.write(ctx, websocket.BinaryMessage, s.audioPending[:s.inputFrameBytes]); err != nil {
			return err
		}
		s.audioPending = s.audioPending[s.inputFrameBytes:]
	}
	return nil
}
func (s *spekoSession) Commit(ctx context.Context) error {
	s.audioMu.Lock()
	if len(s.audioPending) > 0 {
		frame := make([]byte, s.inputFrameBytes)
		copy(frame, s.audioPending)
		s.audioPending = nil
		if err := s.write(ctx, websocket.BinaryMessage, frame); err != nil {
			s.audioMu.Unlock()
			return err
		}
	}
	s.audioMu.Unlock()
	return s.writeJSON(ctx, map[string]any{"t": "control", "action": "commit"})
}
func (s *spekoSession) Interrupt(ctx context.Context) error {
	return s.writeJSON(ctx, map[string]any{"t": "interrupt"})
}
func (s *spekoSession) SendToolResult(ctx context.Context, id, out string) error {
	return s.writeJSON(ctx, map[string]any{"t": "tool_result", "callId": id, "output": out})
}
func (s *spekoSession) Close() error {
	var err error
	s.once.Do(func() { close(s.done); err = s.conn.Close() })
	return err
}
func (s *spekoSession) writeJSON(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.write(ctx, websocket.TextMessage, b)
}
func (s *spekoSession) write(ctx context.Context, typ int, b []byte) error {
	if ctx == nil {
		return errors.New("speko: nil context")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("speko: session is closed")
	case <-s.writeGate:
	}
	defer func() { s.writeGate <- struct{}{} }()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetWriteDeadline(deadline)
	defer s.conn.SetWriteDeadline(time.Time{})
	if err := s.conn.WriteMessage(typ, b); err != nil {
		return fmt.Errorf("speko websocket write: %w", err)
	}
	return nil
}
func (s *spekoSession) readLoop() {
	defer close(s.events)
	defer close(s.errs)
	defer s.once.Do(func() { close(s.done); _ = s.conn.Close() })
	for {
		typ, b, err := s.conn.ReadMessage()
		if err != nil {
			if !normalClose(err) {
				select {
				case s.errs <- err:
				default:
				}
			}
			return
		}
		if typ == websocket.BinaryMessage {
			select {
			case s.events <- Event{Kind: EventAudio, PCM: append([]byte(nil), b...)}:
			case <-s.done:
				return
			}
			continue
		}
		if typ != websocket.TextMessage {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal(b, &frame); err != nil {
			select {
			case s.errs <- fmt.Errorf("speko: decode text frame: %w", err):
			default:
			}
			return
		}
		ev := spekoEvent(frame)
		if ev.Kind == "" {
			continue
		}
		select {
		case s.events <- ev:
		case <-s.done:
			return
		}
	}
}
func normalClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, websocket.ErrCloseSent)
}
func spekoEvent(f map[string]any) Event {
	str := func(k string) string {
		if v, ok := f[k]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}
	switch str("t") {
	case "ready":
		return Event{Kind: EventReady}
	case "transcript":
		return Event{Kind: map[bool]EventKind{true: EventTranscriptFinal, false: EventTranscriptDelta}[f["final"] == true], Role: str("role"), Text: str("text"), Final: f["final"] == true}
	case "interruption":
		return Event{Kind: EventInterruption, At: str("at")}
	case "tool_call":
		return Event{Kind: EventToolCall, CallID: str("callId"), Name: str("name"), Arguments: str("arguments")}
	case "usage":
		return Event{
			Kind:   EventResponseDone,
			Status: "completed",
			Usage:  Usage{InputTokens: intNum(f["inputAudioTokens"]), OutputTokens: intNum(f["outputAudioTokens"])},
		}
	case "error":
		return Event{Kind: EventError, Error: errors.New(str("message"))}
	default:
		return Event{}
	}
}
func intNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

var _ Provider = SpekoProvider{}
var _ Session = (*spekoSession)(nil)
