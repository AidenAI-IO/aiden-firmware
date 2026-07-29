package contextmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	ArtifactReadDefaultBytes = 8 * 1024
	ArtifactReadMaxBytes     = 16 * 1024
	ArtifactSingleMaxBytes   = 8 * 1024 * 1024
	ArtifactScopeMaxBytes    = 32 * 1024 * 1024

	artifactDefaultTTL   = 7 * 24 * time.Hour
	artifactSensitiveTTL = time.Hour
	artifactRefPrefix    = "artifact://"
)

var (
	ErrArtifactTooLarge    = errors.New("artifact exceeds single-artifact size limit")
	ErrArtifactScopeFull   = errors.New("artifact scope size limit exceeded")
	ErrArtifactExpired     = errors.New("artifact expired")
	ErrInvalidArtifactRef  = errors.New("invalid artifact ref")
	ErrArtifactReadTooWide = errors.New("artifact read exceeds hard limit")
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

type ArtifactRef struct {
	Ref      string `json:"ref"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Complete bool   `json:"complete"`
}

type ArtifactChunk struct {
	Content    []byte `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Complete   bool   `json:"complete"`
	Found      bool   `json:"found,omitempty"`
	SHA256     string `json:"sha256"`
	MIMEType   string `json:"mime_type"`
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
	root := filepath.Join(sessionDataDir(sessionFolder, scopeID), "tool-results")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
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

func (s *artifactStore) store(mimeType string, data []byte, metadata ArtifactMetadata) (ArtifactRef, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return ArtifactRef{}, fmt.Errorf("artifact store is closed")
	}
	if len(data) > ArtifactSingleMaxBytes {
		return ArtifactRef{}, ErrArtifactTooLarge
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	used, err := artifactDataBytes(s.root)
	if err != nil {
		return ArtifactRef{}, err
	}
	if used+int64(len(data)) > ArtifactScopeMaxBytes {
		return ArtifactRef{}, ErrArtifactScopeFull
	}

	id := "tr_" + uuid.NewString()
	ref := artifactRefPrefix + id
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

	dataPath := filepath.Join(s.root, id+".data")
	if err := writeArtifactFileAtomically(dataPath, data); err != nil {
		return ArtifactRef{}, err
	}
	metadataData, err := json.Marshal(metadata)
	if err != nil {
		_ = os.Remove(dataPath)
		return ArtifactRef{}, fmt.Errorf("marshal artifact metadata: %w", err)
	}
	if err := writeArtifactFileAtomically(filepath.Join(s.root, id+".json"), metadataData); err != nil {
		_ = os.Remove(dataPath)
		return ArtifactRef{}, err
	}

	return ArtifactRef{
		Ref:      ref,
		Size:     metadata.Size,
		SHA256:   metadata.SHA256,
		Complete: true,
	}, nil
}

func (s *artifactStore) read(ref string, offset int64, limit int) (ArtifactChunk, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return ArtifactChunk{}, fmt.Errorf("artifact store is closed")
	}
	id, err := artifactIDFromRef(ref)
	if err != nil {
		return ArtifactChunk{}, err
	}
	if offset < 0 {
		return ArtifactChunk{}, fmt.Errorf("artifact offset must be >= 0")
	}
	if limit <= 0 {
		limit = ArtifactReadDefaultBytes
	}
	if limit > ArtifactReadMaxBytes {
		return ArtifactChunk{}, ErrArtifactReadTooWide
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	metadata, err := s.loadMetadata(id)
	if err != nil {
		return ArtifactChunk{}, err
	}
	if !metadata.ExpiresAt.IsZero() && time.Now().After(metadata.ExpiresAt) {
		return ArtifactChunk{}, ErrArtifactExpired
	}
	if offset > metadata.Size {
		return ArtifactChunk{}, fmt.Errorf("artifact offset %d exceeds size %d", offset, metadata.Size)
	}

	file, err := os.Open(filepath.Join(s.root, id+".data"))
	if err != nil {
		return ArtifactChunk{}, fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ArtifactChunk{}, fmt.Errorf("seek artifact: %w", err)
	}
	remaining := metadata.Size - offset
	readLen := min(int64(limit), remaining)
	content := make([]byte, int(readLen))
	if _, err := io.ReadFull(file, content); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ArtifactChunk{}, fmt.Errorf("read artifact: %w", err)
	}
	nextOffset := offset + int64(len(content))
	return ArtifactChunk{
		Content:    content,
		Offset:     offset,
		NextOffset: nextOffset,
		Complete:   nextOffset >= metadata.Size,
		Found:      true,
		SHA256:     metadata.SHA256,
		MIMEType:   metadata.MIMEType,
	}, nil
}

func (s *artifactStore) search(ref, query string, offset int64, limit int) (ArtifactChunk, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return ArtifactChunk{}, fmt.Errorf("artifact store is closed")
	}
	id, err := artifactIDFromRef(ref)
	if err != nil {
		return ArtifactChunk{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return ArtifactChunk{}, fmt.Errorf("artifact query is empty")
	}
	if offset < 0 {
		return ArtifactChunk{}, fmt.Errorf("artifact offset must be >= 0")
	}
	if limit <= 0 {
		limit = ArtifactReadDefaultBytes
	}
	if limit > ArtifactReadMaxBytes {
		return ArtifactChunk{}, ErrArtifactReadTooWide
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	metadata, err := s.loadMetadata(id)
	if err != nil {
		return ArtifactChunk{}, err
	}
	if !metadata.ExpiresAt.IsZero() && time.Now().After(metadata.ExpiresAt) {
		return ArtifactChunk{}, ErrArtifactExpired
	}
	if offset > metadata.Size {
		return ArtifactChunk{}, fmt.Errorf("artifact offset %d exceeds size %d", offset, metadata.Size)
	}
	data, err := os.ReadFile(filepath.Join(s.root, id+".data"))
	if err != nil {
		return ArtifactChunk{}, fmt.Errorf("read artifact: %w", err)
	}
	needle := []byte(query)
	relativeIndex := bytes.Index(data[offset:], needle)
	if relativeIndex < 0 {
		return ArtifactChunk{
			Offset:     offset,
			NextOffset: metadata.Size,
			Complete:   true,
			Found:      false,
			SHA256:     metadata.SHA256,
			MIMEType:   metadata.MIMEType,
		}, nil
	}
	matchOffset := offset + int64(relativeIndex)
	windowStart := max(int64(0), matchOffset-int64(max(0, limit-len(needle))/3))
	windowEnd := min(metadata.Size, windowStart+int64(limit))
	minimumEnd := matchOffset + int64(len(needle))
	if windowEnd < minimumEnd {
		windowEnd = minimumEnd
		windowStart = max(int64(0), windowEnd-int64(limit))
	}
	nextOffset := matchOffset + int64(len(needle))
	hasMore := nextOffset < metadata.Size && bytes.Contains(data[nextOffset:], needle)
	return ArtifactChunk{
		Content:    append([]byte(nil), data[windowStart:windowEnd]...),
		Offset:     windowStart,
		NextOffset: nextOffset,
		Complete:   !hasMore,
		Found:      true,
		SHA256:     metadata.SHA256,
		MIMEType:   metadata.MIMEType,
	}, nil
}

func (s *artifactStore) loadMetadata(id string) (ArtifactMetadata, error) {
	data, err := os.ReadFile(filepath.Join(s.root, id+".json"))
	if err != nil {
		return ArtifactMetadata{}, fmt.Errorf("read artifact metadata: %w", err)
	}
	var metadata ArtifactMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return ArtifactMetadata{}, fmt.Errorf("decode artifact metadata: %w", err)
	}
	return metadata, nil
}

func artifactIDFromRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasPrefix(trimmed, artifactRefPrefix) {
		return "", ErrInvalidArtifactRef
	}
	id := strings.TrimPrefix(trimmed, artifactRefPrefix)
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) || !strings.HasPrefix(id, "tr_") {
		return "", ErrInvalidArtifactRef
	}
	return id, nil
}

func artifactDataBytes(root string) (int64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, fmt.Errorf("list artifact directory: %w", err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".data" {
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
	if err := file.Chmod(0o644); err != nil {
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
