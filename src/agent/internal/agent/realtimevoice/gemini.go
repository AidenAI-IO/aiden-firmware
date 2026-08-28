package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultGeminiLiveEndpoint = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	DefaultGeminiLiveModel    = "gemini-3.1-flash-live-preview"
)

// GeminiProvider is the native Google Gemini Live adapter. It currently uses
// the Gemini API-key endpoint; Vertex service-account credentials can be added
// behind the same semantic adapter once a deployment needs them.
type GeminiProvider struct {
	Endpoint    string
	Dialer      *websocket.Dialer
	EventBuffer int
}

func (p GeminiProvider) Open(ctx context.Context, cfg SessionConfig) (Session, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("gemini live: APIKey is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultGeminiLiveModel
	}
	endpoint, err := p.endpoint(model, cfg.APIKey)
	if err != nil {
		return nil, err
	}
	d := p.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	conn, resp, err := d.DialContext(ctx, endpoint, nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("gemini live websocket connect: %w", err)
	}
	s := &geminiSession{
		conn:      conn,
		events:    make(chan Event, buffer(p.EventBuffer)),
		errs:      make(chan error, 1),
		done:      make(chan struct{}),
		writeGate: make(chan struct{}, 1),
		toolNames: make(map[string]string),
		info: SessionInfo{
			InputSampleRate: 16000, OutputSampleRate: 24000,
			Capabilities: Capabilities{TextInput: true, ManualCommit: true, ToolCalls: true},
		},
	}
	s.writeGate <- struct{}{}
	go s.readLoop()
	if err := s.writeJSON(ctx, buildGeminiSetup(cfg, model)); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := waitGeminiReady(ctx, s); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (p GeminiProvider) endpoint(model, apiKey string) (string, error) {
	raw := strings.TrimSpace(p.Endpoint)
	if raw == "" {
		raw = DefaultGeminiLiveEndpoint
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("gemini live: parse endpoint: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("gemini live: endpoint must use ws:// or https://, got %q", u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	}
	q := u.Query()
	q.Set("key", apiKey)
	_ = model // The Live API selects the model in the initial setup message.
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type geminiSetupMessage struct {
	Setup geminiSetup `json:"setup"`
}

type geminiSetup struct {
	Model                    string                     `json:"model"`
	GenerationConfig         geminiGenerationConfig     `json:"generationConfig"`
	SystemInstruction        *geminiSystemInstruction   `json:"systemInstruction,omitempty"`
	Tools                    []geminiTool               `json:"tools,omitempty"`
	InputAudioTranscription  map[string]any             `json:"inputAudioTranscription,omitempty"`
	OutputAudioTranscription map[string]any             `json:"outputAudioTranscription,omitempty"`
	RealtimeInputConfig      *geminiRealtimeInputConfig `json:"realtimeInputConfig,omitempty"`
}

type geminiGenerationConfig struct {
	ResponseModalities []string            `json:"responseModalities"`
	SpeechConfig       *geminiSpeechConfig `json:"speechConfig,omitempty"`
}

type geminiSpeechConfig struct {
	VoiceConfig geminiVoiceConfig `json:"voiceConfig"`
}

type geminiVoiceConfig struct {
	PrebuiltVoiceConfig geminiPrebuiltVoiceConfig `json:"prebuiltVoiceConfig"`
}

type geminiPrebuiltVoiceConfig struct {
	VoiceName string `json:"voiceName"`
}

type geminiSystemInstruction struct {
	Parts []geminiTextPart `json:"parts"`
}

type geminiTextPart struct {
	Text string `json:"text"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiRealtimeInputConfig struct {
	AutomaticActivityDetection *geminiAutomaticActivityDetection `json:"automaticActivityDetection,omitempty"`
}

type geminiAutomaticActivityDetection struct {
	Disabled bool `json:"disabled,omitempty"`
}

func buildGeminiSetup(cfg SessionConfig, model string) geminiSetupMessage {
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	setup := geminiSetup{
		Model:                    model,
		GenerationConfig:         geminiGenerationConfig{ResponseModalities: []string{"AUDIO"}},
		InputAudioTranscription:  map[string]any{},
		OutputAudioTranscription: map[string]any{},
	}
	if strings.TrimSpace(cfg.Voice) != "" {
		setup.GenerationConfig.SpeechConfig = &geminiSpeechConfig{VoiceConfig: geminiVoiceConfig{PrebuiltVoiceConfig: geminiPrebuiltVoiceConfig{VoiceName: cfg.Voice}}}
	}
	if strings.TrimSpace(cfg.Instructions) != "" {
		setup.SystemInstruction = &geminiSystemInstruction{Parts: []geminiTextPart{{Text: cfg.Instructions}}}
	}
	for _, tool := range cfg.Tools {
		setup.Tools = append(setup.Tools, geminiTool{FunctionDeclarations: []geminiFunctionDeclaration{{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}}})
	}
	if cfg.TurnDetection == "disabled" {
		setup.RealtimeInputConfig = &geminiRealtimeInputConfig{AutomaticActivityDetection: &geminiAutomaticActivityDetection{Disabled: true}}
	}
	return geminiSetupMessage{Setup: setup}
}

type geminiSession struct {
	conn           *websocket.Conn
	info           SessionInfo
	events         chan Event
	errs           chan error
	done           chan struct{}
	writeGate      chan struct{}
	closeOnce      sync.Once
	responseMu     sync.Mutex
	responseActive bool
	toolMu         sync.Mutex
	toolNames      map[string]string
}

func (s *geminiSession) Info() SessionInfo     { return s.info }
func (s *geminiSession) Events() <-chan Event  { return s.events }
func (s *geminiSession) Errors() <-chan error  { return s.errs }
func (s *geminiSession) Done() <-chan struct{} { return s.done }
func (s *geminiSession) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	return s.writeJSON(ctx, map[string]any{"realtimeInput": map[string]any{
		"mediaChunks": []map[string]string{{"mimeType": "audio/pcm;rate=16000", "data": base64.StdEncoding.EncodeToString(pcm)}},
	}})
}
func (s *geminiSession) Commit(ctx context.Context) error {
	return s.writeJSON(ctx, map[string]any{"realtimeInput": map[string]any{"audioStreamEnd": true}})
}
func (s *geminiSession) Interrupt(context.Context) error {
	return errors.New("gemini live: client interruption is provider-managed")
}
func (s *geminiSession) SendToolResult(ctx context.Context, id, output string) error {
	response := any(output)
	var decoded any
	if json.Unmarshal([]byte(output), &decoded) == nil {
		response = decoded
	}
	s.toolMu.Lock()
	name := s.toolNames[id]
	s.toolMu.Unlock()
	functionResponse := map[string]any{"id": id, "response": map[string]any{"result": response}}
	if name != "" {
		functionResponse["name"] = name
	}
	return s.writeJSON(ctx, map[string]any{"toolResponse": map[string]any{
		"functionResponses": []map[string]any{functionResponse},
	}})
}
func (s *geminiSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		err = s.conn.Close()
	})
	return err
}
func (s *geminiSession) SendText(ctx context.Context, text string) error {
	return s.writeJSON(ctx, map[string]any{"clientContent": map[string]any{
		"turns":        []map[string]any{{"role": "user", "parts": []map[string]string{{"text": text}}}},
		"turnComplete": false,
	}})
}
func (s *geminiSession) CreateResponse(ctx context.Context) error {
	return s.writeJSON(ctx, map[string]any{"clientContent": map[string]any{"turnComplete": true}})
}
func (s *geminiSession) ReplayContext(context.Context, []ContextItem) error {
	return errors.New("gemini live: context replay is not supported")
}

func (s *geminiSession) writeJSON(ctx context.Context, value any) error {
	if ctx == nil {
		return errors.New("gemini live: nil context")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("gemini live: marshal event: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("gemini live: session is closed")
	case <-s.writeGate:
	}
	defer func() { s.writeGate <- struct{}{} }()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetWriteDeadline(deadline)
	defer s.conn.SetWriteDeadline(time.Time{})
	if err := s.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		return fmt.Errorf("gemini live websocket write: %w", err)
	}
	return nil
}

func (s *geminiSession) readLoop() {
	defer close(s.events)
	defer close(s.errs)
	defer s.closeOnce.Do(func() { close(s.done); _ = s.conn.Close() })
	for {
		_, body, err := s.conn.ReadMessage()
		if err != nil {
			if !normalClose(err) {
				select {
				case s.errs <- err:
				default:
				}
			}
			return
		}
		events := s.translate(body)
		for _, event := range events {
			select {
			case s.events <- event:
			case <-s.done:
				return
			}
		}
	}
}

func waitGeminiReady(ctx context.Context, s *geminiSession) error {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("gemini live: timed out waiting for setupComplete")
		case err, ok := <-s.errs:
			if !ok {
				return errors.New("gemini live: event stream closed")
			}
			if err != nil {
				return err
			}
		case event, ok := <-s.events:
			if !ok {
				return errors.New("gemini live: event stream closed")
			}
			if event.Kind == EventError {
				return event.Error
			}
			if event.Kind == EventReady {
				return nil
			}
		}
	}
}

func (s *geminiSession) translate(body []byte) []Event {
	var envelope struct {
		SetupComplete    json.RawMessage      `json:"setupComplete"`
		ServerContent    *geminiServerContent `json:"serverContent"`
		ToolCall         *geminiToolCall      `json:"toolCall"`
		ToolCancellation json.RawMessage      `json:"toolCallCancellation"`
		UsageMetadata    *geminiUsageMetadata `json:"usageMetadata"`
		GoAway           *struct {
			TimeLeft string `json:"timeLeft"`
		} `json:"goAway"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return []Event{{Kind: EventError, Error: fmt.Errorf("gemini live: decode event: %w", err)}}
	}
	if len(envelope.SetupComplete) > 0 {
		return []Event{{Kind: EventReady}}
	}
	if envelope.Error != nil {
		return []Event{{Kind: EventError, Error: errors.New(envelope.Error.Message)}}
	}
	var events []Event
	if envelope.ServerContent != nil {
		s.responseMu.Lock()
		active := s.responseActive
		if envelope.ServerContent.ModelTurn != nil && !active {
			events = append(events, Event{Kind: EventResponseStarted})
			s.responseActive = true
		}
		s.responseMu.Unlock()
		content := envelope.ServerContent
		if content.InputTranscription != nil && content.InputTranscription.Text != "" {
			kind := EventTranscriptDelta
			if content.InputTranscription.Finished {
				kind = EventTranscriptFinal
			}
			events = append(events, Event{Kind: kind, Role: "user", Text: content.InputTranscription.Text, TextSource: "audio", Final: content.InputTranscription.Finished})
		}
		if content.OutputTranscription != nil && content.OutputTranscription.Text != "" {
			kind := EventTranscriptDelta
			if content.OutputTranscription.Finished {
				kind = EventTranscriptFinal
			}
			events = append(events, Event{Kind: kind, Role: "assistant", Text: content.OutputTranscription.Text, TextSource: "audio", Final: content.OutputTranscription.Finished})
		}
		if content.ModelTurn != nil {
			for _, part := range content.ModelTurn.Parts {
				if part.InlineData != nil && part.InlineData.Data != "" {
					pcm, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
					if err != nil {
						return append(events, Event{Kind: EventError, Error: fmt.Errorf("gemini live: decode audio delta: %w", err)})
					}
					if rate := pcmRate(part.InlineData.MIMEType); rate > 0 {
						s.info.OutputSampleRate = rate
					}
					events = append(events, Event{Kind: EventAudio, PCM: pcm})
				}
				if part.Text != "" {
					events = append(events, Event{Kind: EventTranscriptDelta, Role: "assistant", Text: part.Text, TextSource: "text"})
				}
			}
		}
		if content.Interrupted {
			events = append(events, Event{Kind: EventInterruption, At: "assistant"})
		}
		if content.TurnComplete {
			events = append(events, Event{Kind: EventResponseDone, Status: "completed"})
			s.responseMu.Lock()
			s.responseActive = false
			s.responseMu.Unlock()
		}
	}
	if envelope.ToolCall != nil {
		s.responseMu.Lock()
		if !s.responseActive {
			events = append(events, Event{Kind: EventResponseStarted})
			s.responseActive = true
		}
		s.responseMu.Unlock()
		for _, call := range envelope.ToolCall.FunctionCalls {
			s.toolMu.Lock()
			s.toolNames[call.ID] = call.Name
			s.toolMu.Unlock()
			args, err := json.Marshal(call.Args)
			if err != nil {
				events = append(events, Event{Kind: EventError, Error: err})
				continue
			}
			events = append(events, Event{Kind: EventToolCall, CallID: call.ID, Name: call.Name, Arguments: string(args)})
		}
	}
	if len(envelope.ToolCancellation) > 0 {
		events = append(events, Event{Kind: EventInterruption, At: "assistant"})
	}
	if envelope.UsageMetadata != nil {
		events = append(events, Event{Kind: EventUsage, Usage: Usage{InputTokens: envelope.UsageMetadata.PromptTokenCount, OutputTokens: envelope.UsageMetadata.ResponseTokenCount, TotalTokens: envelope.UsageMetadata.TotalTokenCount}})
	}
	if envelope.GoAway != nil {
		events = append(events, Event{Kind: EventError, Error: fmt.Errorf("gemini live: server requested reconnect in %s", envelope.GoAway.TimeLeft)})
	}
	return events
}

type geminiServerContent struct {
	ModelTurn           *geminiModelTurn     `json:"modelTurn"`
	InputTranscription  *geminiTranscription `json:"inputTranscription"`
	OutputTranscription *geminiTranscription `json:"outputTranscription"`
	TurnComplete        bool                 `json:"turnComplete"`
	Interrupted         bool                 `json:"interrupted"`
}

type geminiModelTurn struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiTranscription struct {
	Text     string `json:"text"`
	Finished bool   `json:"finished"`
}

type geminiToolCall struct {
	FunctionCalls []geminiFunctionCall `json:"functionCalls"`
}

type geminiFunctionCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiUsageMetadata struct {
	PromptTokenCount   int `json:"promptTokenCount"`
	ResponseTokenCount int `json:"responseTokenCount"`
	TotalTokenCount    int `json:"totalTokenCount"`
}

func pcmRate(mime string) int {
	idx := strings.Index(strings.ToLower(mime), "rate=")
	if idx < 0 {
		return 0
	}
	raw := mime[idx+len("rate="):]
	if end := strings.IndexAny(raw, "; ,"); end >= 0 {
		raw = raw[:end]
	}
	rate, _ := strconv.Atoi(raw)
	return rate
}

var _ Provider = GeminiProvider{}
var _ TextSession = (*geminiSession)(nil)
