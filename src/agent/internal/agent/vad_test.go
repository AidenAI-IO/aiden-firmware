package agent

import "testing"

func TestAudioVADEndsUtteranceAfterBiasedSilence(t *testing.T) {
	vad := NewAudioVAD(1000, 50, 90, 60, false)

	speech := alternatingFrame(vad.FrameSamples(), 700, 1000)
	silence := constantFrame(vad.FrameSamples(), 700)

	if utterance := vad.Process(speech); utterance != nil {
		t.Fatal("unexpected utterance after first speech frame")
	}
	if utterance := vad.Process(speech); utterance != nil {
		t.Fatal("unexpected utterance before trailing silence")
	}
	if utterance := vad.Process(silence); utterance != nil {
		t.Fatal("unexpected utterance before silence limit")
	}
	if utterance := vad.Process(silence); utterance != nil {
		t.Fatal("unexpected utterance before silence limit")
	}
	utterance := vad.Process(silence)
	if len(utterance) != vad.FrameSamples()*5 {
		t.Fatalf("utterance samples = %d, want %d", len(utterance), vad.FrameSamples()*5)
	}
}

func TestAudioVADDetectsLowEnergySpeechWithConfiguredThresholdTooHigh(t *testing.T) {
	vad := NewAudioVAD(1000, 500, 90, 60, false)

	speech := alternatingFrame(vad.FrameSamples(), 700, 150)
	silence := constantFrame(vad.FrameSamples(), 700)

	if utterance := vad.Process(speech); utterance != nil {
		t.Fatal("unexpected utterance after first low-energy speech frame")
	}
	if utterance := vad.Process(speech); utterance != nil {
		t.Fatal("unexpected utterance before trailing silence")
	}
	if state := vad.DebugState(); !state.Speaking || state.SpeechFrames != 2 {
		t.Fatalf("low-energy speech was not detected: %#v", state)
	}
	vad.Process(silence)
	vad.Process(silence)
	utterance := vad.Process(silence)
	if len(utterance) != vad.FrameSamples()*5 {
		t.Fatalf("utterance samples = %d, want %d", len(utterance), vad.FrameSamples()*5)
	}
}

func TestComputeEnergyRemovesDCOffset(t *testing.T) {
	if got := computeEnergy([]int16{700, 700, 700, 700}); got != 0 {
		t.Fatalf("biased silence energy = %d, want 0", got)
	}
	if got := computeEnergy([]int16{1700, -300, 1700, -300}); got != 1000 {
		t.Fatalf("alternating speech energy = %d, want 1000", got)
	}
}

func constantFrame(samples int, value int16) []int16 {
	frame := make([]int16, samples)
	for i := range frame {
		frame[i] = value
	}
	return frame
}

func alternatingFrame(samples int, center, amplitude int16) []int16 {
	frame := make([]int16, samples)
	for i := range frame {
		if i%2 == 0 {
			frame[i] = center + amplitude
		} else {
			frame[i] = center - amplitude
		}
	}
	return frame
}
