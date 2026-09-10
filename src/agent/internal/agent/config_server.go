package agent

import (
	"context"
	"fmt"
	"reflect"
)

// PrepareConfig builds changed HTTP clients without replacing the Server or
// its websocket bridge, pending chat results, and event subscriptions.
func (s *Server) PrepareConfig(_ context.Context, cfg Config) (func(bool), error) {
	current := s.runtime.ConfigSnapshot()
	s.configDepsMu.RLock()
	stt, audio, playback, adb := s.sttClient, s.audioClient, s.ttsPlaybackBackend, s.androidADB
	s.configDepsMu.RUnlock()
	var err error
	if !reflect.DeepEqual(current.STT, cfg.STT) {
		stt, err = NewSTTClientFromConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("configure STT: %w", err)
		}
	}
	if current.Audio != cfg.Audio {
		audio = NewAudioServiceClient(cfg.Audio.SocketOrDefault())
		playback = newTTSPlaybackBackendFromConfig(cfg, audio, s.logger)
	}
	if current.HID.FrameSocket != cfg.HID.FrameSocket {
		adb = NewAndroidADBManager(cfg.HID.FrameSocketOrDefault(), s.logger)
	}
	return func(commit bool) {
		if !commit {
			return
		}
		s.configDepsMu.Lock()
		s.sttClient, s.audioClient, s.ttsPlaybackBackend, s.androidADB = stt, audio, playback, adb
		s.configDepsMu.Unlock()
		s.screenCaptureMu.Lock()
		s.screenCaptureClient = screenProviderFromRuntime(s.runtime)
		s.screenCaptureMu.Unlock()
		s.liveActivity.Reconfigure(cfg.LiveActivity)
	}, nil
}
func (s *Server) sttClientSnapshot() STTClient {
	s.configDepsMu.RLock()
	defer s.configDepsMu.RUnlock()
	return s.sttClient
}
func (s *Server) androidADBSnapshot() androidADBController {
	s.configDepsMu.RLock()
	defer s.configDepsMu.RUnlock()
	return s.androidADB
}
