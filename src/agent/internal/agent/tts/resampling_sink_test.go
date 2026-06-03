package tts

import (
	"context"
	"testing"
)

func TestResamplingSinkSkipsEmptyResamplerOutput(t *testing.T) {
	target := &countingAudioSink{format: AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}}
	sink := NewResamplingSink(AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}, target)

	if err := sink.WritePCM([]byte{1}); err != nil {
		t.Fatalf("WritePCM() error = %v", err)
	}
	if target.writes != 0 {
		t.Fatalf("target WritePCM calls = %d, want 0", target.writes)
	}
}

type countingAudioSink struct {
	format AudioFormat
	writes int
}

func (s *countingAudioSink) Format() AudioFormat { return s.format }

func (s *countingAudioSink) WritePCM([]byte) error {
	s.writes++
	return nil
}

func (s *countingAudioSink) Drain(context.Context) error { return nil }

func (s *countingAudioSink) Stop() error { return nil }
