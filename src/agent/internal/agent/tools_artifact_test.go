package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"

	langtools "github.com/tmc/langchaingo/tools"
)

func TestArtifactReadToolReturnsBoundedPage(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("abcdefghij"), contextmanager.ArtifactMetadata{
		ToolName:   "shell",
		ToolCallID: "call_1",
	})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	tool := NewArtifactReadTool(func() *contextmanager.ContextManager { return manager })
	output, err := tool.Call(context.Background(), `{"ref":"`+stored.Ref+`","offset":2,"limit":4}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var payload struct {
		Content    string `json:"content"`
		Offset     int64  `json:"offset"`
		NextOffset int64  `json:"next_offset"`
		Complete   bool   `json:"complete"`
		SHA256     string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if payload.Content != "cdef" || payload.Offset != 2 || payload.NextOffset != 6 || payload.Complete {
		t.Fatalf("Call() payload = %#v", payload)
	}
	if payload.SHA256 == "" {
		t.Fatal("Call() sha256 is empty")
	}
}

func TestArtifactReadToolSearchesLiteralText(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("alpha\nneedle failure detail\nomega"), contextmanager.ArtifactMetadata{})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	tool := NewArtifactReadTool(func() *contextmanager.ContextManager { return manager })
	output, err := tool.Call(context.Background(), `{"ref":"`+stored.Ref+`","query":"failure","limit":24}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var payload struct {
		Content string `json:"content"`
		Found   bool   `json:"found"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if !payload.Found || !strings.Contains(payload.Content, "failure") {
		t.Fatalf("Call() payload = %#v", payload)
	}
}

func TestArtifactReadToolSerializedResultStaysWithinHardLimit(t *testing.T) {
	manager, err := contextmanager.NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte(strings.Repeat("\"\n", contextmanager.ArtifactReadMaxBytes/2)), contextmanager.ArtifactMetadata{})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	tool := NewArtifactReadTool(func() *contextmanager.ContextManager { return manager })
	output, err := tool.Call(context.Background(), `{"ref":"`+stored.Ref+`","limit":16384}`)
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(output) > contextmanager.ArtifactReadMaxBytes {
		t.Fatalf("Call() output bytes = %d, want <= %d", len(output), contextmanager.ArtifactReadMaxBytes)
	}
	var payload struct {
		Content    string `json:"content"`
		NextOffset int64  `json:"next_offset"`
		Complete   bool   `json:"complete"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if payload.Content == "" || payload.NextOffset <= 0 || payload.Complete {
		t.Fatalf("Call() payload = %#v, want bounded continuation", payload)
	}
}

func TestRuntimeRegistersArtifactReadTool(t *testing.T) {
	toolSet := &ToolSet{tools: map[string]langtools.Tool{}}
	runtime := NewRuntimeWithDeps(
		Config{ConfigDir: t.TempDir()},
		nil,
		nil,
		toolSet,
		NewSkillIndex(),
	)
	if runtime == nil {
		t.Fatal("NewRuntimeWithDeps() returned nil")
	}
	registered, ok := toolSet.Get("artifact_read")
	if !ok {
		t.Fatal("artifact_read tool is not registered")
	}
	artifactTool, ok := registered.(*ArtifactReadTool)
	if !ok || artifactTool.managerFn == nil {
		t.Fatalf("artifact_read tool = %T, want manager-aware ArtifactReadTool", registered)
	}
}
