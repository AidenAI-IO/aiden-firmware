package agent

import (
	"context"
	"strings"
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

func TestAudioVolumeDescriptionDocumentsPlaybackScope(t *testing.T) {
	desc := (&AudioVolumeTool{}).Description()
	for _, want := range []string{"Aiden playback/TTS volume", "do not use it for phone system UI volume"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
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
	ctx, _ := WithToolError(context.Background())

	out, err := tool.Call(ctx, `{"volume":101}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if out != "volume must be between 0 and 100" {
		t.Fatalf("Call() = %q", out)
	}
	if got := ToolErrorFromContext(ctx); got == nil || got.Code != CodeInvalidArguments || got.Message != out {
		t.Fatalf("ToolError = %+v, want invalid_arguments with output message", got)
	}
	if len(client.sets) != 0 {
		t.Fatalf("set calls = %v, want none", client.sets)
	}
}
