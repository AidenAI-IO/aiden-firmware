package contextmanager

import (
	"aiden-agent/internal/agent/messages"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"
)

func writeLegacySessionMetadata(sessionFolder, sessionID string, metadata sessionMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return writeArtifactFileAtomically(sessionMetadataPath(sessionFolder, sessionID), data)
}

func TestNewContextManagerStoresArtifactsBySessionWithoutMetadataSidecar(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	if filepath.Base(filepath.Dir(manager.artifactStore.root)) != manager.GetSessionID() {
		t.Fatalf("artifact root = %q, want session %q", manager.artifactStore.root, manager.GetSessionID())
	}
	if _, err := os.Stat(sessionMetadataPath(sessionFolder, manager.GetSessionID())); !os.IsNotExist(err) {
		t.Fatalf("new session metadata sidecar exists, stat error = %v", err)
	}
}

func TestContextManagerReturnsReadableArtifactFilePath(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("shell-readable-result"), ArtifactMetadata{
		ToolName:   "shell",
		ToolCallID: "call_path",
	})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	if stored.Path == "" || !filepath.IsAbs(stored.Path) {
		t.Fatalf("StoreArtifact() path = %q, want absolute path", stored.Path)
	}
	if filepath.Dir(stored.Path) != manager.artifactStore.root {
		t.Fatalf("StoreArtifact() path = %q, want file in %q", stored.Path, manager.artifactStore.root)
	}
	data, err := os.ReadFile(stored.Path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", stored.Path, err)
	}
	if string(data) != "shell-readable-result" {
		t.Fatalf("artifact file content = %q", data)
	}
}

func TestContextManagerReturnsAbsoluteArtifactPathForRelativeSessionFolder(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("sessions", 0o700); err != nil {
		t.Fatalf("MkdirAll(sessions) error = %v", err)
	}
	manager, err := NewContextManagerFromMessageList("sessions", nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("relative-root"), ArtifactMetadata{})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	if !filepath.IsAbs(stored.Path) {
		t.Fatalf("StoreArtifact() path = %q, want absolute path", stored.Path)
	}
	if data, err := os.ReadFile(stored.Path); err != nil || string(data) != "relative-root" {
		t.Fatalf("ReadFile(%s) = %q, error = %v", stored.Path, data, err)
	}
}

func TestToolResultMetadataPersistsButIsNotSentToModel(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	message := messages.Message{
		Role: messages.MessageRoleToolResult,
		ToolResults: []messages.ToolResult{{
			ToolCallID: "call_1",
			Name:       "shell",
			Content:    "bounded observation",
			Meta: &messages.ToolResultMeta{
				ArtifactPath:     "/tmp/tool-results/tr_example.data",
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
	if got.Meta == nil || got.Meta.ArtifactPath != "/tmp/tool-results/tr_example.data" || got.Meta.Summary != "exit_code=1" {
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

func TestLoadContextManagerMigratesLegacyArtifactRefToShellReadablePath(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionID := "s_legacy"
	scopeID := "s_legacy_scope"
	artifactID := "tr_" + uuid.NewString()
	if err := writeLegacySessionMetadata(sessionFolder, sessionID, sessionMetadata{ArtifactScopeID: scopeID}); err != nil {
		t.Fatalf("writeLegacySessionMetadata() error = %v", err)
	}
	store, err := newArtifactStore(sessionFolder, scopeID)
	if err != nil {
		t.Fatalf("newArtifactStore() error = %v", err)
	}
	wantPath := filepath.Join(store.root, artifactID+".data")
	if err := os.WriteFile(wantPath, []byte("legacy-result"), 0o600); err != nil {
		t.Fatalf("WriteFile(artifact) error = %v", err)
	}
	legacyRef := "artifact://" + artifactID
	line := fmt.Sprintf(`{"role":"tool_result","tool_results":[{"tool_call_id":"call_legacy","name":"shell","content":"Full result: %s","meta":{"artifact_ref":"%s","complete":false,"artifact_complete":true}}]}`+"\n", legacyRef, legacyRef)
	if err := os.WriteFile(filepath.Join(sessionFolder, sessionID+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("WriteFile(session) error = %v", err)
	}

	manager, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	result := manager.CloneMessageList()[0].ToolResults[0]
	if result.Meta == nil || result.Meta.ArtifactPath != wantPath {
		t.Fatalf("migrated metadata = %#v, want path %q", result.Meta, wantPath)
	}
	if !strings.Contains(result.Content, "Full result file: "+wantPath) || !strings.Contains(result.Content, "Use shell commands") {
		t.Fatalf("migrated content = %q", result.Content)
	}
	data, err := os.ReadFile(result.Meta.ArtifactPath)
	if err != nil || string(data) != "legacy-result" {
		t.Fatalf("ReadFile(%s) = %q, error = %v", result.Meta.ArtifactPath, data, err)
	}
	encoded, err := json.Marshal(result.Meta)
	if err != nil {
		t.Fatalf("Marshal(meta) error = %v", err)
	}
	if strings.Contains(string(encoded), "artifact_ref") || !strings.Contains(string(encoded), "artifact_path") {
		t.Fatalf("migrated metadata JSON = %s", encoded)
	}
}

func TestLoadContextManagerIgnoresUnsafeLegacyArtifactRef(t *testing.T) {
	sessionFolder := t.TempDir()
	sessionID := "s_legacy_unsafe"
	if err := writeLegacySessionMetadata(sessionFolder, sessionID, sessionMetadata{ArtifactScopeID: sessionID}); err != nil {
		t.Fatalf("writeLegacySessionMetadata() error = %v", err)
	}
	line := `{"role":"tool_result","tool_results":[{"tool_call_id":"call_legacy","name":"shell","content":"legacy","meta":{"artifact_ref":"artifact://tr_../../outside","complete":false,"artifact_complete":true}}]}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionFolder, sessionID+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatalf("WriteFile(session) error = %v", err)
	}
	manager, err := LoadContextManagerFromSessionID(sessionFolder, sessionID)
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	meta := manager.CloneMessageList()[0].ToolResults[0].Meta
	if meta == nil || meta.ArtifactPath != "" {
		t.Fatalf("unsafe legacy metadata = %#v, want no migrated path", meta)
	}
}

func TestContextManagerRevisionPreservesArtifactPathWithoutSharingStore(t *testing.T) {
	sessionFolder := t.TempDir()
	manager, err := NewContextManagerFromMessageList(sessionFolder, []messages.Message{
		{Role: messages.MessageRoleSystem, Content: "system"},
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
	if err := manager.AppendMessage(messages.Message{
		Role: messages.MessageRoleToolResult,
		ToolResults: []messages.ToolResult{{
			ToolCallID: "call_1",
			Name:       "shell",
			Content:    "bounded result",
			Meta:       &messages.ToolResultMeta{ArtifactPath: stored.Path, ArtifactComplete: true},
		}},
	}); err != nil {
		t.Fatalf("AppendMessage() error = %v", err)
	}

	revision, err := NewContextManagerRevisionFromMessageList(manager, manager.CloneMessageList())
	if err != nil {
		t.Fatalf("NewContextManagerRevisionFromMessageList() error = %v", err)
	}
	if revision.GetSessionID() == manager.GetSessionID() {
		t.Fatal("revision reused transcript session ID")
	}
	if revision.artifactStore.root == manager.artifactStore.root {
		t.Fatalf("revision artifact store = %q, want independent session store", revision.artifactStore.root)
	}
	revisionStored, err := revision.StoreArtifact("text/plain", []byte("revision-result"), ArtifactMetadata{ToolName: "shell"})
	if err != nil {
		t.Fatalf("revision StoreArtifact() error = %v", err)
	}
	if filepath.Dir(revisionStored.Path) != revision.artifactStore.root || filepath.Base(filepath.Dir(revision.artifactStore.root)) != revision.GetSessionID() {
		t.Fatalf("revision artifact path = %q, want revision-owned directory %q", revisionStored.Path, revision.artifactStore.root)
	}
	if data, err := os.ReadFile(stored.Path); err != nil || string(data) != "persisted-result" {
		t.Fatalf("revision artifact file read = %q, error = %v", data, err)
	}

	reloaded, err := LoadContextManagerFromSessionID(sessionFolder, revision.GetSessionID())
	if err != nil {
		t.Fatalf("LoadContextManagerFromSessionID() error = %v", err)
	}
	result := reloaded.CloneMessageList()[1].ToolResults[0]
	if result.Meta == nil || result.Meta.ArtifactPath != stored.Path {
		t.Fatalf("reloaded artifact path = %#v, want %q", result.Meta, stored.Path)
	}
	if reloaded.artifactStore.root == manager.artifactStore.root {
		t.Fatalf("reloaded artifact store = %q, want revision-owned store", reloaded.artifactStore.root)
	}
	data, err := os.ReadFile(stored.Path)
	if err != nil || string(data) != "persisted-result" {
		t.Fatalf("reloaded artifact file read = %q, error = %v", data, err)
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

func TestContextManagerRejectsArtifactWhenSessionStoreIsFull(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	existingPath := filepath.Join(manager.artifactStore.root, "existing.data")
	file, err := os.Create(existingPath)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	if err := file.Truncate(ArtifactSessionMaxBytes); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = manager.StoreArtifact("text/plain", []byte("x"), ArtifactMetadata{})
	if !errors.Is(err, ErrArtifactSessionFull) {
		t.Fatalf("StoreArtifact() error = %v, want %v", err, ErrArtifactSessionFull)
	}
}

func TestContextManagerCountsMetadataTowardArtifactSessionLimit(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	existingPath := filepath.Join(manager.artifactStore.root, "existing.json")
	file, err := os.Create(existingPath)
	if err != nil {
		t.Fatalf("os.Create() error = %v", err)
	}
	if err := file.Truncate(ArtifactSessionMaxBytes); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = manager.StoreArtifact("text/plain", []byte("x"), ArtifactMetadata{})
	if !errors.Is(err, ErrArtifactSessionFull) {
		t.Fatalf("StoreArtifact() error = %v, want %v", err, ErrArtifactSessionFull)
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
	id := strings.TrimSuffix(filepath.Base(stored.Path), filepath.Ext(stored.Path))
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

func TestContextManagerUsesShortTTLForSensitiveArtifact(t *testing.T) {
	manager, err := NewContextManagerFromMessageList(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewContextManagerFromMessageList() error = %v", err)
	}
	stored, err := manager.StoreArtifact("text/plain", []byte("clipboard"), ArtifactMetadata{Sensitive: true})
	if err != nil {
		t.Fatalf("StoreArtifact() error = %v", err)
	}
	id := strings.TrimSuffix(filepath.Base(stored.Path), filepath.Ext(stored.Path))
	metadataData, err := os.ReadFile(filepath.Join(manager.artifactStore.root, id+".json"))
	if err != nil {
		t.Fatalf("ReadFile(metadata) error = %v", err)
	}
	var metadata ArtifactMetadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		t.Fatalf("json.Unmarshal(metadata) error = %v", err)
	}
	if got := metadata.ExpiresAt.Sub(metadata.CreatedAt); got != artifactSensitiveTTL {
		t.Fatalf("sensitive TTL = %v, want %v", got, artifactSensitiveTTL)
	}
}

func TestArtifactPathRecoverableRejectsUnsafeMetadataSidecars(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "tr_safe.data")
	metadataPath := filepath.Join(root, "tr_safe.json")
	if err := os.WriteFile(dataPath, []byte("artifact"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", dataPath, err)
	}
	validMetadata, err := json.Marshal(ArtifactMetadata{
		MIMEType:  "text/plain",
		Size:      int64(len("artifact")),
		CreatedAt: time.Now().Add(-time.Minute),
		ExpiresAt: time.Now().Add(time.Hour),
		Complete:  true,
	})
	if err != nil {
		t.Fatalf("Marshal metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, validMetadata, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", metadataPath, err)
	}
	if !ArtifactPathRecoverable(dataPath, time.Now()) {
		t.Fatal("valid artifact metadata was rejected")
	}

	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove(%s): %v", metadataPath, err)
	}
	targetPath := filepath.Join(root, "target.json")
	if err := os.WriteFile(targetPath, validMetadata, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", targetPath, err)
	}
	if err := os.Symlink(targetPath, metadataPath); err == nil {
		if ArtifactPathRecoverable(dataPath, time.Now()) {
			t.Fatal("symlink metadata sidecar was accepted")
		}
		if err := os.Remove(metadataPath); err != nil {
			t.Fatalf("Remove symlink(%s): %v", metadataPath, err)
		}
	}

	if err := os.Mkdir(metadataPath, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", metadataPath, err)
	}
	if ArtifactPathRecoverable(dataPath, time.Now()) {
		t.Fatal("non-regular metadata sidecar was accepted")
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("Remove directory(%s): %v", metadataPath, err)
	}

	oversized := bytes.Repeat([]byte("x"), artifactMetadataMaxBytes+1)
	if err := os.WriteFile(metadataPath, oversized, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", metadataPath, err)
	}
	if ArtifactPathRecoverable(dataPath, time.Now()) {
		t.Fatal("oversized metadata sidecar was accepted")
	}
}

func TestLoadContextManagerRejectsArtifactScopePathTraversal(t *testing.T) {
	for _, scopeID := range []string{"../outside", ".", ".."} {
		t.Run(scopeID, func(t *testing.T) {
			sessionFolder := t.TempDir()
			sessionID := "s_session"
			if err := writeLegacySessionMetadata(sessionFolder, sessionID, sessionMetadata{ArtifactScopeID: scopeID}); err != nil {
				t.Fatalf("writeLegacySessionMetadata() error = %v", err)
			}
			if _, err := LoadContextManagerFromSessionID(sessionFolder, sessionID); err == nil {
				t.Fatalf("LoadContextManagerFromSessionID() succeeded with scope %q", scopeID)
			}
		})
	}
}

func TestClearAllSessionsRemovesSessionArtifactDirectory(t *testing.T) {
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
	if _, err := os.Stat(sessionDataDir(sessionFolder, manager.GetSessionID())); !os.IsNotExist(err) {
		t.Fatalf("session data directory still exists, stat error = %v", err)
	}
}
