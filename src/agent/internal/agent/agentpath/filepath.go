package agentpath

import (
	"log"
	"path/filepath"
	"strings"
)

func ContextManagerSessionFolder(configDir string) string {
	trimmedConfigDir := strings.TrimSpace(configDir)
	if trimmedConfigDir == "" {
		log.Fatalf("configDir is required")
	}
	return filepath.Join(configDir, "sessions")
}