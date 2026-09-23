package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLogVADHelperStderrPreservesStructuredLines(t *testing.T) {
	structured := "2026-08-05T06:22:03Z [ERROR][rknn_vad][inference] run_failed error=timeout"
	var output bytes.Buffer
	logVADHelperStderrTo("rknn", strings.NewReader(structured+"\nraw diagnostic\n"), &output)

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want 2: %q", len(lines), output.String())
	}
	if lines[0] != structured {
		t.Fatalf("structured line = %q, want unchanged %q", lines[0], structured)
	}
	wrappedPattern := regexp.MustCompile(
		`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z \[INFO\]\[agent\]\[vad_rknn\] helper_stderr message="raw diagnostic"$`,
	)
	if !wrappedPattern.MatchString(lines[1]) {
		t.Fatalf("wrapped line = %q, want complete structured helper_stderr record", lines[1])
	}
}

func TestAudioVADEndsUtteranceFromRKNNProbabilities(t *testing.T) {
	vad := newTestAudioVAD(t, []float64{0.9, 0.8, 0.1, 0.1, 0.1})

	speech := constantFrame(vad.FrameSamples(), 1000)
	silence := constantFrame(vad.FrameSamples(), 0)

	if utterance, err := vad.Process(speech); err != nil || utterance != nil {
		t.Fatalf("first speech Process() = len %d, err %v; want no utterance", len(utterance), err)
	}
	if utterance, err := vad.Process(speech); err != nil || utterance != nil {
		t.Fatalf("second speech Process() = len %d, err %v; want no utterance", len(utterance), err)
	}
	for i := 0; i < 2; i++ {
		if utterance, err := vad.Process(silence); err != nil || utterance != nil {
			t.Fatalf("silence Process(%d) = len %d, err %v; want no utterance", i, len(utterance), err)
		}
	}
	utterance, err := vad.Process(silence)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(utterance) != vad.FrameSamples()*5 {
		t.Fatalf("utterance samples = %d, want %d", len(utterance), vad.FrameSamples()*5)
	}
}

func TestAudioVADDoesNotUsePCMEnergyAsSpeech(t *testing.T) {
	vad := newTestAudioVAD(t, []float64{0.1, 0.1, 0.1, 0.1})
	loudFrame := alternatingFrame(vad.FrameSamples(), 0, 30000)

	for i := 0; i < 4; i++ {
		if utterance, err := vad.Process(loudFrame); err != nil || utterance != nil {
			t.Fatalf("Process(%d) = len %d, err %v; want no utterance from PCM energy", i, len(utterance), err)
		}
	}
	if state := vad.DebugState(); state.Speaking || state.SpeechFrames != 0 {
		t.Fatalf("VAD treated loud PCM as speech without RKNN probability: %#v", state)
	}
}

func TestAudioVADRequiresSileroFrameShape(t *testing.T) {
	_, err := NewAudioVADWithScorer(AudioVADConfig{
		SampleRate:  32000,
		SilenceMs:   90,
		MinSpeechMs: 60,
	}, &sequenceScorer{})
	if err == nil {
		t.Fatal("NewAudioVADWithScorer() error = nil, want sample-rate validation error")
	}

	vad := newTestAudioVAD(t, []float64{0.9})
	if _, err := vad.Process(make([]int16, vad.FrameSamples()-1)); err == nil {
		t.Fatal("Process() error = nil, want frame-size validation error")
	}
}

func TestNewAudioVADSelectsHelperFromBackend(t *testing.T) {
	vad, err := NewAudioVAD(AudioVADConfig{Backend: "cpu", HelperPath: defaultVADHelperPath})
	if err != nil {
		t.Fatalf("NewAudioVAD(cpu) error = %v", err)
	}
	scorer, ok := vad.scorer.(*helperVADScorer)
	if !ok {
		t.Fatalf("scorer type = %T, want *helperVADScorer", vad.scorer)
	}
	if scorer.backend != "cpu" {
		t.Fatalf("backend = %q, want cpu", scorer.backend)
	}
	wantCPUHelper := defaultCPUVADHelperPath
	if _, err := os.Stat(wantCPUHelper); err != nil {
		wantCPUHelper = legacyCPUVADHelperPath
	}
	if scorer.helperPath != wantCPUHelper {
		t.Fatalf("helperPath = %q, want %q", scorer.helperPath, wantCPUHelper)
	}

	defaultVAD, err := NewAudioVAD(AudioVADConfig{})
	if err != nil {
		t.Fatalf("NewAudioVAD(default) error = %v", err)
	}
	defaultScorer, ok := defaultVAD.scorer.(*helperVADScorer)
	if !ok {
		t.Fatalf("default scorer type = %T, want *helperVADScorer", defaultVAD.scorer)
	}
	if defaultScorer.backend != "rknn" {
		t.Fatalf("default backend = %q, want rknn", defaultScorer.backend)
	}
	wantRKNNHelper := defaultVADHelperPath
	if _, err := os.Stat(wantRKNNHelper); err != nil {
		wantRKNNHelper = legacyVADHelperPath
	}
	if defaultScorer.helperPath != wantRKNNHelper {
		t.Fatalf("default helperPath = %q, want %q", defaultScorer.helperPath, wantRKNNHelper)
	}
}

func TestResolveBuiltinPathUsesLegacyInstallWhenPrimaryIsMissing(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "usr", "helper")
	legacy := filepath.Join(dir, "oem", "helper")
	if err := os.MkdirAll(filepath.Dir(legacy), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBuiltinPath(primary, legacy); got != legacy {
		t.Fatalf("resolveBuiltinPath() = %q, want legacy %q", got, legacy)
	}
	if err := os.MkdirAll(filepath.Dir(primary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(primary, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := resolveBuiltinPath(primary, legacy); got != primary {
		t.Fatalf("resolveBuiltinPath() = %q, want primary %q", got, primary)
	}
}

func TestAudioVADPropagatesRKNNScorerErrors(t *testing.T) {
	wantErr := errors.New("rknn failed")
	vad, err := NewAudioVADWithScorer(AudioVADConfig{
		SampleRate:  16000,
		SilenceMs:   90,
		MinSpeechMs: 60,
	}, &sequenceScorer{err: wantErr})
	if err != nil {
		t.Fatalf("NewAudioVADWithScorer() error = %v", err)
	}

	if _, err := vad.Process(constantFrame(vad.FrameSamples(), 0)); !errors.Is(err, wantErr) {
		t.Fatalf("Process() error = %v, want %v", err, wantErr)
	}
	if state := vad.DebugState(); state.LastError != wantErr.Error() {
		t.Fatalf("LastError = %q, want %q", state.LastError, wantErr.Error())
	}
}

func TestHelperVADScorerStopsAfterMalformedScoreResponse(t *testing.T) {
	helperPath := filepath.Join(t.TempDir(), "vad-helper")
	script := "#!/bin/sh\n" +
		"printf 'READY\\n'\n" +
		"dd bs=1 count=1 >/dev/null 2>/dev/null\n" +
		"printf 'BOGUS\\n'\n" +
		"sleep 30\n"
	if err := os.WriteFile(helperPath, []byte(script), 0755); err != nil {
		t.Fatalf("write helper script: %v", err)
	}

	scorer := newHelperVADScorer("cpu", "", helperPath)
	if _, err := scorer.Score(make([]int16, sileroVADFrameSamples)); err == nil || !strings.Contains(err.Error(), "unexpected VAD helper response") {
		t.Fatalf("Score() error = %v, want unexpected response", err)
	}

	scorer.mu.Lock()
	helperStillRunning := scorer.cmd != nil
	scorer.mu.Unlock()
	if helperStillRunning {
		t.Fatal("helper process was kept after malformed response")
	}
}

func TestHelperVADScorerFallsBackToCPUWhenRKNNEncoderIsUnavailable(t *testing.T) {
	rknnPath := filepath.Join(t.TempDir(), "rknn-vad")
	rknnScript := "#!/bin/sh\n" +
		"printf 'ERR encoder rknn_init failed: -1\\n'\n" +
		"sleep 30\n"
	if err := os.WriteFile(rknnPath, []byte(rknnScript), 0755); err != nil {
		t.Fatalf("write RKNN helper script: %v", err)
	}
	cpuPath := filepath.Join(t.TempDir(), "cpu-vad")
	cpuScript := "#!/bin/sh\n" +
		"printf 'READY\\n'\n" +
		"dd bs=1025 count=1 >/dev/null 2>/dev/null\n" +
		"printf 'P 0.25\\n'\n" +
		"sleep 30\n"
	if err := os.WriteFile(cpuPath, []byte(cpuScript), 0755); err != nil {
		t.Fatalf("write CPU helper script: %v", err)
	}

	scorer := newHelperVADScorer("rknn", "model.rknn", rknnPath)
	scorer.fallback = newHelperVADScorer("cpu", "", cpuPath)
	probability, err := scorer.Score(make([]int16, sileroVADFrameSamples))
	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if probability != 0.25 {
		t.Fatalf("probability = %v, want 0.25", probability)
	}
	if !scorer.usingFallback {
		t.Fatal("RKNN scorer did not switch to CPU fallback")
	}
	if err := scorer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func newTestAudioVAD(t *testing.T, probabilities []float64) *AudioVAD {
	t.Helper()
	vad, err := NewAudioVADWithScorer(AudioVADConfig{
		SampleRate:      16000,
		SilenceMs:       90,
		MinSpeechMs:     60,
		SpeechThreshold: 0.5,
	}, &sequenceScorer{probabilities: probabilities})
	if err != nil {
		t.Fatalf("NewAudioVADWithScorer() error = %v", err)
	}
	return vad
}

type sequenceScorer struct {
	probabilities []float64
	err           error
	resetErr      error
	resetCalls    int
	index         int
}

func (s *sequenceScorer) Score(samples []int16) (float64, error) {
	if s.err != nil {
		return 0, s.err
	}
	if len(s.probabilities) == 0 {
		return 0, nil
	}
	if s.index >= len(s.probabilities) {
		return s.probabilities[len(s.probabilities)-1], nil
	}
	probability := s.probabilities[s.index]
	s.index++
	return probability, nil
}

func (s *sequenceScorer) Reset() error {
	s.resetCalls++
	if s.resetErr != nil {
		return s.resetErr
	}
	s.index = 0
	return nil
}

func (s *sequenceScorer) Close() error {
	return nil
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
