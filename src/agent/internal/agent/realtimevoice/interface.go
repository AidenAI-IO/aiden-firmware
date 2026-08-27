package realtimevoice

import (
	"context"
	"encoding/json"
)

// Provider opens provider-neutral realtime voice sessions. Implementations
// hide provider wire formats, authentication, and transport details.
type Provider interface {
	Open(context.Context, SessionConfig) (Session, error)
}

type SessionConfig struct {
	APIKey                 string
	Model                  string
	Voice                  string
	Instructions           string
	InputAudioFormat       string
	OutputAudioFormat      string
	InputSampleRate        int
	OutputSampleRate       int
	MaxHistoryTurns        int
	TurnDetection          string
	TurnDetectionThresh    *float64
	TurnDetectionSilenceMs int
	EnableSpeechEmotion    *bool
	Tools                  []Tool
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type Capabilities struct {
	TextInput                bool
	ContextReplay            bool
	ManualCommit             bool
	Interrupt                bool
	ToolCalls                bool
	ExplicitToolContinuation bool
}

type SessionInfo struct {
	ID               string
	InputSampleRate  int
	OutputSampleRate int
	Capabilities     Capabilities
}

type Session interface {
	Info() SessionInfo
	Events() <-chan Event
	Errors() <-chan error
	Done() <-chan struct{}
	SendAudio(context.Context, []byte) error
	Commit(context.Context) error
	Interrupt(context.Context) error
	SendToolResult(context.Context, string, string) error
	Close() error
}

// TextSession is an optional capability. Speko S2S deliberately does not
// implement it because text injection is not part of its documented contract.
type TextSession interface {
	Session
	SendText(context.Context, string) error
	CreateResponse(context.Context) error
	ReplayContext(context.Context, []ContextItem) error
}

type ContextItem struct {
	Role      string
	Content   string
	Type      string
	CallID    string
	Name      string
	Arguments string
	Output    string
}

type EventKind string

const (
	EventReady             EventKind = "ready"
	EventAudio             EventKind = "audio"
	EventSpeechStarted     EventKind = "speech_started"
	EventSpeechStopped     EventKind = "speech_stopped"
	EventTranscriptDelta   EventKind = "transcript_delta"
	EventTranscriptFinal   EventKind = "transcript_final"
	EventResponseStarted   EventKind = "response_started"
	EventResponseDone      EventKind = "response_done"
	EventResponseCancelled EventKind = "response_cancelled"
	EventInterruption      EventKind = "interruption"
	EventToolCall          EventKind = "tool_call"
	EventUsage             EventKind = "usage"
	EventError             EventKind = "error"
	EventClosed            EventKind = "closed"
)

type Event struct {
	Kind       EventKind
	SessionID  string
	ResponseID string
	CallID     string
	Name       string
	Arguments  string
	Text       string
	TextSource string
	PCM        []byte
	Role       string
	Final      bool
	At         string
	Status     string
	Error      error
	Usage      Usage
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}
