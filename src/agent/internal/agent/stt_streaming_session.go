package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type streamingSTTSession struct {
	uploader STTStreamUploader

	mu         sync.Mutex
	transcript string
	finalized  bool
}

func beginStreamingSTTSession(ctx context.Context, client STTClient, cfg STTStreamConfig) (*streamingSTTSession, error) {
	if client == nil || !client.Capabilities().SupportsStreamingUpload {
		return nil, nil
	}
	uploader, err := client.NewStreamingUploader(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &streamingSTTSession{uploader: uploader}, nil
}

func (s *streamingSTTSession) UploadPCM(pcm []byte) error {
	if s == nil || s.uploader == nil || len(pcm) == 0 {
		return nil
	}
	if err := s.uploader.UploadPCM(pcm); err != nil {
		return fmt.Errorf("streaming STT upload: %w", err)
	}
	return nil
}

func (s *streamingSTTSession) Finalize() (string, error) {
	if s == nil {
		return "", nil
	}

	s.mu.Lock()
	if s.finalized {
		transcript := s.transcript
		s.mu.Unlock()
		return transcript, nil
	}
	s.mu.Unlock()

	transcript, err := s.uploader.Finalize()
	if err != nil {
		return "", fmt.Errorf("streaming STT finalize: %w", err)
	}

	transcript = strings.TrimSpace(transcript)
	s.mu.Lock()
	s.transcript = transcript
	s.finalized = true
	s.mu.Unlock()
	return transcript, nil
}

func (s *streamingSTTSession) Close() error {
	if s == nil || s.uploader == nil {
		return nil
	}
	if err := s.uploader.Close(); err != nil {
		return fmt.Errorf("streaming STT close: %w", err)
	}
	return nil
}
