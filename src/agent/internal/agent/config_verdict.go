package agent

import (
	"errors"
	"io/fs"
	"os"
)

// ConfigValidationVerdict reports whether the Agent runtime accepts the
// persisted configuration at path, plus the field errors when it does not.
//
// The runtime loader is the authority: it is the loader the Agent boots with, so
// a verdict taken anywhere else can drift from the one that keeps the Agent down.
// Every surface that reports a config as valid or invalid reads it here — the
// Config Web recovery portal and `agent config-check --config` — so the verdict
// shown to a user and the verdict that gates startup cannot disagree.
//
// An absent file is valid, not a field error: the runtime reads it as the
// built-in defaults, which is also what the config page renders so a save can
// create the file. A file that cannot be read, decoded, or resolved still fails,
// because there is no configuration to work from.
func ConfigValidationVerdict(path string) (bool, []ConfigValidationError) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return true, []ConfigValidationError{}
	}
	if _, err := LoadRuntimeConfig(path); err != nil {
		return false, ParseConfigValidationErrors(err)
	}
	return true, []ConfigValidationError{}
}
