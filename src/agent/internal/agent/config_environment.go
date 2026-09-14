package agent

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// SystemEnvironmentRevision identifies the environment file without returning
// credentials. An absent file has the same revision as an empty file.
func SystemEnvironmentRevision(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (r *Runtime) SetInitialEnvironmentRevision(revision string) {
	r.configStatusMu.Lock()
	defer r.configStatusMu.Unlock()
	r.configStatus.EnvironmentRevision = revision
}
