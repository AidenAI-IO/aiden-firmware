package realtimevoice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

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
		jsonRealtimeSession: newJSONRealtimeSession(transport, newPCM16SessionInfo(cfg.SessionID, inputRate, outputRate, Capabilities{EmitsSpeechEvents: true, ExplicitToolContinuation: true})),
	}
	transport.start(func(body []byte) []Event {
		event, ok := translateOpenAIEvent(body)
		if !ok {
			return nil
		}
		return []Event{event}
	})

	if err := s.waitReady(ctx, 1); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.writeJSON(ctx, buildOpenAISessionUpdate(cfg, model)); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.waitReady(ctx, 1); err != nil {
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
	inputRate := cfg.InputSampleRate
	if inputRate <= 0 {
		inputRate = 24000
	}
	outputRate := cfg.OutputSampleRate
	if outputRate <= 0 {
		outputRate = 24000
	}
	settings := openAISessionSettings{
		Type:             "realtime",
		Model:            model,
		OutputModalities: []string{"audio"},
		Instructions:     cfg.Instructions,
		Audio: openAIAudioSettings{
			Input: openAIAudioInput{
				Format:        openAIAudioFormat{Type: "audio/pcm", Rate: inputRate},
				Transcription: openAITranscriptionConfig{Model: DefaultOpenAIInputTranscriptionModel},
			},
			Output: openAIAudioOutput{Format: openAIAudioFormat{Type: "audio/pcm", Rate: outputRate}, Voice: cfg.Voice},
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
	*jsonRealtimeSession
}

func (s *openAISession) Interrupt(ctx context.Context, interruption ResponseInterruption) error {
	if err := s.cancelResponse(ctx); err != nil {
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
