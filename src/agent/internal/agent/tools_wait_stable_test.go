package agent

import "testing"

func TestScreenStableDefaultsResolved(t *testing.T) {
	t.Parallel()

	defaults := ScreenStableDefaults{}.Resolved()
	if defaults.TimeoutMs != defaultStableWaitTimeoutMs {
		t.Fatalf("TimeoutMs = %d, want %d", defaults.TimeoutMs, defaultStableWaitTimeoutMs)
	}
	if defaults.StableMs != defaultStableDurationMs {
		t.Fatalf("StableMs = %d, want %d", defaults.StableMs, defaultStableDurationMs)
	}

	custom := ScreenStableDefaults{TimeoutMs: 8000, StableMs: 9000}.Resolved()
	if custom.TimeoutMs != 8000 {
		t.Fatalf("TimeoutMs = %d, want 8000", custom.TimeoutMs)
	}
	if custom.StableMs != 8000 {
		t.Fatalf("StableMs = %d, want clamped to 8000", custom.StableMs)
	}
}

func TestScreenStableDefaultsInputJSON(t *testing.T) {
	t.Parallel()

	got := ScreenStableDefaults{TimeoutMs: 4500, StableMs: 600}.InputJSON()
	want := `{"timeout_ms":4500,"stable_ms":600,"diff_threshold":5}`
	if got != want {
		t.Fatalf("InputJSON() = %q, want %q", got, want)
	}
}
