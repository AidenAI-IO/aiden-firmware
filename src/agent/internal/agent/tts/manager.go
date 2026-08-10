package tts

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

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
	holder      *ProviderHolder
	logger      Logger
	newProvider Factory

	lifecycleMu sync.Mutex
	closed      bool
	closeDone   chan struct{}
	retiredWG   sync.WaitGroup
	retiredErr  error
	retiredMu   sync.Mutex
}

// NewProviderManager creates a manager around an initial provider.
func NewProviderManager(initial TTSProvider, logger Logger) *ProviderManager {
	return NewProviderManagerWithFactory(initial, logger, New)
}

// NewProviderManagerWithFactory creates a manager with an injectable provider
// factory. A nil factory uses the package-level New function.
func NewProviderManagerWithFactory(initial TTSProvider, logger Logger, factory Factory) *ProviderManager {
	if logger == nil {
		logger = nopLogger{}
	}
	if factory == nil {
		factory = New
	}
	return &ProviderManager{
		holder:      NewProviderHolder(initial),
		logger:      logger,
		newProvider: factory,
	}
}

// Holder returns the holder for business code to consume as a TTSProvider.
func (m *ProviderManager) Holder() *ProviderHolder { return m.holder }

// Current returns the current provider name.
func (m *ProviderManager) Current() string { return m.holder.Name() }

// SwitchTo creates a new provider and replaces the current one.
// Existing sessions keep using their original provider and are drained in the
// background, so the settings request is not blocked by ongoing playback.
func (m *ProviderManager) SwitchTo(cfg ProviderConfig) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	if m.closed {
		return ErrProviderManagerClosed
	}

	next, err := m.newProvider(cfg)
	if err != nil {
		return fmt.Errorf("create %s: %w", cfg.Provider, err)
	}
	old, wait := m.holder.replace(next)
	if old != nil {
		m.logger.Info("TTS switched: %s -> %s", old.Name(), next.Name())
		m.retire(old, wait)
	} else {
		m.logger.Info("TTS initialized: %s", next.Name())
	}
	return nil
}

// Close releases the manager and the underlying provider.
func (m *ProviderManager) Close() error {
	return m.CloseContext(context.Background())
}

// CloseContext stops accepting new providers and waits for current and retired
// provider generations to drain. The manager stays closed if ctx expires.
func (m *ProviderManager) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	if !m.closed {
		m.closed = true
		old, wait := m.holder.replace(nil)
		m.retire(old, wait)
		closeDone := make(chan struct{})
		m.closeDone = closeDone
		go func() {
			m.retiredWG.Wait()
			close(closeDone)
		}()
	}

	select {
	case <-m.closeDone:
		m.retiredMu.Lock()
		defer m.retiredMu.Unlock()
		return m.retiredErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ProviderManager) retire(provider TTSProvider, wait func()) {
	if provider == nil {
		return
	}
	name := provider.Name()
	m.retiredWG.Add(1)
	go func() {
		defer m.retiredWG.Done()
		wait()
		if err := provider.Close(); err != nil {
			m.retiredMu.Lock()
			m.retiredErr = errors.Join(m.retiredErr, err)
			m.retiredMu.Unlock()
			m.logger.Warn("close old provider %s: %v", name, err)
		}
	}()
}
