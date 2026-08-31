package realtimevoice

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"aiden-agent/internal/agent/rtclient"
)

func TestTranslateQwenEvents(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		kind  EventKind
		check func(*testing.T, Event)
	}{
		{name: "speech", raw: `{"type":"input_audio_buffer.speech_started"}`, kind: EventSpeechStarted},
		{name: "audio", raw: `{"type":"response.audio.delta","delta":"` + base64.StdEncoding.EncodeToString([]byte{1, 2}) + `"}`, kind: EventAudio, check: func(t *testing.T, e Event) {
			if string(e.PCM) != string([]byte{1, 2}) {
				t.Fatalf("pcm = %v", e.PCM)
			}
		}},
		{name: "transcript", raw: `{"type":"response.audio_transcript.delta","delta":"hi"}`, kind: EventTranscriptDelta, check: func(t *testing.T, e Event) {
			if e.Text != "hi" || e.TextSource != "audio" {
				t.Fatalf("event = %+v", e)
			}
		}},
		{name: "tool", raw: `{"type":"response.function_call_arguments.done","response_id":"r1","call_id":"c1","name":"clock","arguments":"{}"}`, kind: EventToolCall, check: func(t *testing.T, e Event) {
			if e.ResponseID != "r1" || e.CallID != "c1" || e.Name != "clock" {
				t.Fatalf("event = %+v", e)
			}
		}},
		{name: "done", raw: `{"type":"response.done","response":{"id":"r1","status":"completed","usage":{"total_tokens":3,"input_tokens":1,"output_tokens":2}}}`, kind: EventResponseDone, check: func(t *testing.T, e Event) {
			if e.Usage.TotalTokens != 3 {
				t.Fatalf("usage = %+v", e.Usage)
			}
		}},
		{name: "error", raw: `{"type":"error","error":{"message":"boom"}}`, kind: EventError, check: func(t *testing.T, e Event) {
			if e.Error == nil || e.Error.Error() != "boom" {
				t.Fatalf("error = %v", e.Error)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire rtclient.Event
			if err := json.Unmarshal([]byte(tt.raw), &wire); err != nil {
				t.Fatal(err)
			}
			event, ok := translateQwenEvent(wire)
			if !ok || event.Kind != tt.kind {
				t.Fatalf("event = %+v, ok=%t", event, ok)
			}
			if tt.check != nil {
				tt.check(t, event)
			}
		})
	}
}

func TestForwardQwenEventsDrainsBufferedFinalEvent(t *testing.T) {
	raw := make(chan rtclient.Event, 1)
	var final rtclient.Event
	if err := json.Unmarshal([]byte(`{"type":"response.done","response":{"id":"r1","status":"completed"}}`), &final); err != nil {
		t.Fatal(err)
	}
	raw <- final
	close(raw)

	out := make(chan Event, 1)
	forwardQwenEvents(raw, make(chan struct{}), out)
	event, ok := <-out
	if !ok || event.Kind != EventResponseDone || event.ResponseID != "r1" {
		t.Fatalf("event = %+v, ok=%t", event, ok)
	}
	if _, ok := <-out; ok {
		t.Fatal("translated event stream stayed open")
	}
}

func TestForwardQwenEventsStopsWhenSessionCloses(t *testing.T) {
	raw := make(chan rtclient.Event, 1)
	var speech rtclient.Event
	if err := json.Unmarshal([]byte(`{"type":"input_audio_buffer.speech_started"}`), &speech); err != nil {
		t.Fatal(err)
	}
	raw <- speech

	out := make(chan Event, 1)
	out <- Event{Kind: EventReady}
	stop := make(chan struct{})
	close(stop)
	forwardQwenEvents(raw, stop, out)
	if len(out) != 1 {
		t.Fatalf("buffered events = %d, want only the pre-existing event", len(out))
	}
}

func TestQwenResponseDoneFailureIsError(t *testing.T) {
	for _, status := range []string{"failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			var wire rtclient.Event
			if err := json.Unmarshal([]byte(`{"type":"response.done","response":{"id":"resp_1","status":"`+status+`"}}`), &wire); err != nil {
				t.Fatal(err)
			}
			event, ok := translateQwenEvent(wire)
			if !ok || event.Kind != EventError || event.Error == nil {
				t.Fatalf("event = %+v, ok=%t; want provider failure", event, ok)
			}
			if !strings.Contains(event.Error.Error(), status) {
				t.Fatalf("error = %v, want status %q", event.Error, status)
			}
		})
	}
}
