package realtimevoice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"aiden-agent/internal/agent/rtclient"
)

type QwenProvider struct {
	WorkspaceID string
	Region      string
	Endpoint    string
}

func (p QwenProvider) Open(ctx context.Context, cfg SessionConfig) (Session, error) {
	client, err := rtclient.New(rtclient.Config{APIKey: cfg.APIKey, Model: cfg.Model, WorkspaceID: p.WorkspaceID, Region: p.Region, Endpoint: p.Endpoint})
	if err != nil {
		return nil, err
	}
	ws, err := client.Connect(ctx)
	if err != nil {
		return nil, err
	}
	created, err := waitQwenEvent(ctx, ws, "session.created")
	if err != nil {
		_ = ws.Close()
		return nil, err
	}
	if err := ws.Update(ctx, qwenSessionConfig(cfg)); err != nil {
		_ = ws.Close()
		return nil, err
	}
	if _, err := waitQwenEvent(ctx, ws, "session.updated"); err != nil {
		_ = ws.Close()
		return nil, err
	}
	var sessionEvent rtclient.SessionEvent
	_ = created.Decode(&sessionEvent)
	return &qwenSession{
		ws:            ws,
		info:          newPCM16SessionInfo(sessionEvent.Session.ID, 16000, 24000, Capabilities{ExplicitToolContinuation: true}),
		translateStop: make(chan struct{}),
	}, nil
}

func qwenSessionConfig(cfg SessionConfig) rtclient.SessionConfig {
	out := rtclient.SessionConfig{Modalities: []string{"audio", "text"}, Voice: cfg.Voice, Instructions: cfg.Instructions, InputAudioFormat: cfg.InputAudioFormat, OutputAudioFormat: cfg.OutputAudioFormat, MaxHistoryTurns: cfg.MaxHistoryTurns, Tools: make([]rtclient.Tool, 0, len(cfg.Tools))}
	if out.Voice == "" {
		out.Voice = rtclient.DefaultVoice
	}
	out.EnableSpeechEmotion = cfg.EnableSpeechEmotion
	if cfg.TurnDetection != "" {
		out.TurnDetection = &rtclient.TurnDetection{Type: cfg.TurnDetection, Threshold: cfg.TurnDetectionThresh, SilenceDurationMS: cfg.TurnDetectionSilenceMs}
	}
	for _, tool := range cfg.Tools {
		out.Tools = append(out.Tools, rtclient.Tool{Type: "function", Function: rtclient.FunctionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	return out
}

func waitQwenEvent(ctx context.Context, ws *rtclient.Session, want string) (rtclient.Event, error) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	errs := ws.Errors()
	events := ws.Events()
	for {
		select {
		case <-ctx.Done():
			return rtclient.Event{}, ctx.Err()
		case <-timer.C:
			return rtclient.Event{}, fmt.Errorf("timed out waiting for %s", want)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return rtclient.Event{}, err
			}
		case ev, ok := <-events:
			if !ok {
				return rtclient.Event{}, errors.New("qwen realtime event stream closed")
			}
			if ev.Type == "error" {
				var x rtclient.ErrorEvent
				if err := ev.Decode(&x); err != nil {
					return rtclient.Event{}, err
				}
				return rtclient.Event{}, errors.New(x.Error.Message)
			}
			if ev.Type == want {
				return ev, nil
			}
		}
	}
}

type qwenSession struct {
	ws            *rtclient.Session
	info          SessionInfo
	events        chan Event
	translateStop chan struct{}
	startOnce     sync.Once
	closeOnce     sync.Once
	closeErr      error
}

func (s *qwenSession) Info() SessionInfo { return s.info }
func (s *qwenSession) Events() <-chan Event {
	s.startOnce.Do(func() {
		s.events = make(chan Event, 64)
		go s.translate()
	})
	return s.events
}
func (s *qwenSession) Errors() <-chan error  { return s.ws.Errors() }
func (s *qwenSession) Done() <-chan struct{} { return s.ws.Done() }
func (s *qwenSession) SendAudio(ctx context.Context, pcm []byte) error {
	return s.ws.AppendAudio(ctx, pcm)
}
func (s *qwenSession) Commit(ctx context.Context) error { return s.ws.CommitAudio(ctx) }
func (s *qwenSession) Interrupt(ctx context.Context, _ ResponseInterruption) error {
	return s.ws.CancelResponse(ctx)
}
func (s *qwenSession) SendToolResult(ctx context.Context, id, out string) error {
	return s.ws.SendFunctionOutput(ctx, id, out)
}
func (s *qwenSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.translateStop)
		s.closeErr = s.ws.Close()
	})
	return s.closeErr
}
func (s *qwenSession) SendText(ctx context.Context, text string) error {
	return s.ws.SendText(ctx, text, "")
}
func (s *qwenSession) CreateResponse(ctx context.Context) error { return s.ws.CreateResponse(ctx, nil) }
func (s *qwenSession) ReplayContext(ctx context.Context, items []ContextItem) error {
	var previous string
	for _, item := range items {
		contentType := "input_text"
		if item.Role == "assistant" {
			contentType = "output_text"
		}
		ci := rtclient.ConversationItem{Type: item.Type, Role: item.Role, Content: []rtclient.ContentPart{{Type: contentType, Text: item.Content}}, CallID: item.CallID, Name: item.Name, Arguments: item.Arguments, Output: item.Output}
		if item.Type == "function_call" || item.Type == "function_call_output" {
			ci.Content = nil
		}
		if err := s.ws.CreateItem(ctx, ci, previous); err != nil {
			return err
		}
	}
	return nil
}
func (s *qwenSession) translate() {
	forwardQwenEvents(s.ws.Events(), s.translateStop, s.events)
}

func forwardQwenEvents(events <-chan rtclient.Event, stop <-chan struct{}, output chan<- Event) {
	defer close(output)
	for ev := range events {
		out, ok := translateQwenEvent(ev)
		if ok {
			select {
			case output <- out:
			case <-stop:
				return
			}
		}
	}
}

func translateQwenEvent(ev rtclient.Event) (Event, bool) {
	switch ev.Type {
	case "session.created":
		return Event{Kind: EventReady}, true
	case "session.updated":
		return Event{Kind: EventReady}, true
	case "input_audio_buffer.speech_started":
		return Event{Kind: EventSpeechStarted}, true
	case "input_audio_buffer.speech_stopped", "input_audio_buffer.committed":
		return Event{Kind: EventSpeechStopped}, true
	case "conversation.item.input_audio_transcription.completed":
		var x rtclient.TranscriptEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptFinal, ItemID: x.ItemID, Role: "user", Text: x.Transcript, TextSource: "audio", Final: true}, true
	case "response.created":
		var x rtclient.ResponseEvent
		_ = ev.Decode(&x)
		return Event{Kind: EventResponseStarted, ResponseID: x.Response.ID}, true
	case "response.text.delta":
		var x rtclient.ResponseDeltaEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptDelta, ResponseID: x.ResponseID, ItemID: x.ItemID, Role: "assistant", Text: x.Delta, TextSource: "text"}, true
	case "response.audio_transcript.delta":
		var x rtclient.ResponseDeltaEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptDelta, ResponseID: x.ResponseID, ItemID: x.ItemID, Role: "assistant", Text: x.Delta, TextSource: "audio"}, true
	case "response.audio_transcript.done":
		var x rtclient.TranscriptEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventTranscriptFinal, ItemID: x.ItemID, Role: "assistant", Text: x.Transcript, TextSource: "audio", Final: true}, true
	case "response.audio.delta":
		var x rtclient.ResponseDeltaEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		pcm, err := x.Audio()
		if err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventAudio, ResponseID: x.ResponseID, ItemID: x.ItemID, PCM: pcm}, true
	case "response.function_call_arguments.done":
		var x rtclient.FunctionCallEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventToolCall, ResponseID: x.ResponseID, ItemID: x.ItemID, CallID: x.CallID, Name: x.Name, Arguments: x.Arguments}, true
	case "response.done":
		var x rtclient.ResponseEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		var usage Usage
		if x.Response.Usage != nil {
			usage = Usage{InputTokens: x.Response.Usage.InputTokens, OutputTokens: x.Response.Usage.OutputTokens, TotalTokens: x.Response.Usage.TotalTokens}
		}
		return terminalResponseEvent("qwen", x.Response.ID, x.Response.Status, usage), true
	case "error":
		var x rtclient.ErrorEvent
		if err := ev.Decode(&x); err != nil {
			return Event{Kind: EventError, Error: err}, true
		}
		return Event{Kind: EventError, Error: errors.New(x.Error.Message)}, true
	default:
		return Event{}, false
	}
}

var _ TextSession = (*qwenSession)(nil)
