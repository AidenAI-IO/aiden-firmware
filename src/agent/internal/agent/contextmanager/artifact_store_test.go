package contextmanager

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

func TestContextManagerStoresAndReadsBoundedArtifact(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("abcdefghij"), ArtifactMetadata{
		ToolName:   "shell",
		ToolCallID: "call_1",
	})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	if stored.Ref == "" {
		t.Fatal("StoreArtifact() ref is empty")
	}

	chunk, err := manager.ReadArtifact(stored.Ref, 2, 4)
	if err != nil {
		t.Fatalf("ReadArtifact() error = %v", err)
	}
	if !bytes.Equal(chunk.Content, []byte("cdef")) {
		t.Fatalf("ReadArtifact() content = %q, want cdef", chunk.Content)
	}
	if chunk.Offset != 2 || chunk.NextOffset != 6 || chunk.Complete {
		t.Fatalf("ReadArtifact() chunk = %#v, want offset=2 next=6 complete=false", chunk)
	}
	if chunk.SHA256 == "" {
		t.Fatal("ReadArtifact() SHA256 is empty")
	}
}

func TestToolResultMetadataPersistsButIsNotSentToModel(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	message := Message{
		Role: MessageRoleToolResult,
		ToolResults: []ToolResult{{
			ToolCallID: "call_1",
			Name:       "shell",
			Content:    "bounded observation",
			Meta: &ToolResultMeta{
				ArtifactRef:      "artifact://tr_example",
				OriginalBytes:    12_345,
				OriginalChars:    12_345,
				Complete:         false,
				ArtifactComplete: true,
				Reason:           "intrinsic_large",
				Summary:          "exit_code=1",
			},
		}},
	}
	if err := manager.AppendMessage(message); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}
	reloaded, err := LoadContextManagerFromSessionID(sessionFolder, manager.GetSessionID())
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	got := reloaded.CloneMessageList()[0].ToolResults[0]
	if got.Meta == nil || got.Meta.ArtifactRef != "artifact://tr_example" || got.Meta.Summary != "exit_code=1" {
		t.Fatalf("reloaded ToolResult meta = %#v", got.Meta)
	}

	standard := ConvertMessageList(reloaded.CloneMessageList())
	response, ok := standard[0].Parts[0].(llms.ToolCallResponse)
	if !ok {
		t.Fatalf("standard part = %T, want ToolCallResponse", standard[0].Parts[0])
	}
	if response.Content != "bounded observation" {
		t.Fatalf("model content = %q", response.Content)
	}
}

func TestContextManagerRevisionInheritsArtifactScope(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, []Message{
		{Role: MessageRoleSystem, Content: "system"},
	})
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("persisted-result"), ArtifactMetadata{
		ToolName:   "shell",
		ToolCallID: "call_1",
	})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}

	revision, err := NewContextManagerRevisionFromMessageList(manager, manager.CloneMessageList())
	if err != nil {
		t.Fatalf("NewContextManagerRevisionFromMessageList() error = %v", err)
	}
	if revision.GetSessionID() == manager.GetSessionID() {
		t.Fatal("revision reused transcript session ID")
	}
	if revision.GetArtifactScopeID() != manager.GetArtifactScopeID() {
		t.Fatalf("revision artifact scope = %q, want %q", revision.GetArtifactScopeID(), manager.GetArtifactScopeID())
	}
	if _, err := revision.ReadArtifact(stored.Ref, 0, ArtifactReadDefaultBytes); err != nil {
		t.Fatalf("revision ReadArtifact() error = %v", err)
	}

	reloaded, err := LoadContextManagerFromSessionID(sessionFolder, revision.GetSessionID())
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	if reloaded.GetArtifactScopeID() != manager.GetArtifactScopeID() {
		t.Fatalf("reloaded artifact scope = %q, want %q", reloaded.GetArtifactScopeID(), manager.GetArtifactScopeID())
	}
	chunk, err := reloaded.ReadArtifact(stored.Ref, 0, ArtifactReadDefaultBytes)
	if err != nil {
		t.Fatalf("reloaded ReadArtifact() error = %v", err)
	}
	if string(chunk.Content) != "persisted-result" {
		t.Fatalf("reloaded artifact content = %q", chunk.Content)
	}
}

func TestContextManagerRejectsArtifactAboveSingleLimit(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	_, err = manager.StoreArtifact("application/octet-stream", make([]byte, ArtifactSingleMaxBytes+1), ArtifactMetadata{})
	if !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("StoreArtifact() error = %v, want %v", err, ErrArtifactTooLarge)
	}
}

func TestContextManagerRejectsArtifactWhenScopeIsFull(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	existingPath := filepath.Join(manager.artifactStore.root, "existing.data")
	file, err := os.Create(existingPath)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	if err := file.Truncate(ArtifactScopeMaxBytes); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = manager.StoreArtifact("text/plain", []byte("x"), ArtifactMetadata{})
	if !errors.Is(err, ErrArtifactScopeFull) {
		t.Fatalf("StoreArtifact() error = %v, want %v", err, ErrArtifactScopeFull)
	}
}

func TestContextManagerCountsMetadataTowardArtifactScopeLimit(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	existingPath := filepath.Join(manager.artifactStore.root, "existing.json")
	file, err := os.Create(existingPath)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	if err := file.Truncate(ArtifactScopeMaxBytes); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = manager.StoreArtifact("text/plain", []byte("x"), ArtifactMetadata{})
	if !errors.Is(err, ErrArtifactScopeFull) {
		t.Fatalf("StoreArtifact() error = %v, want %v", err, ErrArtifactScopeFull)
	}
}

func TestContextManagerStoresArtifactsWithOwnerOnlyPermissions(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("sensitive"), ArtifactMetadata{Sensitive: true})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	id, err := artifactIDFromRef(stored.Ref)
	if err != nil {
		t.Fatalf("artifactIDFromRef() error = %v", err)
	}
	for path, want := range map[string]os.FileMode{
		manager.artifactStore.root:                            0o700,
		filepath.Join(manager.artifactStore.root, id+".data"): 0o600,
		filepath.Join(manager.artifactStore.root, id+".json"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("permissions for %s = %o, want %o", path, got, want)
		}
	}
}

func TestContextManagerRejectsExpiredArtifact(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("expired"), ArtifactMetadata{
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	_, err = manager.ReadArtifact(stored.Ref, 0, ArtifactReadDefaultBytes)
	if !errors.Is(err, ErrArtifactExpired) {
		t.Fatalf("ReadArtifact() error = %v, want %v", err, ErrArtifactExpired)
	}
}

func TestContextManagerUsesShortTTLForSensitiveArtifact(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("clipboard"), ArtifactMetadata{Sensitive: true})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	id, err := artifactIDFromRef(stored.Ref)
	if err != nil {
		t.Fatalf("artifactIDFromRef() error = %v", err)
	}
	metadata, err := manager.artifactStore.loadMetadata(id)
	if err != nil {
		t.Fatalf("loadMetadata() error = %v", err)
	}
	if got := metadata.ExpiresAt.Sub(metadata.CreatedAt); got != artifactSensitiveTTL {
		t.Fatalf("sensitive TTL = %v, want %v", got, artifactSensitiveTTL)
	}
}

func TestContextManagerRejectsArtifactFromAnotherScope(t *testing.T) {
	sessionFolder := t.TempDir()
	owner, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("owner NewContextManagerFromMessageList() error = %v", err)
	}
	other, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("other NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := owner.StoreArtifact("text/plain", []byte("private"), ArtifactMetadata{})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	if _, err := other.ReadArtifact(stored.Ref, 0, ArtifactReadDefaultBytes); err == nil {
		t.Fatal("ReadArtifact() from another scope succeeded, want error")
	}
}

func TestContextManagerRejectsArtifactReadAboveHardLimit(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("bounded"), ArtifactMetadata{})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	_, err = manager.ReadArtifact(stored.Ref, 0, ArtifactReadMaxBytes+1)
	if !errors.Is(err, ErrArtifactReadTooWide) {
		t.Fatalf("ReadArtifact() error = %v, want %v", err, ErrArtifactReadTooWide)
	}
}

func TestLoadContextManagerRejectsArtifactScopePathTraversal(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionID := "s_session"
	if err := saveSessionMetadata(sessionFolder, sessionID, sessionMetadata{ArtifactScopeID: "../outside"}); err != nil {
		t.Fatalf("saveSessionMetadata() error = %v", err)
	}
	if _, err := LoadContextManagerFromSessionID(sessionFolder, sessionID); err == nil {
		t.Fatal("LoadContextManagerFromSessionID() succeeded with traversal scope")
	}
}

func TestClearAllSessionsRemovesEmptySessionArtifactScope(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	if _, err := manager.StoreArtifact("text/plain", []byte("artifact"), ArtifactMetadata{}); err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	if err := ClearAllSessions(sessionFolder); err != nil {
		t.Fatalf("ClearAllSessions() error = %v", err)
	}
	if _, err := os.Stat(sessionMetadataPath(sessionFolder, manager.GetSessionID())); !os.IsNotExist(err) {
		t.Fatalf("session metadata still exists, stat error = %v", err)
	}
	if _, err := os.Stat(sessionDataDir(sessionFolder, manager.GetArtifactScopeID())); !os.IsNotExist(err) {
		t.Fatalf("artifact scope still exists, stat error = %v", err)
	}
}
