package main

import (
	"testing"

	"aiden-agent/internal/agent"
)

type fakeRealtimePlaybackAudio struct {
	nextSessionID uint64
	starts        int
	writes        []fakeRealtimePlaybackWrite
	stops         []uint64
}

type fakeRealtimePlaybackWrite struct {
	sessionID uint64
	data      []byte
	final     bool
}

func (f *fakeRealtimePlaybackAudio) StartPlayback(agent.AudioFormat) (*agent.PlaybackStartResult, error) {
	f.starts++
	f.nextSessionID++
	return &agent.PlaybackStartResult{SessionID: f.nextSessionID}, nil
}

func (f *fakeRealtimePlaybackAudio) WritePlayChunk(sessionID uint64, data []byte, final bool) error {
	f.writes = append(f.writes, fakeRealtimePlaybackWrite{
		sessionID: sessionID,
		data:      append([]byte(nil), data...),
		final:     final,
	})
	return nil
}

func (f *fakeRealtimePlaybackAudio) StopPlayback(sessionID uint64) error {
	f.stops = append(f.stops, sessionID)
	return nil
}

func TestRealtimePlaybackInterruptStopsDrainAndSuppressesStaleDeltas(t *testing.T) {
	audio := &fakeRealtimePlaybackAudio{}
	playback := realtimePlaybackState{}
	format := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}

	if err := playback.append(audio, format, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := playback.finalize(audio); err != nil {
		t.Fatal(err)
	}
	if err := playback.interrupt(audio); err != nil {
		t.Fatal(err)
	}

	if len(audio.stops) != 1 || audio.stops[0] != 1 {
		t.Fatalf("stopped sessions = %v, want [1]", audio.stops)
	}
	if err := playback.append(audio, format, []byte{3, 4}); err != nil {
		t.Fatal(err)
	}
	if audio.starts != 1 {
		t.Fatalf("playback starts = %d, want stale delta suppressed", audio.starts)
	}

	if err := playback.beginResponse(audio); err != nil {
		t.Fatal(err)
	}
	if err := playback.append(audio, format, []byte{5, 6}); err != nil {
		t.Fatal(err)
	}
	if audio.starts != 2 {
		t.Fatalf("playback starts = %d, want next response to start playback", audio.starts)
	}
}
