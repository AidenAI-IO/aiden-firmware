package contextmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	ArtifactSingleMaxBytes = 8 * 1024 * 1024
	ArtifactScopeMaxBytes  = 32 * 1024 * 1024

	artifactDefaultTTL   = 7 * 24 * time.Hour
	artifactSensitiveTTL = time.Hour
)

var (
	ErrArtifactTooLarge  = errors.New("artifact exceeds single-artifact size limit")
	ErrArtifactScopeFull = errors.New("artifact scope size limit exceeded")
)

type ArtifactMetadata struct {
	ToolName   string    `json:"tool_name,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	MIMEType   string    `json:"mime_type"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Sensitive  bool      `json:"sensitive,omitempty"`
	Complete   bool      `json:"complete"`
}

type ArtifactFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Complete bool   `json:"complete"`
}

type artifactStore struct {
	root string
	mu   sync.Mutex
}

func newArtifactStore(sessionFolder, scopeID string) (*artifactStore, error) {
	var err error
	scopeID, err = validateArtifactScopeID(scopeID)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(filepath.Join(sessionDataDir(sessionFolder, scopeID), "tool-results"))
	if err != nil {
		return nil, fmt.Errorf("resolve artifact directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure artifact directory: %w", err)
	}
	return &artifactStore{root: root}, nil
}

func validateArtifactScopeID(scopeID string) (string, error) {
	trimmed := strings.TrimSpace(scopeID)
	if trimmed == "" || filepath.Base(trimmed) != trimmed || strings.ContainsAny(trimmed, `/\\`) {
		return "", fmt.Errorf("invalid artifact scope ID")
	}
	return trimmed, nil
}

func (s *artifactStore) store(mimeType string, data []byte, metadata ArtifactMetadata) (ArtifactFile, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return ArtifactFile{}, fmt.Errorf("artifact store is closed")
	}
	if len(data) > ArtifactSingleMaxBytes {
		return ArtifactFile{}, ErrArtifactTooLarge
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := "tr_" + uuid.NewString()
	hash := sha256.Sum256(data)
	metadata.MIMEType = strings.TrimSpace(mimeType)
	if metadata.MIMEType == "" {
		metadata.MIMEType = "text/plain"
	}
	metadata.Size = int64(len(data))
	metadata.SHA256 = hex.EncodeToString(hash[:])
	metadata.CreatedAt = time.Now().UTC()
	ttl := artifactDefaultTTL
	if metadata.Sensitive {
		ttl = artifactSensitiveTTL
	}
	if metadata.ExpiresAt.IsZero() {
		metadata.ExpiresAt = metadata.CreatedAt.Add(ttl)
	}
	metadata.Complete = true
	metadataData, err := json.Marshal(metadata)
	if err != nil {
		return ArtifactFile{}, fmt.Errorf("marshal artifact metadata: %w", err)
	}
	used, err := artifactScopeBytes(s.root)
	if err != nil {
		return ArtifactFile{}, err
	}
	if used+int64(len(data))+int64(len(metadataData)) > ArtifactScopeMaxBytes {
		return ArtifactFile{}, ErrArtifactScopeFull
	}

	dataPath := filepath.Join(s.root, id+".data")
	if err := writeArtifactFileAtomically(dataPath, data); err != nil {
		return ArtifactFile{}, err
	}
	if err := writeArtifactFileAtomically(filepath.Join(s.root, id+".json"), metadataData); err != nil {
		_ = os.Remove(dataPath)
		return ArtifactFile{}, err
	}

	return ArtifactFile{
		Path:     dataPath,
		Size:     metadata.Size,
		SHA256:   metadata.SHA256,
		Complete: true,
	}, nil
}

func artifactScopeBytes(root string) (int64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, fmt.Errorf("list artifact directory: %w", err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, fmt.Errorf("stat artifact: %w", err)
		}
		total += info.Size()
	}
	return total, nil
}

func writeArtifactFileAtomically(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return fmt.Errorf("create artifact temp file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod artifact temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write artifact temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync artifact temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close artifact temp file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install artifact file: %w", err)
	}
	return nil
}
