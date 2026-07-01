package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"aiden-agent/internal/agent/tts"
)

type session struct {
	ctx        context.Context
	adapter    *Adapter
	sink       tts.AudioSink
	httpClient *http.Client

	mu         sync.Mutex
	textBuffer *bytes.Buffer
	closed     bool
	lastErr    error
}

func (s *session) WriteText(text string) error {
	if text == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("session closed")
	}

	s.textBuffer.WriteString(text)

	// Find sentence boundary and synthesize only up to it, retaining remainder
	if idx := lastSentenceBoundary(s.textBuffer.String()); idx >= 0 {
		return s.synthesizeUpTo(idx + 1) // +1 to include the boundary char
	}
	return nil
}

func (s *session) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.textBuffer.Len() == 0 {
		return nil
	}
	return s.synthesizeAndClear()
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return s.lastErr
	}
	s.closed = true

	if s.textBuffer.Len() > 0 {
		if err := s.synthesizeAndClear(); err != nil && s.lastErr == nil {
			s.lastErr = err
		}
	}

	if err := s.sink.Drain(s.ctx); err != nil && s.lastErr == nil {
		s.lastErr = err
	}

	return s.lastErr
}

func (s *session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// ResetBuffer drops any buffered text not yet synthesized.
func (s *session) ResetBuffer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.textBuffer.Reset()
}

// synthesizeAndClear sends buffered text to OpenRouter TTS and writes PCM to sink.
// Must be called with s.mu held.
func (s *session) synthesizeAndClear() error {
	text := s.textBuffer.String()
	s.textBuffer.Reset()

	if strings.TrimSpace(text) == "" {
		return nil
	}

	return s.synthesize(text)
}

// synthesizeUpTo sends text up to the specified index and retains the remainder.
// Must be called with s.mu held.
func (s *session) synthesizeUpTo(endIdx int) error {
	text := s.textBuffer.String()
	if endIdx <= 0 || endIdx > len(text) {
		return nil
	}

	toSend := text[:endIdx]
	remainder := text[endIdx:]

	s.textBuffer.Reset()
	s.textBuffer.WriteString(remainder)

	if strings.TrimSpace(toSend) == "" {
		return nil
	}

	return s.synthesize(toSend)
}

// synthesize sends the given text to OpenRouter TTS API and writes PCM to sink.
// Must be called with s.mu held.
func (s *session) synthesize(text string) error {
	reqBody := speechRequest{
		Model:          s.adapter.model,
		Voice:          s.adapter.voice,
		Input:          text,
		ResponseFormat: "pcm",
	}
	if s.adapter.speed != 1.0 {
		reqBody.Speed = s.adapter.speed
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		s.lastErr = fmt.Errorf("marshal request: %w", err)
		return s.lastErr
	}

	url := fmt.Sprintf("%s/audio/speech", s.adapter.endpoint)
	req, err := http.NewRequestWithContext(s.ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		s.lastErr = fmt.Errorf("create request: %w", err)
		return s.lastErr
	}

	req.Header.Set("Authorization", "Bearer "+s.adapter.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.lastErr = fmt.Errorf("send request: %w", err)
		return s.lastErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		s.lastErr = fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
		return s.lastErr
	}

	// Response is raw PCM s16le audio stream.
	pcmData, err := io.ReadAll(resp.Body)
	if err != nil {
		s.lastErr = fmt.Errorf("read audio: %w", err)
		return s.lastErr
	}

	if len(pcmData) > 0 {
		if err := s.sink.WritePCM(pcmData); err != nil {
			s.lastErr = err
			return fmt.Errorf("write pcm: %w", err)
		}
	}

	return nil
}

func containsSentenceBoundary(text string) bool {
	for _, r := range text {
		switch r {
		case '.', '!', '?', '\n', '。', '！', '？', '；':
			return true
		}
	}
	return false
}

// lastSentenceBoundary returns the index of the last sentence boundary char in text, or -1 if none.
func lastSentenceBoundary(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		switch rune(text[i]) {
		case '.', '!', '?', '\n', '。', '！', '？', '；':
			return i
		}
	}
	return -1
}

type speechRequest struct {
	Model          string  `json:"model"`
	Voice          string  `json:"voice"`
	Input          string  `json:"input"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed,omitempty"`
}
