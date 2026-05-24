package agent

import (
	"context"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	streamingSpeechMaxChunkRunes = 90
	streamingSpeechMinChunkRunes = 18
)

type streamingSpeechWriter struct {
	ctx    context.Context
	dialog *AudioDialog

	mu      sync.Mutex
	buffer  strings.Builder
	spoken  bool
	closed  bool
	chunks  chan string
	done    chan error
	lastErr error
}

func newStreamingSpeechWriter(ctx context.Context, dialog *AudioDialog) *streamingSpeechWriter {
	if ctx == nil {
		ctx = context.Background()
	}
	w := &streamingSpeechWriter{
		ctx:    ctx,
		dialog: dialog,
		chunks: make(chan string, 8),
		done:   make(chan error, 1),
	}
	go w.speakLoop()
	return w
}

func (w *streamingSpeechWriter) Write(p []byte) (int, error) {
	text := string(p)
	if text == "" {
		return len(p), nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(p), nil
	}
	w.buffer.WriteString(text)
	for {
		chunk, ok := nextSpeechChunk(w.buffer.String(), false)
		if !ok {
			break
		}
		w.consumeLocked(len(chunk))
		w.enqueueLocked(chunk)
	}
	return len(p), nil
}

func (w *streamingSpeechWriter) CloseAndWait() error {
	w.mu.Lock()
	if !w.closed {
		if chunk := strings.TrimSpace(w.buffer.String()); chunk != "" {
			w.buffer.Reset()
			w.enqueueLocked(chunk)
		}
		w.closed = true
		close(w.chunks)
	}
	w.mu.Unlock()

	err := <-w.done
	w.mu.Lock()
	if w.lastErr == nil {
		w.lastErr = err
	}
	lastErr := w.lastErr
	w.mu.Unlock()
	return lastErr
}

func (w *streamingSpeechWriter) Spoke() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spoken
}

func (w *streamingSpeechWriter) consumeLocked(bytesLen int) {
	rest := w.buffer.String()[bytesLen:]
	w.buffer.Reset()
	w.buffer.WriteString(rest)
}

func (w *streamingSpeechWriter) enqueueLocked(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	select {
	case w.chunks <- text:
	case <-w.ctx.Done():
		if w.lastErr == nil {
			w.lastErr = w.ctx.Err()
		}
	}
}

func (w *streamingSpeechWriter) speakLoop() {
	var firstErr error
	for chunk := range w.chunks {
		if firstErr != nil {
			continue
		}
		if err := w.dialog.Speak(w.ctx, chunk, nil); err != nil {
			firstErr = err
			continue
		}
		w.mu.Lock()
		w.spoken = true
		w.mu.Unlock()
	}
	w.done <- firstErr
}

func nextSpeechChunk(text string, flush bool) (string, bool) {
	if text == "" {
		return "", false
	}
	if flush {
		return text, true
	}

	runeCount := 0
	lastSoftBreak := -1
	for idx, r := range text {
		runeCount++
		if isSpeechHardBoundary(r) && runeCount >= streamingSpeechMinChunkRunes {
			_, size := utf8.DecodeRuneInString(text[idx:])
			return text[:idx+size], true
		}
		if isSpeechSoftBoundary(r) {
			_, size := utf8.DecodeRuneInString(text[idx:])
			lastSoftBreak = idx + size
		}
		if runeCount >= streamingSpeechMaxChunkRunes {
			if lastSoftBreak > 0 {
				return text[:lastSoftBreak], true
			}
			_, size := utf8.DecodeRuneInString(text[idx:])
			return text[:idx+size], true
		}
	}
	return "", false
}

func isSpeechHardBoundary(r rune) bool {
	switch r {
	case '.', '!', '?', ';', '\n', '。', '！', '？', '；':
		return true
	default:
		return false
	}
}

func isSpeechSoftBoundary(r rune) bool {
	switch r {
	case ',', ':', '，', '、', '：':
		return true
	default:
		return false
	}
}
