package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCommitEpisodeDoesNotReviseRecalledScreenMemory(t *testing.T) {
	ctx := context.Background()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	longTerm := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	if _, err := longTerm.AddMemory(ctx, MemoryItem{
		ID:               "mem_screen_snapshot",
		Type:             MemoryTypeScreenSnapshot,
		Status:           "active",
		Priority:         80,
		Confidence:       0.9,
		Title:            "Saved screen",
		Content:          "Tracking number QC-1234",
		EvidenceExcerpts: []string{"Tracking number QC-1234"},
		TTL:              "90d",
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}
	original, err := readMemoryMarkdown(longTerm.memoryPath("mem_screen_snapshot"))
	if err != nil {
		t.Fatalf("read original screen memory: %v", err)
	}

	plane := NewFilesystemMemoryPlane(memoryDir, DefaultMemoryExtractionConfig(), nil)
	for _, testCase := range []struct {
		id      string
		outcome TaskEpisodeOutcome
	}{
		{id: "ep_screen_success", outcome: TaskEpisodeOutcome{Success: true, VerifierReason: "answer accepted"}},
		{id: "ep_screen_failure", outcome: TaskEpisodeOutcome{Success: false, FailureReason: "answer rejected"}},
	} {
		if err := plane.CommitEpisode(ctx, TaskEpisode{
			ID:                  testCase.id,
			Status:              "active",
			StartedAt:           "2026-08-20T00:00:00Z",
			EndedAt:             "2026-08-20T00:00:01Z",
			UserGoal:            "Recall the saved tracking number",
			RetrievedMemoryRefs: []string{"mem_screen_snapshot"},
			Outcome:             testCase.outcome,
			Events: []TaskEpisodeEvent{
				{EventID: "evt_recall", Type: runEventToolCall, ToolName: "recall_memory"},
			},
		}); err != nil {
			t.Fatalf("CommitEpisode(%s) error = %v", testCase.id, err)
		}
	}

	updated, err := readMemoryMarkdown(longTerm.memoryPath("mem_screen_snapshot"))
	if err != nil {
		t.Fatalf("read updated screen memory: %v", err)
	}
	if updated.Item.Status != original.Item.Status ||
		updated.Item.Confidence != original.Item.Confidence ||
		updated.Item.SuccessCount != original.Item.SuccessCount ||
		updated.Item.FailureCount != original.Item.FailureCount ||
		updated.Item.ExpiresAt != original.Item.ExpiresAt {
		t.Fatalf("episode outcome revised a screen observation: before=%#v after=%#v", original.Item, updated.Item)
	}
}

func TestScreenSnapshotIsNotProfileRelevant(t *testing.T) {
	if isProfileRelevantType(MemoryTypeScreenSnapshot) {
		t.Fatal("screen_snapshot is profile-relevant, want excluded from the User Profile")
	}
}
