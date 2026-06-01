package tts

import "fmt"

// Logger is the minimal logging interface used by the manager.
type Logger interface {
	Info(format string, args ...any)
	Warn(format string, args ...any)
}

// nopLogger is used when no logger is provided.
type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Warn(string, ...any) {}

// ProviderManager coordinates provider creation and runtime switching.
// Business code uses Holder() for actual TTS calls; SwitchTo is invoked from
// the HTTP API when the user changes provider in the app.
type ProviderManager struct {
	holder *ProviderHolder
	logger Logger
}

// NewProviderManager creates a manager around an initial provider.
func NewProviderManager(initial TTSProvider, logger Logger) *ProviderManager {
	if logger == nil {
		logger = nopLogger{}
	}
	return &ProviderManager{
		holder: NewProviderHolder(initial),
		logger: logger,
	}
}

// Holder returns the holder for business code to consume as a TTSProvider.
func (m *ProviderManager) Holder() *ProviderHolder { return m.holder }

// Current returns the current provider name.
func (m *ProviderManager) Current() string { return m.holder.Name() }

// SwitchTo creates a new provider and replaces the current one.
// Swap blocks until all in-flight sessions from the old provider have
// completed, so the old provider can be safely closed immediately after.
func (m *ProviderManager) SwitchTo(cfg ProviderConfig) error {
	next, err := New(cfg)
	if err != nil {
		return fmt.Errorf("create %s: %w", cfg.Provider, err)
	}
	old := m.holder.Swap(next) // blocks until in-flight sessions finish
	if old != nil {
		m.logger.Info("TTS switched: %s -> %s", old.Name(), next.Name())
		if err := old.Close(); err != nil {
			m.logger.Warn("close old provider %s: %v", old.Name(), err)
		}
	} else {
		m.logger.Info("TTS initialized: %s", next.Name())
	}
	return nil
}

// Close releases the manager and the underlying provider.
func (m *ProviderManager) Close() error {
	return m.holder.Close()
}
