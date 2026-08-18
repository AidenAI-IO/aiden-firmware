package agent

import (
	"testing"
)

func TestQuickCaptureToneKindsAreDistinct(t *testing.T) {
	// Splitting the const block must not have collided the iota values with the
	// pre-existing kinds.
	kinds := map[promptSoundKind]string{
		promptSoundRecordingStart:        "recording_start",
		promptSoundAgentSend:             "agent_send",
		promptSoundQuickCaptureThreshold: "quick_capture_threshold",
		promptSoundQuickCaptureSuccess:   "quick_capture_success",
		promptSoundQuickCaptureFailed:    "quick_capture_failed",
	}
	if len(kinds) != 5 {
		t.Fatalf("prompt sound kinds collided: %v", kinds)
	}
	if promptSoundRecordingStart != 0 || promptSoundAgentSend != 1 {
		t.Fatalf("pre-existing kinds shifted: recording_start=%d agent_send=%d, want 0 and 1",
			promptSoundRecordingStart, promptSoundAgentSend)
	}
}

func TestQuickCaptureTonesAreAudibleAndDistinct(t *testing.T) {
	threshold := promptSoundPCM(promptSoundQuickCaptureThreshold)
	success := promptSoundPCM(promptSoundQuickCaptureSuccess)
	failed := promptSoundPCM(promptSoundQuickCaptureFailed)

	for name, pcm := range map[string][]byte{
		"threshold": threshold,
		"success":   success,
		"failed":    failed,
	} {
		if len(pcm) == 0 {
			t.Fatalf("%s tone synthesized no audio", name)
		}
		if promptSoundDurationMS(promptSoundQuickCaptureThreshold) <= 0 {
			t.Fatalf("%s tone reports zero duration", name)
		}
	}

	// Success and failure must be tellable apart by ear, so they must not be
	// byte-identical.
	if string(success) == string(failed) {
		t.Fatal("success and failure tones are identical; the user cannot tell whether the capture worked")
	}
	if string(threshold) == string(success) {
		t.Fatal("threshold and success tones are identical; the user cannot tell when to release")
	}
}

func TestPlayPromptSoundWithNilClientIsNoop(t *testing.T) {
	// Tone playback is fire-and-forget. A nil audio client (or, on device, a
	// rejected playback session while TTS is speaking) must never fail the
	// capture that requested it.
	for _, kind := range []promptSoundKind{
		promptSoundQuickCaptureThreshold,
		promptSoundQuickCaptureSuccess,
		promptSoundQuickCaptureFailed,
	} {
		if err := playPromptSound(nil, nil, kind, false); err != nil {
			t.Fatalf("playPromptSound(nil client, kind=%d) error = %v, want nil", kind, err)
		}
	}
}
