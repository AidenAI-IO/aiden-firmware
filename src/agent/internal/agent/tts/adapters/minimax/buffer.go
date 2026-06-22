package minimax

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxChunkRunes = 90
	minChunkRunes = 18
)

// sentenceBuffer accumulates incoming text and yields chunks at sentence
// boundaries so non-incremental providers can consume LLM token streams.
// Thread-safe.
type sentenceBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

// Write appends text and returns any complete chunks ready to synthesize.
func (b *sentenceBuffer) Write(text string) []string {
	if text == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(text)

	var out []string
	for {
		chunk, ok := nextChunk(b.buf.String(), false)
		if !ok {
			break
		}
		out = append(out, chunk)
		rest := b.buf.String()[len(chunk):]
		b.buf.Reset()
		b.buf.WriteString(rest)
	}
	return out
}

// Flush returns any remaining buffered text, regardless of length or boundary.
func (b *sentenceBuffer) Flush() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	rest := strings.TrimSpace(b.buf.String())
	b.buf.Reset()
	return rest
}

// Reset discards any buffered text without synthesizing it. Used to drop
// residual content left over from a turn that did not produce a final answer.
func (b *sentenceBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// nextChunk extracts one sentence-bounded chunk if possible.
// Returns the chunk and true; if no boundary is reached and flush=false,
// returns "", false. With flush=true, returns whatever remains.
func nextChunk(text string, flush bool) (string, bool) {
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
		if isHardBoundary(r) && runeCount >= minChunkRunes {
			_, size := utf8.DecodeRuneInString(text[idx:])
			return text[:idx+size], true
		}
		if isSoftBoundary(r) {
			_, size := utf8.DecodeRuneInString(text[idx:])
			lastSoftBreak = idx + size
		}
		if runeCount >= maxChunkRunes {
			if lastSoftBreak > 0 {
				return text[:lastSoftBreak], true
			}
			_, size := utf8.DecodeRuneInString(text[idx:])
			return text[:idx+size], true
		}
	}
	return "", false
}

func isHardBoundary(r rune) bool {
	switch r {
	case '.', '!', '?', ';', '\n', '。', '！', '？', '；':
		return true
	}
	return false
}

func isSoftBoundary(r rune) bool {
	switch r {
	case ',', ':', '，', '、', '：':
		return true
	}
	return false
}
