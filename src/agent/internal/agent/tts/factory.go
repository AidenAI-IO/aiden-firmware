package tts

import (
	"fmt"
	"sort"
	"strings"
)

// Factory is the constructor signature each adapter package exports.
type Factory func(cfg ProviderConfig) (TTSProvider, error)

// factories holds adapter constructors registered at init time.
// Adapters live in sub-packages and call Register() in their own init().
// We use this lightweight registration (rather than a hard switch in factory.go)
// so the core package does not import every adapter directly — that would
// force compiling all adapters into every binary, which is wasteful for the
// embedded target.
var factories = map[string]Factory{}

// Register installs an adapter factory. Panics if name is already taken,
// since this only runs at init time and a duplicate is a programming error.
func Register(name string, f Factory) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		panic("tts: Register called with empty name")
	}
	if _, exists := factories[name]; exists {
		panic(fmt.Sprintf("tts: duplicate provider registration: %s", name))
	}
	factories[name] = f
}

// New creates a provider from cfg.Provider. The named adapter must have been
// registered via Register() (typically by importing the adapter package).
func New(cfg ProviderConfig) (TTSProvider, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" {
		return nil, fmt.Errorf("%w: empty provider name", ErrProviderNotFound)
	}
	f, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrProviderNotFound, cfg.Provider)
	}
	return f(cfg)
}

// AvailableProviders returns the list of registered provider names.
func AvailableProviders() []string {
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
