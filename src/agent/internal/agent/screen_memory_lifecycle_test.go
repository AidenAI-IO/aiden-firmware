package agent

import (
	"testing"
	"time"
)

// Screen Memory records an observation the user deliberately captured, not an
// inference whose confidence should change with later task outcomes.
//
// These exemptions look like scattered special cases in the code, which makes
// them easy for a later reader to "fix". These tests are what stops that.

func TestScreenMemoryKeepsConfidenceOnSuccess(t *testing.T) {
	item := MemoryItem{
		Type:       MemoryTypeScreenSnapshot,
		Status:     "active",
		Confidence: 0.9,
	}
	updateLongTermMemoryFromEpisode(&item, TaskEpisode{
		ID:      "ep_1",
		Outcome: TaskEpisodeOutcome{Success: true},
	})

	if item.Confidence != 0.9 {
		t.Fatalf("confidence = %v, want 0.9 unchanged: a screen observation does not become more true when a task succeeds", item.Confidence)
	}
	if item.SuccessCount != 0 {
		t.Fatalf("success_count = %d, want 0: validation counts are meaningless for an observation", item.SuccessCount)
	}
}

func TestScreenMemoryExpiryIsNotRefreshedOnSuccess(t *testing.T) {
	// This is the exemption with real consequences. TTL is the only automatic
	// reclamation path for long-term memory, so refreshing it on every
	// successful recall would make a frequently-asked entry immortal.
	original := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	item := MemoryItem{
		Type:       MemoryTypeScreenSnapshot,
		Status:     "active",
		Confidence: 0.9,
		TTL:        "90d",
		ExpiresAt:  original,
	}
	updateLongTermMemoryFromEpisode(&item, TaskEpisode{
		ID:      "ep_1",
		Outcome: TaskEpisodeOutcome{Success: true},
	})

	if item.ExpiresAt != original {
		t.Fatalf("expires_at = %q, want %q unchanged: refreshing TTL defeats the only reclamation path", item.ExpiresAt, original)
	}
}

func TestScreenMemorySurvivesFailedEpisode(t *testing.T) {
	item := MemoryItem{
		Type:       MemoryTypeScreenSnapshot,
		Status:     "active",
		Confidence: 0.9,
	}
	updateLongTermMemoryFromEpisode(&item, TaskEpisode{
		ID:      "ep_1",
		Outcome: TaskEpisodeOutcome{Success: false},
	})

	if item.Status != "active" {
		t.Fatalf("status = %q, want active: a failed task says nothing about what was on screen", item.Status)
	}
	if item.Confidence != 0.9 {
		t.Fatalf("confidence = %v, want 0.9 unchanged", item.Confidence)
	}
	if item.FailureCount != 0 {
		t.Fatalf("failure_count = %d, want 0", item.FailureCount)
	}
}

func TestNonScreenMemoryStillGetsOutcomeFeedback(t *testing.T) {
	// The exemption must be narrow: other types keep their existing behavior.
	item := MemoryItem{
		Type:       "procedure",
		Status:     "active",
		Confidence: 0.75,
		TTL:        "30d",
		ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	}
	before := item.ExpiresAt
	updateLongTermMemoryFromEpisode(&item, TaskEpisode{
		ID:      "ep_1",
		Outcome: TaskEpisodeOutcome{Success: true},
	})

	if item.Confidence <= 0.75 {
		t.Fatalf("confidence = %v, want > 0.75: non-screen memory should still be credited", item.Confidence)
	}
	if item.SuccessCount != 1 {
		t.Fatalf("success_count = %d, want 1", item.SuccessCount)
	}
	if item.ExpiresAt == before {
		t.Fatalf("expires_at unchanged, want refreshed for non-screen memory")
	}
}

func TestNonScreenMemoryStillPenalizedOnFailure(t *testing.T) {
	item := MemoryItem{
		Type:       "procedure",
		Status:     "active",
		Confidence: 0.75,
	}
	updateLongTermMemoryFromEpisode(&item, TaskEpisode{
		ID:      "ep_1",
		Outcome: TaskEpisodeOutcome{Success: false},
	})

	if item.Confidence >= 0.75 {
		t.Fatalf("confidence = %v, want < 0.75", item.Confidence)
	}
	if item.FailureCount != 1 {
		t.Fatalf("failure_count = %d, want 1", item.FailureCount)
	}
}

func TestScreenSnapshotIsNotProfileRelevant(t *testing.T) {
	// Screen Memory must never reach the synthesized User Profile.
	if isProfileRelevantType(MemoryTypeScreenSnapshot) {
		t.Fatal("screen_snapshot is profile-relevant, want excluded from the User Profile")
	}
}

func TestScreenSnapshotIsNotPenalized(t *testing.T) {
	if shouldPenalizeMemoryType(MemoryTypeScreenSnapshot) {
		t.Fatal("screen_snapshot is in the penalize list, want exempt")
	}
}
