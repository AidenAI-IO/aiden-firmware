package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	DefaultOpenAIRealtimeEndpoint        = "wss://api.openai.com/v1/realtime"
	DefaultOpenAIRealtimeModel           = "gpt-realtime"
	DefaultOpenAIInputTranscriptionModel = "gpt-4o-mini-transcribe"
)

// OpenAIProvider is the native OpenAI Realtime adapter. Endpoint is optional
// and is primarily useful for a compatible gateway or protocol tests.
type OpenAIProvider struct {
	Endpoint    string
	Dialer      *websocket.Dialer
	EventBuffer int
}

func (p OpenAIProvider) Open(ctx context.Context, cfg SessionConfig) (Session, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("openai realtime: APIKey is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultOpenAIRealtimeModel
	}
	endpoint, err := p.endpoint(model)
	if err != nil {
		return nil, err
	}
	d := p.Dialer
	if d == nil {
		d = websocket.DefaultDialer
	}
	header := make(map[string][]string, 1)
	header["Authorization"] = []string{bearer(cfg.APIKey)}
	conn, resp, err := d.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("openai realtime websocket connect: %w", err)
	}
	inputRate := cfg.InputSampleRate
	if inputRate <= 0 {
		inputRate = 24000
	}
	outputRate := cfg.OutputSampleRate
	if outputRate <= 0 {
		outputRate = 24000
	}
	transport := newJSONWebSocketTransport(conn, "openai realtime", p.EventBuffer)
	s := &openAISession{
		jsonWebSocketTransport: transport,
		info:                   newPCM16SessionInfo(cfg.SessionID, inputRate, outputRate, Capabilities{ExplicitToolContinuation: true}),
	}
	transport.start(func(body []byte) []Event {
		event, ok := translateOpenAIEvent(body)
		if !ok {
			return nil
		}
		return []Event{event}
	})

	if err := waitOpenAIReady(ctx, s, 1); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.writeJSON(ctx, buildOpenAISessionUpdate(cfg, model)); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := waitOpenAIReady(ctx, s, 1); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (p OpenAIProvider) endpoint(model string) (string, error) {
	raw := strings.TrimSpace(p.Endpoint)
	if raw == "" {
		raw = DefaultOpenAIRealtimeEndpoint
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("openai realtime: parse endpoint: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("openai realtime: endpoint must use ws:// or https://, got %q", u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/realtime"
	}
	q := u.Query()
	q.Set("model", model)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type openAISessionUpdate struct {
	Type    string                `json:"type"`
	Session openAISessionSettings `json:"session"`
}

type openAISessionSettings struct {
	Type             string              `json:"type,omitempty"`
	Model            string              `json:"model,omitempty"`
	OutputModalities []string            `json:"output_modalities,omitempty"`
	Instructions     string              `json:"instructions,omitempty"`
	Audio            openAIAudioSettings `json:"audio"`
	Tools            []openAITool        `json:"tools,omitempty"`
}

type openAIAudioSettings struct {
	Input  openAIAudioInput  `json:"input"`
	Output openAIAudioOutput `json:"output"`
}

type openAIAudioInput struct {
	Format        openAIAudioFormat         `json:"format"`
	Transcription openAITranscriptionConfig `json:"transcription"`
	TurnDetection any                       `json:"turn_detection,omitempty"`
}

type openAITranscriptionConfig struct {
	Model string `json:"model"`
}

type openAIAudioOutput struct {
	Format openAIAudioFormat `json:"format"`
	Voice  string            `json:"voice,omitempty"`
}

type openAIAudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type openAITool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func buildOpenAISessionUpdate(cfg SessionConfig, model string) openAISessionUpdate {
	settings := openAISessionSettings{
		Type:             "realtime",
		Model:            model,
		OutputModalities: []string{"audio"},
		Instructions:     cfg.Instructions,
		Audio: openAIAudioSettings{
			Input: openAIAudioInput{
				Format:        openAIAudioFormat{Type: "audio/pcm", Rate: 24000},
				Transcription: openAITranscriptionConfig{Model: DefaultOpenAIInputTranscriptionModel},
			},
			Output: openAIAudioOutput{Format: openAIAudioFormat{Type: "audio/pcm", Rate: 24000}, Voice: cfg.Voice},
		},
	}
	if turnDetection := turnDetectionConfig(cfg); turnDetection != nil {
		settings.Audio.Input.TurnDetection = turnDetection
	}
	for _, tool := range cfg.Tools {
		settings.Tools = append(settings.Tools, openAITool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return openAISessionUpdate{Type: "session.update", Session: settings}
}

type openAISession struct {
	*jsonWebSocketTransport
	info   SessionInfo
	infoMu sync.RWMutex
}

func (s *openAISession) Info() SessionInfo {
	s.infoMu.RLock()
	defer s.infoMu.RUnlock()
	return s.info
}
func (s *openAISession) SendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	return s.writeJSON(ctx, map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(pcm),
	})
}
func (s *openAISession) Commit(ctx context.Context) error {
	return s.writeJSON(ctx, map[string]any{"type": "input_audio_buffer.commit"})
}
func (s *openAISession) Interrupt(ctx context.Context, interruption ResponseInterruption) error {
	if err := s.writeJSON(ctx, map[string]any{"type": "response.cancel"}); err != nil {
		return err
	}
	if strings.TrimSpace(interruption.ItemID) == "" {
		return nil
	}
	if interruption.AudioEndMS < 0 {
		interruption.AudioEndMS = 0
	}
	return s.writeJSON(ctx, map[string]any{
		"type":          "conversation.item.truncate",
		"item_id":       interruption.ItemID,
		"content_index": 0,
		"audio_end_ms":  interruption.AudioEndMS,
	})
}
func (s *openAISession) SendToolResult(ctx context.Context, id, output string) error {
	return s.writeJSON(ctx, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{"type": "function_call_output", "call_id": id, "output": output},
	})
}
func (s *openAISession) SendText(ctx context.Context, text string) error {
	return s.writeJSON(ctx, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]string{{"type": "input_text", "text": text}},
		},
	})
}
func (s *openAISession) CreateResponse(ctx context.Context) error {
	return s.writeJSON(ctx, map[string]any{"type": "response.create"})
}
func (s *openAISession) ReplayContext(ctx context.Context, items []ContextItem) error {
	// Items are replayed in slice order and the server appends each one to the
	// conversation, so no explicit previous_item_id chaining is sent. Adding it
	// would require tracking server-assigned ids from conversation.item.created.
	for _, item := range items {
		message := contextItemPayload(item)
		if err := s.writeJSON(ctx, map[string]any{"type": "conversation.item.create", "item": message}); err != nil {
			return err
		}
	}
	return nil
}

func waitOpenAIReady(ctx context.Context, s *openAISession, count int) error {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for count > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("openai realtime: timed out waiting for session ready")
		case err, ok := <-s.errs:
			if !ok {
				return errors.New("openai realtime: event stream closed")
			}
			if err != nil {
				return err
			}
		case event, ok := <-s.events:
			if !ok {
				return errors.New("openai realtime: event stream closed")
			}
			if event.Kind == EventError {
				return event.Error
			}
			if event.Kind == EventReady {
				if event.SessionID != "" {
					s.infoMu.Lock()
					s.info.ID = event.SessionID
					s.infoMu.Unlock()
				}
				count--
			}
		}
	}
	return nil
}

func translateOpenAIEvent(body []byte) (Event, bool) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Event{Kind: EventError, Error: fmt.Errorf("openai realtime: decode event: %w", err)}, true
	}
	// The GA Realtime API renamed assistant output events to response.output_*;
	// retain the beta aliases so gateways and older models remain compatible.
	switch envelope.Type {
	case "response.output_text.delta":
		envelope.Type = "response.text.delta"
	case "response.output_audio_transcript.delta":
		envelope.Type = "response.audio_transcript.delta"
	case "response.output_audio_transcript.done":
		envelope.Type = "response.audio_transcript.done"
	case "response.output_audio.delta":
		envelope.Type = "response.audio.delta"
	}
	switch envelope.Type {
	case "session.created", "session.updated":
		var event struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventReady, SessionID: event.Session.ID}, true
	case "input_audio_buffer.speech_started":
		return Event{Kind: EventSpeechStarted}, true
	case "input_audio_buffer.speech_stopped", "input_audio_buffer.committed":
		return Event{Kind: EventSpeechStopped}, true
	case "conversation.item.input_audio_transcription.delta":
		var event struct {
			Delta          string `json:"delta"`
			ItemID         string `json:"item_id"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptDelta, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), Role: "user", Text: event.Delta, TextSource: "audio"}, true
	case "conversation.item.input_audio_transcription.completed":
		var event struct {
			Transcript     string `json:"transcript"`
			ItemID         string `json:"item_id"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptFinal, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), Role: "user", Text: event.Transcript, TextSource: "audio", Final: true}, true
	case "response.created":
		var event struct {
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventResponseStarted, ResponseID: event.Response.ID}, true
	case "response.text.delta":
		var event struct {
			Delta          string `json:"delta"`
			ResponseID     string `json:"response_id"`
			ItemID         string `json:"item_id"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptDelta, ResponseID: event.ResponseID, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), Role: "assistant", Text: event.Delta, TextSource: "text"}, true
	case "response.audio_transcript.delta":
		var event struct {
			Delta          string `json:"delta"`
			ResponseID     string `json:"response_id"`
			ItemID         string `json:"item_id"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptDelta, ResponseID: event.ResponseID, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), Role: "assistant", Text: event.Delta, TextSource: "audio"}, true
	case "response.audio_transcript.done":
		var event struct {
			Transcript     string `json:"transcript"`
			ResponseID     string `json:"response_id"`
			ItemID         string `json:"item_id"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptFinal, ResponseID: event.ResponseID, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), Role: "assistant", Text: event.Transcript, TextSource: "audio", Final: true}, true
	case "response.audio.delta":
		var event struct {
			Delta          string `json:"delta"`
			ResponseID     string `json:"response_id"`
			ItemID         string `json:"item_id"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		pcm, err := base64.StdEncoding.DecodeString(event.Delta)
		if err != nil {
			return Event{Kind: EventError, Error: fmt.Errorf("openai realtime: decode audio delta: %w", err)}, true
		}
		return Event{Kind: EventAudio, ResponseID: event.ResponseID, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), PCM: pcm}, true
	case "response.function_call_arguments.done":
		var event struct {
			ResponseID     string `json:"response_id"`
			ItemID         string `json:"item_id"`
			CallID         string `json:"call_id"`
			Name           string `json:"name"`
			Arguments      string `json:"arguments"`
			Sequence       uint64 `json:"sequence"`
			SequenceNumber uint64 `json:"sequence_number"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventToolCall, ResponseID: event.ResponseID, ItemID: event.ItemID, Sequence: normalizedSequence(event.Sequence, event.SequenceNumber), CallID: event.CallID, Name: event.Name, Arguments: event.Arguments}, true
	case "response.done":
		var event struct {
			Response struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Usage  *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					TotalTokens  int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		var usage Usage
		if event.Response.Usage != nil {
			usage = Usage{InputTokens: event.Response.Usage.InputTokens, OutputTokens: event.Response.Usage.OutputTokens, TotalTokens: event.Response.Usage.TotalTokens}
		}
		return terminalResponseEvent("openai", event.Response.ID, event.Response.Status, usage), true
	case "conversation.item.truncated", "response.cancelled":
		return Event{Kind: EventInterruption, At: "assistant"}, true
	case "error":
		var event struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventError, Error: errors.New(event.Error.Message)}, true
	default:
		return Event{}, false
	}
}

var _ Provider = OpenAIProvider{}
var _ TextSession = (*openAISession)(nil)
