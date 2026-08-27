package agent

import (
	"context"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestMemoryMergeEngineRunsSearchAndModelInOrder(t *testing.T) {
	model := &episodeMemoryScriptedModel{responses: []string{`{"actions":[{"action":"ignore","scope":"temporary"}]}`}}
	engine := NewMemoryMergeEngine(model)
	var searched bool
	refs, raw, err := engine.Extract(context.Background(), MemoryMergeRequest{
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if !searched || len(refs) != 1 || refs[0].ID != "tmp_1" || raw == "" {
		t.Fatalf("searched=%v refs=%#v raw=%q", searched, refs, raw)
	}
}
