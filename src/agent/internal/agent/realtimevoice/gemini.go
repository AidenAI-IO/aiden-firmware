package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	geminiVertexLivePath      = "/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent"
)

// GeminiProvider is the native Google Gemini Live adapter. AuthMode selects
// the Gemini Developer API (api_key) or Vertex AI (vertex/OAuth) wire path.
type GeminiProvider struct {
	Endpoint    string
	Dialer      *websocket.Dialer
	EventBuffer int
	AuthMode    string
	ProjectID   string
	Location    string
	// DelegatedCredential makes the adapter pass the Speko-minted token as access_token.
	DelegatedCredential bool
}

func (p GeminiProvider) Open(ctx context.Context, cfg SessionConfig) (Session, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("gemini live: APIKey is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultGeminiLiveModel
	}
	setupModel, err := p.setupModel(model)
	if err != nil {
		return nil, err
	}
	endpoint, err := p.endpoint(model, cfg.APIKey)
	if err != nil {
		return nil, err
	}
	d := p.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	conn, resp, err := d.DialContext(ctx, endpoint, p.headers(cfg.APIKey))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("gemini live websocket connect: %w", err)
	}
	inputRate := cfg.InputSampleRate
	if inputRate <= 0 {
		inputRate = 16000
	}
	outputRate := cfg.OutputSampleRate
	if outputRate <= 0 {
		outputRate = 24000
	}
	transport := newJSONWebSocketTransport(conn, "gemini live", p.EventBuffer)
	s := &geminiSession{
		jsonWebSocketTransport: transport,
		toolNames:              make(map[string]string),
		info:                   newPCM16SessionInfo(cfg.SessionID, inputRate, outputRate, Capabilities{}),
		inputRate:              inputRate,
	}
	transport.start(s.translate)
	if err := s.writeJSON(ctx, buildGeminiSetup(cfg, setupModel)); err != nil {
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
	authMode, err := p.authMode()
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(p.Endpoint)
	if raw == "" {
		if authMode == "vertex" {
			if err := validateGeminiVertexPart("location", p.Location); err != nil {
				return "", err
			}
			raw = "wss://" + strings.TrimSpace(p.Location) + "-aiplatform.googleapis.com" + geminiVertexLivePath
		} else {
			raw = DefaultGeminiLiveEndpoint
		}
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
		if authMode == "vertex" {
			u.Path = geminiVertexLivePath
		} else {
			u.Path = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
		}
	}
	q := u.Query()
	delegated := authMode == "delegated" || q.Get("access_token") != ""
	if authMode == "vertex" {
		q.Del("key")
		q.Del("access_token")
	} else if delegated {
		q.Del("key")
		q.Set("access_token", apiKey)
	} else if q.Get("key") == "" {
		q.Set("key", apiKey)
	}
	// Speko delegated credentials are accepted by Gemini's constrained Live endpoint.
	if delegated && strings.HasSuffix(u.Path, "BidiGenerateContent") && !strings.HasSuffix(u.Path, "BidiGenerateContentConstrained") {
		u.Path += "Constrained"
	}
	_ = model // The Live API selects the model in the initial setup message.
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (p GeminiProvider) authMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(p.AuthMode))
	if p.DelegatedCredential {
		if mode != "" && mode != "delegated" {
			return "", errors.New("gemini live: delegated credentials cannot be combined with an explicit auth mode")
		}
		return "delegated", nil
	}
	switch mode {
	case "", "api_key", "apikey":
		return "api_key", nil
	case "vertex", "oauth":
		return "vertex", nil
	default:
		return "", fmt.Errorf("gemini live: unsupported auth mode %q", p.AuthMode)
	}
}

func (p GeminiProvider) headers(credential string) http.Header {
	mode, _ := p.authMode()
	if mode != "vertex" {
		return nil
	}
	header := make(http.Header, 1)
	header.Set("Authorization", bearer(credential))
	return header
}

func (p GeminiProvider) setupModel(model string) (string, error) {
	mode, err := p.authMode()
	if err != nil {
		return "", err
	}
	if mode != "vertex" || strings.HasPrefix(model, "projects/") {
		return model, nil
	}
	if err := validateGeminiVertexPart("project_id", p.ProjectID); err != nil {
		return "", err
	}
	if err := validateGeminiVertexPart("location", p.Location); err != nil {
		return "", err
	}
	return fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s", strings.TrimSpace(p.ProjectID), strings.TrimSpace(p.Location), model), nil
}

func validateGeminiVertexPart(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("gemini live: Vertex %s is required", name)
	}
	if strings.ContainsAny(value, "/?#") {
		return fmt.Errorf("gemini live: invalid Vertex %s %q", name, value)
	}
	return nil
}

type geminiSetupMessage struct {
	Setup geminiSetup `json:"setup"`
}

type geminiSetup struct {
	Model             string                   `json:"model"`
	GenerationConfig  geminiGenerationConfig   `json:"generationConfig"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	// Gemini Live enables transcription by the presence of these keys, and an
	// empty object is the documented way to request it with default settings.
	// omitempty would drop an empty map and silently disable transcription, so
	// these are always serialized. A nil map still marshals as null, which the
	// API treats as "not requested".
	InputAudioTranscription  map[string]any             `json:"inputAudioTranscription"`
	OutputAudioTranscription map[string]any             `json:"outputAudioTranscription"`
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
	if !strings.HasPrefix(model, "models/") && !strings.HasPrefix(model, "projects/") {
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
	*jsonWebSocketTransport
	info           SessionInfo
	inputRate      int
	infoMu         sync.RWMutex
	responseMu     sync.Mutex
	responseActive bool
	toolMu         sync.Mutex
	toolNames      map[string]string
	// userTranscript accumulates inputTranscription deltas for the current user
	// turn. Gemini Live never marks an input transcript as finished, so the turn
	// boundary is the only place a final transcript can be emitted.
	userTranscriptMu sync.Mutex
	userTranscript   strings.Builder
}

// takeUserTranscript returns the accumulated user transcript and clears it.
func (s *geminiSession) takeUserTranscript() string {
	s.userTranscriptMu.Lock()
	defer s.userTranscriptMu.Unlock()
	text := strings.TrimSpace(s.userTranscript.String())
	s.userTranscript.Reset()
	return text
}

// appendUserTranscript accumulates one inputTranscription delta.
func (s *geminiSession) appendUserTranscript(text string) {
	s.userTranscriptMu.Lock()
	defer s.userTranscriptMu.Unlock()
	s.userTranscript.WriteString(text)
}

// finalUserTranscriptEvent drains the accumulated user turn into a final
// transcript event. It returns false when nothing has been accumulated.
func (s *geminiSession) finalUserTranscriptEvent() (Event, bool) {
	text := s.takeUserTranscript()
	if text == "" {
		return Event{}, false
	}
	return Event{Kind: EventTranscriptFinal, Role: "user", Text: text, TextSource: "audio", Final: true}, true
}

func (s *geminiSession) Info() SessionInfo {
	s.infoMu.RLock()
	defer s.infoMu.RUnlock()
	return s.info
}
func (s *geminiSession) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	rate := s.inputRate
	if rate <= 0 {
		rate = 16000
	}
	return s.writeJSON(ctx, map[string]any{"realtimeInput": map[string]any{
		"audio": map[string]string{"mimeType": fmt.Sprintf("audio/pcm;rate=%d", rate), "data": base64.StdEncoding.EncodeToString(pcm)},
	}})
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
func (s *geminiSession) SendText(ctx context.Context, text string) error {
	return s.writeJSON(ctx, map[string]any{"clientContent": map[string]any{
		"turns":        []map[string]any{{"role": "user", "parts": []map[string]string{{"text": text}}}},
		"turnComplete": false,
	}})
}
func (s *geminiSession) CreateResponse(ctx context.Context) error {
	return s.writeJSON(ctx, map[string]any{"clientContent": map[string]any{"turnComplete": true}})
}

func (s *geminiSession) ReplayContext(ctx context.Context, items []ContextItem) error {
	if len(items) == 0 {
		return nil
	}
	toolNames := make(map[string]string)
	turns := make([]map[string]any, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "message":
			role := "user"
			if item.Role == "assistant" || item.Role == "model" {
				role = "model"
			}
			turns = append(turns, map[string]any{
				"role":  role,
				"parts": []map[string]any{{"text": item.Content}},
			})
		case "function_call":
			args := any(map[string]any{})
			if strings.TrimSpace(item.Arguments) != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
					return fmt.Errorf("gemini live: replay function call %q arguments: %w", item.CallID, err)
				}
			}
			toolNames[item.CallID] = item.Name
			turns = append(turns, map[string]any{
				"role": "model",
				"parts": []map[string]any{{"functionCall": map[string]any{
					"id": item.CallID, "name": item.Name, "args": args,
				}}},
			})
		case "function_call_output":
			output := any(item.Output)
			var decoded any
			if json.Unmarshal([]byte(item.Output), &decoded) == nil {
				output = decoded
			}
			turns = append(turns, map[string]any{
				"role": "user",
				"parts": []map[string]any{{"functionResponse": map[string]any{
					"id": item.CallID, "name": toolNames[item.CallID], "response": map[string]any{"result": output},
				}}},
			})
		default:
			return fmt.Errorf("gemini live: cannot replay context item type %q", item.Type)
		}
	}
	return s.writeJSON(ctx, map[string]any{"clientContent": map[string]any{
		"turns": turns, "turnComplete": false,
	}})
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
		ResponseID       string               `json:"responseId"`
		ResponseIDSnake  string               `json:"response_id"`
		ItemID           string               `json:"itemId"`
		ItemIDSnake      string               `json:"item_id"`
		Sequence         uint64               `json:"sequence"`
		SequenceNumber   uint64               `json:"sequence_number"`
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
	usageEmitted := false
	if envelope.ServerContent != nil {
		content := envelope.ServerContent
		s.responseMu.Lock()
		active := s.responseActive
		s.responseMu.Unlock()
		// Interruption changes the ownership of a same-frame input transcript: it
		// is the beginning of the new user turn, not late refinement of the turn
		// whose response was just cut off.
		if content.Interrupted {
			events = append(events, Event{Kind: EventInterruption, At: "assistant"})
			s.responseMu.Lock()
			s.responseActive = false
			s.responseMu.Unlock()
			active = false
		}
		// Accumulate the user transcript only while no response is in flight.
		// Gemini keeps refining a transcript after the model has already started
		// answering, and those late frames belong to the turn that was finalized
		// when the response began. Accumulating them would file a second, usually
		// worse, user message for the same speech.
		if content.InputTranscription != nil && content.InputTranscription.Text != "" && !active {
			s.appendUserTranscript(content.InputTranscription.Text)
			events = append(events, Event{Kind: EventTranscriptDelta, Role: "user", Text: content.InputTranscription.Text, TextSource: "audio"})
		}
		// The user turn ends when the model starts answering it. modelTurn
		// repeats for every audio chunk, so only the transition into a response
		// may finalize; turnComplete covers a turn that produced no modelTurn.
		startingResponse := content.ModelTurn != nil && !active
		if startingResponse || content.TurnComplete {
			// Emitted before EventResponseStarted below: the daemon persists in
			// event order, so a later flush would file the user turn behind the
			// answer it prompted.
			if final, ok := s.finalUserTranscriptEvent(); ok {
				events = append(events, final)
			}
		}
		if startingResponse {
			events = append(events, Event{Kind: EventResponseStarted})
			s.responseMu.Lock()
			s.responseActive = true
			s.responseMu.Unlock()
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
						s.infoMu.Lock()
						s.info.OutputSampleRate = rate
						s.info.OutputAudioFormat.SampleRate = rate
						s.infoMu.Unlock()
					}
					events = append(events, Event{Kind: EventAudio, PCM: pcm})
				}
				if part.Text != "" {
					events = append(events, Event{Kind: EventTranscriptDelta, Role: "assistant", Text: part.Text, TextSource: "text"})
				}
			}
		}
		if content.TurnComplete {
			if envelope.UsageMetadata != nil {
				events = append(events, Event{Kind: EventUsage, Usage: Usage{InputTokens: envelope.UsageMetadata.PromptTokenCount, OutputTokens: envelope.UsageMetadata.ResponseTokenCount, TotalTokens: envelope.UsageMetadata.TotalTokenCount}})
				usageEmitted = true
			}
			events = append(events, Event{Kind: EventResponseDone, Status: "completed"})
			s.responseMu.Lock()
			s.responseActive = false
			s.responseMu.Unlock()
		}
	}
	if envelope.ToolCall != nil {
		s.responseMu.Lock()
		if !s.responseActive {
			if final, ok := s.finalUserTranscriptEvent(); ok {
				events = append(events, final)
			}
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
	if envelope.UsageMetadata != nil && !usageEmitted {
		events = append(events, Event{Kind: EventUsage, Usage: Usage{InputTokens: envelope.UsageMetadata.PromptTokenCount, OutputTokens: envelope.UsageMetadata.ResponseTokenCount, TotalTokens: envelope.UsageMetadata.TotalTokenCount}})
	}
	if envelope.GoAway != nil {
		// Gemini Live caps session lifetime and announces the cutoff rather than
		// failing, so this is reported as rotation. The caller decides whether to
		// reconnect; reporting a generic error would make a scheduled handover
		// look like a fault.
		events = append(events, Event{Kind: EventError, Error: fmt.Errorf("%w: gemini live ends this session in %s", ErrSessionRotated, envelope.GoAway.TimeLeft)})
	}
	responseID := envelope.ResponseID
	if responseID == "" {
		responseID = envelope.ResponseIDSnake
	}
	itemID := envelope.ItemID
	if itemID == "" {
		itemID = envelope.ItemIDSnake
	}
	sequence := normalizedSequence(envelope.Sequence, envelope.SequenceNumber)
	for index := range events {
		if events[index].ResponseID == "" {
			events[index].ResponseID = responseID
		}
		if events[index].ItemID == "" {
			events[index].ItemID = itemID
		}
		if events[index].Sequence == 0 {
			events[index].Sequence = sequence
		}
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
var _ ContextReplayer = (*geminiSession)(nil)
