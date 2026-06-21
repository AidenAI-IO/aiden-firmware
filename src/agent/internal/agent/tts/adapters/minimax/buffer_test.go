package minimax

import "testing"

// TestSentenceBufferResetDropsResidual verifies that Reset discards
// sub-threshold text the buffer is still holding, so it cannot leak into a
// later Write/Flush cycle. This guards the tool-call-speech duplication fix:
// a short tool-call preamble such as "我查下天气。" (< minChunkRunes) must not
// survive to be prepended to a subsequent final answer.
func TestSentenceBufferResetDropsResidual(t *testing.T) {
	var b sentenceBuffer

	// Short text below the chunk threshold stays buffered, nothing emitted.
	if chunks := b.Write("我查下天气。"); len(chunks) != 0 {
		t.Fatalf("expected no chunks for sub-threshold text, got %v", chunks)
	}

	// Reset drops the residual.
	b.Reset()

	if rest := b.Flush(); rest != "" {
		t.Fatalf("expected empty buffer after Reset, got %q", rest)
	}
}

// TestSentenceBufferResetThenWriteIsClean verifies a Write after Reset is not
// contaminated by previously buffered residual.
func TestSentenceBufferResetThenWriteIsClean(t *testing.T) {
	var b sentenceBuffer

	b.Write("我查下天气。") // residual, sub-threshold
	b.Reset()

	b.Write("上海今天有小雨。")
	if rest := b.Flush(); rest != "上海今天有小雨。" {
		t.Fatalf("expected only post-reset text, got %q", rest)
	}
}
