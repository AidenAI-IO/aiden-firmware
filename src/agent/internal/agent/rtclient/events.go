package rtclient

import (
	"encoding/base64"
	"encoding/json"
)

const (
	EventSessionUpdate            = "session.update"
	EventInputAudioAppend         = "input_audio_buffer.append"
	EventInputAudioCommit         = "input_audio_buffer.commit"
	EventInputAudioClear          = "input_audio_buffer.clear"
	EventConversationItemCreate   = "conversation.item.create"
	EventConversationItemRetrieve = "conversation.item.retrieve"
	EventConversationItemDelete   = "conversation.item.delete"
	EventResponseCreate           = "response.create"
	EventResponseCancel           = "response.cancel"
)

type Event struct {
	Type    string                     `json:"type"`
	EventID string                     `json:"event_id,omitempty"`
	Raw     json.RawMessage            `json:"-"`
	Data    map[string]json.RawMessage `json:"-"`
}

func (e *Event) UnmarshalJSON(b []byte) error {
	type alias Event
	var a struct {
		Type    string `json:"type"`
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	e.Type, e.EventID, e.Raw = a.Type, a.EventID, append(e.Raw[:0], b...)
	return json.Unmarshal(b, &e.Data)
}
func (e Event) Decode(v any) error { return json.Unmarshal(e.Raw, v) }

type SimpleEvent struct {
	Type string `json:"type"`
}
type SessionUpdate struct {
	Type    string        `json:"type"`
	Session SessionConfig `json:"session,omitempty"`
}
type InputAudioAppend struct {
	Type  string `json:"type"`
	Audio []byte `json:"-"`
}

func (e InputAudioAppend) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}{e.Type, base64.StdEncoding.EncodeToString(e.Audio)})
}

type ConversationItemCreate struct {
	Type           string           `json:"type"`
	PreviousItemID string           `json:"previous_item_id,omitempty"`
	Item           ConversationItem `json:"item"`
}
type ConversationItemRetrieve struct {
	Type   string `json:"type"`
	ItemID string `json:"item_id"`
}
type ConversationItemDelete struct {
	Type   string `json:"type"`
	ItemID string `json:"item_id"`
}
type ResponseCreate struct {
	Type     string                `json:"type"`
	Response *ResponseCreateConfig `json:"response,omitempty"`
}

type SessionConfig struct {
	Modalities          []string       `json:"modalities,omitempty"`
	Voice               string         `json:"voice,omitempty"`
	EnableSpeechEmotion *bool          `json:"enable_speech_emotion,omitempty"`
	Instructions        string         `json:"instructions,omitempty"`
	InputAudioFormat    string         `json:"input_audio_format,omitempty"`
	OutputAudioFormat   string         `json:"output_audio_format,omitempty"`
	MaxHistoryTurns     int            `json:"max_history_turns,omitempty"`
	Tools               []Tool         `json:"tools,omitempty"`
	TurnDetection       *TurnDetection `json:"turn_detection,omitempty"`
	PushToTalk          bool           `json:"-"`
}

func (c SessionConfig) MarshalJSON() ([]byte, error) {
	type plain SessionConfig
	b, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	if !c.PushToTalk {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["turn_detection"] = json.RawMessage("null")
	return json.Marshal(m)
}

type TurnDetection struct {
	Type                string   `json:"type,omitempty"`
	Threshold           *float64 `json:"threshold,omitempty"`
	SilenceDurationMS   int      `json:"silence_duration_ms,omitempty"`
	VoiceprintAudioURLs []string `json:"voiceprint_audio_urls,omitempty"`
}
type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}
type ResponseCreateConfig struct {
	Modalities []string `json:"modalities,omitempty"`
	Voice      string   `json:"voice,omitempty"`
}
type ConversationItem struct {
	ID        string        `json:"id,omitempty"`
	Object    string        `json:"object,omitempty"`
	Type      string        `json:"type"`
	Status    string        `json:"status,omitempty"`
	Role      string        `json:"role,omitempty"`
	Content   []ContentPart `json:"content,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Output    string        `json:"output,omitempty"`
}
type ContentPart struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Audio      string `json:"audio,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

func (e Event) AudioDelta() ([]byte, error) {
	var x struct {
		Delta string `json:"delta"`
	}
	if err := e.Decode(&x); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(x.Delta)
}

// Server event payloads. Event.Decode unmarshals an Event into any of these.
type SessionEvent struct {
	EventID string        `json:"event_id"`
	Type    string        `json:"type"`
	Session ServerSession `json:"session"`
}
type ServerSession struct {
	ID                      string                    `json:"id"`
	Object                  string                    `json:"object"`
	Model                   string                    `json:"model"`
	Modalities              []string                  `json:"modalities"`
	Voice                   string                    `json:"voice"`
	InputAudioTranscription *AudioTranscriptionConfig `json:"input_audio_transcription,omitempty"`
	TurnDetection           *TurnDetection            `json:"turn_detection"`
	Tools                   []Tool                    `json:"tools,omitempty"`
}
type AudioTranscriptionConfig struct {
	Model string `json:"model"`
}
type ErrorEvent struct {
	EventID string   `json:"event_id"`
	Type    string   `json:"type"`
	Error   APIError `json:"error"`
}
type APIError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
	EventID string `json:"event_id,omitempty"`
}
type ItemEvent struct {
	EventID        string           `json:"event_id"`
	Type           string           `json:"type"`
	PreviousItemID string           `json:"previous_item_id,omitempty"`
	ItemID         string           `json:"item_id,omitempty"`
	Item           ConversationItem `json:"item"`
}
type AudioBufferEvent struct {
	EventID      string `json:"event_id"`
	Type         string `json:"type"`
	ItemID       string `json:"item_id,omitempty"`
	AudioStartMS int    `json:"audio_start_ms,omitempty"`
	AudioEndMS   int    `json:"audio_end_ms,omitempty"`
	Reason       string `json:"reason,omitempty"`
}
type TranscriptEvent struct {
	EventID      string `json:"event_id"`
	Type         string `json:"type"`
	ItemID       string `json:"item_id"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta,omitempty"`
	Transcript   string `json:"transcript,omitempty"`
}
type ResponseEvent struct {
	EventID  string   `json:"event_id"`
	Type     string   `json:"type"`
	Response Response `json:"response"`
}
type Response struct {
	ID            string             `json:"id"`
	Object        string             `json:"object,omitempty"`
	Status        string             `json:"status"`
	StatusDetails *StatusDetails     `json:"status_details,omitempty"`
	Modalities    []string           `json:"modalities,omitempty"`
	Voice         string             `json:"voice,omitempty"`
	Output        []ConversationItem `json:"output,omitempty"`
	Usage         *Usage             `json:"usage,omitempty"`
}
type StatusDetails struct {
	Type   string    `json:"type"`
	Reason string    `json:"reason,omitempty"`
	Error  *APIError `json:"error,omitempty"`
}
type Usage struct {
	TotalTokens         int             `json:"total_tokens"`
	InputTokens         int             `json:"input_tokens"`
	OutputTokens        int             `json:"output_tokens"`
	InputTokensDetails  TokenDetails    `json:"input_tokens_details"`
	OutputTokensDetails TokenDetails    `json:"output_tokens_details"`
	Plugins             json.RawMessage `json:"plugins,omitempty"`
}
type TokenDetails struct {
	TextTokens  int `json:"text_tokens"`
	AudioTokens int `json:"audio_tokens,omitempty"`
}
type OutputItemEvent struct {
	EventID     string           `json:"event_id"`
	Type        string           `json:"type"`
	ResponseID  string           `json:"response_id"`
	OutputIndex int              `json:"output_index"`
	Item        ConversationItem `json:"item"`
}
type ContentPartEvent struct {
	EventID      string      `json:"event_id"`
	Type         string      `json:"type"`
	ResponseID   string      `json:"response_id"`
	ItemID       string      `json:"item_id"`
	OutputIndex  int         `json:"output_index"`
	ContentIndex int         `json:"content_index"`
	Part         ContentPart `json:"part"`
}
type ResponseDeltaEvent struct {
	EventID      string `json:"event_id"`
	Type         string `json:"type"`
	ResponseID   string `json:"response_id"`
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta,omitempty"`
	Text         string `json:"text,omitempty"`
	Transcript   string `json:"transcript,omitempty"`
}

func (e ResponseDeltaEvent) Audio() ([]byte, error) { return base64.StdEncoding.DecodeString(e.Delta) }

type FunctionCallEvent struct {
	EventID     string `json:"event_id"`
	Type        string `json:"type"`
	ResponseID  string `json:"response_id"`
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	CallID      string `json:"call_id"`
	Name        string `json:"name,omitempty"`
	Delta       string `json:"delta,omitempty"`
	Arguments   string `json:"arguments,omitempty"`
}
type VoiceprintEvent struct {
	EventID string `json:"event_id"`
	Type    string `json:"type"`
	ItemID  string `json:"item_id"`
	Reason  string `json:"reason,omitempty"`
}
