package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"aiden-agent/internal/agent/tts"
)

func TestLocalAudioPlaybackBackendWritesWAVAndCleansUp(t *testing.T) {
	oldLookPath := localAudioLookPath
	oldCommandContext := localAudioCommandContext
	defer func() {
		localAudioLookPath = oldLookPath
		localAudioCommandContext = oldCommandContext
	}()

	localAudioLookPath = func(name string) (string, error) {
		return name, nil
	}
	var playedPath string
	localAudioCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) == 0 {
			t.Fatalf("local player %q received no wav path", name)
		}
		playedPath = args[len(args)-1]
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestLocalAudioPlaybackHelper")
		cmd.Env = append(os.Environ(), "AIDEN_LOCAL_AUDIO_HELPER=1")
		return cmd
	}

	backend := newLocalAudioPlaybackBackend(nil)
	format := tts.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16}
	sessionID, err := backend.StartPlayback(format)
	if err != nil {
		t.Fatalf("StartPlayback() error = %v", err)
	}
	pcm := []byte{1, 2, 3, 4}
	if err := backend.WritePlayChunk(sessionID, pcm, false); err != nil {
		t.Fatalf("WritePlayChunk(pcm) error = %v", err)
	}
	if err := backend.WritePlayChunk(sessionID, nil, true); err != nil {
		t.Fatalf("WritePlayChunk(final) error = %v", err)
	}
	if playedPath == "" {
		t.Fatal("local player was not invoked")
	}
	wav, err := os.ReadFile(playedPath)
	if err != nil {
		t.Fatalf("read generated wav: %v", err)
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatalf("invalid wav header: %q %q %q", wav[0:4], wav[8:12], wav[36:40])
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(pcm)) {
		t.Fatalf("data size = %d, want %d", got, len(pcm))
	}
	if got := wav[44:]; string(got) != string(pcm) {
		t.Fatalf("pcm payload = %#v, want %#v", got, pcm)
	}

	if err := backend.StopPlayback(sessionID); err != nil {
		t.Fatalf("StopPlayback() error = %v", err)
	}
	if _, err := os.Stat(playedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated wav still exists or stat failed differently: %v", err)
	}
}

func TestLocalAudioPlaybackBackendReportsMissingPlayer(t *testing.T) {
	oldLookPath := localAudioLookPath
	defer func() { localAudioLookPath = oldLookPath }()
	localAudioLookPath = func(name string) (string, error) {
		return "", exec.ErrNotFound
	}

	backend := newLocalAudioPlaybackBackend(nil)
	_, err := backend.StartPlayback(tts.AudioFormat{SampleRate: 16000, Channels: 1, BitWidth: 16})
	if err == nil {
		t.Fatal("StartPlayback() error = nil, want missing local player")
	}
}

func TestLocalAudioPlaybackHelper(t *testing.T) {
	if os.Getenv("AIDEN_LOCAL_AUDIO_HELPER") != "1" {
		return
	}
	time.Sleep(5 * time.Second)
	os.Exit(0)
}
