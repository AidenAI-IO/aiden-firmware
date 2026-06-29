package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type visualArtifactReader interface {
	ReadVisualArtifact(ref string) ([]byte, error)
}

type visualArtifactStore struct {
	mu        sync.Mutex
	rootDir   string
	next      int
	temporary bool
}

func newVisualArtifactStore(recorder *EpisodeRecorder) *visualArtifactStore {
	rootDir := recorder.visualArtifactRoot()
	temporary := false
	if strings.TrimSpace(rootDir) == "" {
		var err error
		rootDir, err = os.MkdirTemp("", "aiden-agent-visual-")
		if err != nil {
			return nil
		}
		temporary = true
	}
	return &visualArtifactStore{
		rootDir:   rootDir,
		temporary: temporary,
	}
}

func (s *visualArtifactStore) Close() {
	if s == nil || !s.temporary {
		return
	}
	_ = os.RemoveAll(s.rootDir)
}

func (s *visualArtifactStore) ExternalizeObservation(observation string) (string, bool, error) {
	if s == nil {
		return observation, false, nil
	}
	var result postActionScreenshotResult
	if err := json.Unmarshal([]byte(observation), &result); err != nil {
		return observation, false, nil
	}
	if strings.TrimSpace(result.Data) == "" {
		return observation, false, nil
	}
	imageBytes, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || len(imageBytes) == 0 {
		return observation, false, nil
	}
	format, ok := normalizeScreenshotFormat(result.Format)
	if !ok {
		return observation, false, nil
	}

	ref, err := s.write(format, imageBytes)
	if err != nil {
		return observation, false, err
	}
	result.Format = format
	result.Size = len(imageBytes)
	result.Data = ""
	result.ScreenshotRef = ref
	data, err := json.Marshal(result)
	if err != nil {
		return observation, false, err
	}
	return string(data), true, nil
}

func (s *visualArtifactStore) write(format string, imageBytes []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	artifactsDir := filepath.Join(s.rootDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return "", fmt.Errorf("create visual artifact directory: %w", err)
	}
	for {
		s.next++
		name := fmt.Sprintf("visual_%06d.%s", s.next, safePathName(format))
		path := filepath.Join(artifactsDir, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create visual artifact: %w", err)
		}
		_, writeErr := file.Write(imageBytes)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("write visual artifact: %w", writeErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close visual artifact: %w", closeErr)
		}
		return filepath.ToSlash(filepath.Join("artifacts", name)), nil
	}
}

func (s *visualArtifactStore) ReadVisualArtifact(ref string) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("visual artifact store is not configured")
	}
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(ref)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("invalid visual artifact reference %q", ref)
	}
	path := filepath.Join(s.rootDir, clean)
	return os.ReadFile(path)
}
