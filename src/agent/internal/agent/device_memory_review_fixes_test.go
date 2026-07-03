package agent

import (
	"context"
	"testing"

	"github.com/tmc/langchaingo/schema"
)

// TestEpisodeRecorderFinishRespectsVerifierRejection ensures a clean run whose
// final verifier decision is can_finish=false is not stored as a success.
func TestEpisodeRecorderFinishRespectsVerifierRejection(t *testing.T) {
	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{Input: "测试任务"}, MemoryContext{})
	canFinish := false
	recorder.append(TaskEpisodeEvent{
		Type:        "verifier_decision",
		Role:        "agent",
		CanFinish:   &canFinish,
		NeedsReplan: true,
		Reason:      "completion criteria not met",
	})

	episode := recorder.Finish("partial answer", nil, nil, nil, nil)
	if episode.Outcome.Success {
		t.Fatalf("episode should not be successful when last verifier decision is can_finish=false: %#v", episode.Outcome)
	}
	if episode.Outcome.VerifierReason != "completion criteria not met" {
		t.Errorf("verifier reason = %q", episode.Outcome.VerifierReason)
	}
}

// TestEpisodeRecorderFinishAcceptsVerifierApproval ensures a clean run approved
// by the verifier is stored as success with the verifier's final answer.
func TestEpisodeRecorderFinishAcceptsVerifierApproval(t *testing.T) {
	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{Input: "测试任务"}, MemoryContext{})
	canFinish := true
	recorder.append(TaskEpisodeEvent{
		Type:      "verifier_decision",
		Role:      "agent",
		CanFinish: &canFinish,
		Content:   "done",
		Reason:    "all criteria satisfied",
	})

	episode := recorder.Finish("candidate", nil, nil, nil, nil)
	if !episode.Outcome.Success {
		t.Fatalf("episode should be successful when verifier approves: %#v", episode.Outcome)
	}
	if episode.Outcome.FinalAnswer != "done" {
		t.Errorf("final answer = %q, want verifier final answer", episode.Outcome.FinalAnswer)
	}
}

func TestEpisodeRecorderPersistsToolContent(t *testing.T) {
	action := schema.AgentAction{
		Tool:      "echo",
		ToolInput: "hello",
		Log:       formatToolActionLog("echo", `{"input":"hello","speech":"Saying hi."}`, "I will echo.", "\n"),
	}

	recorder := NewEpisodeRecorder(MemoryRetrieveRequest{Input: "test"}, MemoryContext{})
	recorder.RecordExecution(roleExecutionResult{Action: &action})
	episode := recorder.Finish("", nil, nil, nil, nil)
	if got := firstToolCallEventContent(episode.Events); got != "I will echo." {
		t.Fatalf("tool event content = %q", got)
	}
}

func firstToolCallEventContent(events []TaskEpisodeEvent) string {
	for _, event := range events {
		if event.Type == runEventToolCall {
			return event.Content
		}
	}
	return ""
}

// TestDeviceMemoryConflictTypeFiltering ensures conflicted records are matched
// by their effective "conflict" type rather than their stored type.
func TestDeviceMemoryConflictTypeFiltering(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:       "proc_conflicted",
		Type:     "procedure",
		Status:   "conflicted",
		Title:    "Conflicted procedure",
		DeviceID: "default",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Querying by the stored type must NOT return a conflicted record.
	procHits, err := store.Search(ctx, DeviceMemoryQuery{Types: []string{"procedure"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search(procedure): %v", err)
	}
	for _, hit := range procHits {
		if hit.ID == "proc_conflicted" {
			t.Fatalf("conflicted record should not match Types=[procedure]: %#v", procHits)
		}
	}

	// Querying by "conflict" must return it, with Type remapped to "conflict".
	conflictHits, err := store.Search(ctx, DeviceMemoryQuery{Types: []string{"conflict"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search(conflict): %v", err)
	}
	if len(conflictHits) != 1 || conflictHits[0].ID != "proc_conflicted" || conflictHits[0].Type != "conflict" {
		t.Fatalf("Types=[conflict] should return remapped conflicted record, got %#v", conflictHits)
	}
}

// TestDeviceMemoryUpdateRemovesStaleTypeFile ensures changing an item's type via
// Update does not leave the old type-specific YAML behind (duplicate ID).
func TestDeviceMemoryUpdateRemovesStaleTypeFile(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(t.TempDir())
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:       "mem_retype",
		Type:     "procedure",
		Status:   "active",
		Title:    "To be retyped",
		DeviceID: "default",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := store.Update(ctx, "mem_retype", func(item *DeviceMemoryItem) {
		item.Type = "failure"
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// readAll (via Search with no filter) must return exactly one record.
	hits, err := store.Search(ctx, DeviceMemoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	count := 0
	for _, hit := range hits {
		if hit.ID == "mem_retype" {
			count++
			if hit.Type != "failure" {
				t.Errorf("retyped record type = %q, want failure", hit.Type)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one record for mem_retype after retype, got %d (hits=%#v)", count, hits)
	}
}
