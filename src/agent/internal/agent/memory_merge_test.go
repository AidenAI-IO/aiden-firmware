package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestMemoryMergeEngineRunsSearchModelAndApplyInOrder(t *testing.T) {
	model := &episodeMemoryScriptedModel{responses: []string{`{"actions":[{"action":"ignore","scope":"temporary"}]}`}}
	engine := NewMemoryMergeEngine(model)
	var searched bool
	var applied bool
	err := engine.Merge(context.Background(), MemoryMergeRequest{
		Search: func(context.Context) ([]MemoryMergeReference, error) {
			searched = true
			return []MemoryMergeReference{{Scope: "temporary", ID: "tmp_1", Content: "old"}}, nil
		},
		BuildMessages: func(refs []MemoryMergeReference) ([]llms.MessageContent, error) {
			if len(refs) != 1 || refs[0].ID != "tmp_1" {
				t.Fatalf("references=%#v", refs)
			}
			return []llms.MessageContent{{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart("raw + " + refs[0].Content)}}}, nil
		},
		Apply: func(_ context.Context, raw string, refs []MemoryMergeReference) error {
			applied = true
			if !strings.Contains(raw, `"actions"`) || len(refs) != 1 {
				t.Fatalf("raw=%q refs=%#v", raw, refs)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !searched || !applied {
		t.Fatalf("searched=%v applied=%v", searched, applied)
	}
}

func TestMemoryRunGateZeroValueSerializesCalls(t *testing.T) {
	var gate MemoryRunGate
	if err := gate.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		if err := gate.acquire(context.Background()); err == nil {
			close(acquired)
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second call acquired gate before first release")
	case <-time.After(20 * time.Millisecond):
	}
	gate.release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second call did not acquire gate after release")
	}
	gate.release()
}

func TestMemoryRunGateWaitCanBeCanceled(t *testing.T) {
	gate := NewMemoryRunGate()
	if err := gate.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.acquire(ctx); err != context.Canceled {
		t.Fatalf("acquire() error=%v, want context canceled", err)
	}
	gate.release()
}
