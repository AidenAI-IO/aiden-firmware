package agent

func newServerQuickCapture(runtime *Runtime, frameClient screenshotFrameClient) *QuickCaptureController {
	if runtime == nil || !runtime.config.QuickCapture.EnabledOrDefault() || frameClient == nil || runtime.models == nil || runtime.memories == nil || runtime.memories.longTerm == nil {
		return nil
	}
	pipeline := NewScreenMemoryPipeline(
		frameClient,
		runtime.screenState,
		runtime.models,
		runtime.memories.longTerm,
		ScreenMemoryOptions{TTL: runtime.config.QuickCapture.ScreenMemoryTTLOrDefault()},
	)
	return NewQuickCaptureController(pipeline, runtime.logger)
}

func (s *Server) TriggerQuickCapture() error {
	if s == nil || s.quickCapture == nil {
		return ErrQuickCaptureUnavailable
	}
	return s.quickCapture.Trigger()
}
