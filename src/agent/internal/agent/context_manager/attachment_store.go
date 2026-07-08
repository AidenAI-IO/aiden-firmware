package context_manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type attachmentStore struct {
	root string
}

func newAttachmentStore(sessionFolder string, sessionID string) (*attachmentStore, error) {
	attachmentFolder := filepath.Join(sessionFolder, sessionID, "attachments")
	if err := os.MkdirAll(attachmentFolder, 0o755); err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}
	return &attachmentStore{
		root: attachmentFolder,
	}, nil
}

func (s *attachmentStore) store(mimeType string, data []byte) (Attachment, error) {
	if s.root == "" {
		return Attachment{}, fmt.Errorf("attachment store is closed")
	}
	for {
		name := fmt.Sprintf("%s%s", uuid.New().String(), attachmentExtension(mimeType))
		path := filepath.Join(s.root, name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return Attachment{}, fmt.Errorf("create attachment file: %w", err)
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return Attachment{}, fmt.Errorf("write attachment file: %w", writeErr)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return Attachment{}, fmt.Errorf("close attachment file: %w", closeErr)
		}
		return Attachment{
			MIMEType: mimeType,
			FileSize: int64(len(data)),
			FilePath: path,
		}, nil
	}
}

func attachmentExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "audio/wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	default:
		return ".bin"
	}
}