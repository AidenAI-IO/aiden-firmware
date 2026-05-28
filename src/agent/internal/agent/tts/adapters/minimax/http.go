package minimax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"aiden-agent/internal/agent/tts"
)

const (
	httpEndpoint = "https://api.minimaxi.com/v1/t2a_v2"
)

// HTTPAdapter implements TTSProvider using Minimax HTTP streaming API.
type HTTPAdapter struct {
	cfg        commonConfig
	httpClient *http.Client
}

var _ tts.TTSProvider = (*HTTPAdapter)(nil)

// NewHTTP creates a Minimax HTTP provider.
func NewHTTP(cfg tts.ProviderConfig) (tts.TTSProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("minimax: api_key is required")
	}
	c := parseConfig(cfg, httpEndpoint)
	return &HTTPAdapter{
		cfg:        c,
		httpClient: httpClientForConfig(c),
	}, nil
}

func (a *HTTPAdapter) Name() string                   { return "minimax" }
func (a *HTTPAdapter) Capabilities() tts.Capabilities { return capabilities() }
func (a *HTTPAdapter) Close() error                   { return nil }

func (a *HTTPAdapter) BeginStream(ctx context.Context, sink tts.AudioSink) (tts.StreamSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return &httpSession{
		ctx:        ctx,
		cfg:        a.cfg,
		sink:       sink,
		httpClient: a.httpClient,
		buf:        &sentenceBuffer{},
	}, nil
}

// httpSession buffers text to sentence boundaries, then POSTs each sentence.
type httpSession struct {
	ctx        context.Context
	cfg        commonConfig
	sink       tts.AudioSink
	httpClient *http.Client
	buf        *sentenceBuffer

	errMu   sync.Mutex
	lastErr error
}

func (s *httpSession) WriteText(text string) error {
	chunks := s.buf.Write(text)
	for _, chunk := range chunks {
		if err := s.synthesizeChunk(chunk); err != nil {
			s.recordErr(err)
			return err
		}
	}
	return nil
}

func (s *httpSession) Flush() error {
	if rest := s.buf.Flush(); rest != "" {
		if err := s.synthesizeChunk(rest); err != nil {
			s.recordErr(err)
			return err
		}
	}
	return nil
}

func (s *httpSession) Close() error {
	if err := s.Flush(); err != nil {
		return err
	}
	if err := s.sink.Drain(context.Background()); err != nil {
		s.recordErr(err)
	}
	return s.Err()
}

func (s *httpSession) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

func (s *httpSession) recordErr(err error) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.lastErr == nil {
		s.lastErr = err
	}
}

func (s *httpSession) synthesizeChunk(text string) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}

	reqBody := map[string]any{
		"model":  s.cfg.model,
		"text":   text,
		"stream": true,
		"voice_setting": map[string]any{
			"voice_id": s.cfg.voiceID,
			"speed":    s.cfg.speed,
			"vol":      1.0,
			"pitch":    0,
			"emotion":  s.cfg.emotion,
		},
		"audio_setting": map[string]any{
			"sample_rate": defaultSampleRate,
			"format":      "pcm",
			"channel":     defaultChannels,
		},
		"stream_options": map[string]any{
			"exclude_aggregated_audio": true,
		},
		"subtitle_enable": false,
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(s.ctx, "POST", s.cfg.endpoint, bytes.NewReader(reqData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Parse streaming JSON response
	parser := &streamParser{}
	readBuf := make([]byte, 8192)
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		default:
		}
		n, rerr := resp.Body.Read(readBuf)
		if n > 0 {
			chunks := parser.feed(readBuf[:n])
			for _, pcm := range chunks {
				if err := s.sink.WritePCM(pcm); err != nil {
					return err
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				if s.ctx.Err() != nil {
					return s.ctx.Err()
				}
				return fmt.Errorf("read response: %w", rerr)
			}
			break
		}
	}

	log.Printf("[tts] minimax: synthesized %d chars\n", len(text))
	return nil
}

// streamParser parses concatenated JSON objects from Minimax streaming response.
type streamParser struct {
	buffer []byte
}

func (p *streamParser) feed(data []byte) [][]byte {
	p.buffer = append(p.buffer, data...)
	var out [][]byte
	for {
		start := bytes.IndexByte(p.buffer, '{')
		if start < 0 {
			p.buffer = p.buffer[:0]
			break
		}
		if start > 0 {
			p.buffer = p.buffer[start:]
		}
		end, complete := findJSONEnd(p.buffer)
		if !complete {
			break
		}
		var obj struct {
			Data struct {
				Audio string `json:"audio"`
			} `json:"data"`
		}
		if err := json.Unmarshal(p.buffer[:end], &obj); err == nil {
			if obj.Data.Audio != "" {
				if pcm := hexDecode(obj.Data.Audio); len(pcm) > 0 {
					out = append(out, pcm)
				}
			}
		}
		p.buffer = p.buffer[end:]
	}
	return out
}

func findJSONEnd(buf []byte) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i, c := range buf {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}
