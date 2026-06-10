package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollingSummaryAccumulatesArchivedChunks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	session := NewSessionMemoryStore(filepath.Join(root, "session"), 2)

	for i := 0; i < 4; i++ {
		_, err := session.AppendEvent(ctx, SessionEvent{
			Type:    "user_input",
			Role:    "user",
			Content: "test event",
		})
		if err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
		_, err = session.Compress(ctx, CompressOption{
			Summary: "chunk summary " + string(rune('A'+i)),
			ChunkID: "chunk_00" + string(rune('0'+i)),
		})
		if err != nil {
			t.Fatalf("Compress() error = %v", err)
		}
	}

	summaryPath := filepath.Join(root, "session", "summary.md")
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("ReadFile(summary.md) error = %v", err)
	}
	summary := string(summaryBytes)

	if !strings.Contains(summary, "## Rolling Summary") {
		t.Errorf("summary.md missing Rolling Summary section")
	}
	if !strings.Contains(summary, "chunk_000") || !strings.Contains(summary, "chunk_001") {
		t.Errorf("rolling summary should mention archived chunks 000 and 001")
	}
	if !strings.Contains(summary, "## Recent Chunks") {
		t.Errorf("summary.md missing Recent Chunks section")
	}
	if !strings.Contains(summary, "chunk_002") && !strings.Contains(summary, "chunk_003") {
		t.Errorf("recent chunks should show chunk_002 and chunk_003")
	}
}
