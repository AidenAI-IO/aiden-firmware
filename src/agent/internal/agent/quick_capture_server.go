package agent

import "context"

type runtimeQuickCapturer struct {
	runtime  *Runtime
	fallback screenshotFrameClient
}

func (c runtimeQuickCapturer) Capture(ctx context.Context) (string, error) {
	r := c.runtime
	r.configOperations.RLock()
	defer r.configOperations.RUnlock()
	cfg := r.ConfigSnapshot()
	if !cfg.QuickCapture.EnabledOrDefault() || r.models == nil || r.memories == nil || r.memories.longTerm == nil {
		return "", ErrQuickCaptureUnavailable
	}
	client := c.fallback
	if r.toolSnapshot() != nil {
		client = screenProviderFromRuntime(r)
	}
	if client == nil {
		return "", ErrQuickCaptureUnavailable
	}
	pipeline := NewScreenMemoryPipeline(client, r.screenState, r.models, r.memories.longTerm, ScreenMemoryOptions{TTL: cfg.QuickCapture.ScreenMemoryTTLOrDefault()})
	return pipeline.Capture(ctx)
}
func newServerQuickCapture(runtime *Runtime, frameClient screenshotFrameClient) *QuickCaptureController {
	if runtime == nil {
		return nil
	}
	return NewQuickCaptureController(runtimeQuickCapturer{runtime, frameClient}, runtime.logger)
}
func (s *Server) TriggerQuickCapture() error {
	if s == nil || s.quickCapture == nil || (s.runtime != nil && !s.runtime.ConfigSnapshot().QuickCapture.EnabledOrDefault()) {
		return ErrQuickCaptureUnavailable
	}
	return s.quickCapture.Trigger()
}
