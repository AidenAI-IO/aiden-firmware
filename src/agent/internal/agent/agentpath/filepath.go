package agentpath

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

func ContextManagerSessionFolder(configDir string) string {
	trimmedConfigDir := strings.TrimSpace(configDir)
	if trimmedConfigDir == "" {
		log.Fatalf("configDir is required")
	}
	folderPath := filepath.Join(trimmedConfigDir, "sessions")
	if err := os.MkdirAll(folderPath, 0755); err != nil {
		log.Fatalf("failed to create sessions folder %s: %v\n", folderPath, err)
	}
	return folderPath
}
