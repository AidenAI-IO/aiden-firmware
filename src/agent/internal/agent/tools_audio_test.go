package agent

import (
	"context"
	"testing"
)

type fakePlaybackVolumeClient struct {
	volume int
	sets   []int
	getErr error
	setErr error
}

func (f *fakePlaybackVolumeClient) SetPlaybackVolume(volume int) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.volume = volume
	f.sets = append(f.sets, volume)
	return nil
}

func (f *fakePlaybackVolumeClient) GetPlaybackVolume() (int, error) {
	if f.getErr != nil {
		return 0, f.getErr
	}
	return f.volume, nil
}

func TestAudioVolumeToolGet(t *testing.T) {
	tool := &AudioVolumeTool{client: &fakePlaybackVolumeClient{volume: 55}}

	out, err := tool.Call(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != `{"volume":55}` {
		t.Fatalf("Call() = %q, want current volume JSON", out)
	}
}

func TestAudioVolumeToolSet(t *testing.T) {
	client := &fakePlaybackVolumeClient{volume: 55}
	tool := &AudioVolumeTool{client: client}

	out, err := tool.Call(context.Background(), `{"volume":70}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(client.sets) != 1 || client.sets[0] != 70 {
		t.Fatalf("set calls = %v, want [70]", client.sets)
	}
	if out != `{"volume":70}` {
		t.Fatalf("Call() = %q, want updated volume JSON", out)
	}
}

func TestAudioVolumeToolRejectsOutOfRange(t *testing.T) {
	client := &fakePlaybackVolumeClient{volume: 55}
	tool := &AudioVolumeTool{client: client}

	out, err := tool.Call(context.Background(), `{"volume":101}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != "error: volume must be between 0 and 100" {
		t.Fatalf("Call() = %q", out)
	}
	if len(client.sets) != 0 {
		t.Fatalf("set calls = %v, want none", client.sets)
	}
}
