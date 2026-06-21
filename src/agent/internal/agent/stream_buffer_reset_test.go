package agent

import (
	"bytes"
	"testing"
)

// bufferResetRecorder is a fake terminal writer that records whether
// ResetBuffer reached it through the streaming writer chain.
type bufferResetRecorder struct {
	resetCalls int
}

func (r *bufferResetRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (r *bufferResetRecorder) ResetBuffer()                { r.resetCalls++ }

// TestResetStreamBufferReachesTerminalThroughChain verifies that a buffer
// reset propagates through the JSON-field-or-plain wrapper and the
// cancel-on-first-write wrapper down to the underlying TTS sink. This is the
// chain used for streaming voice replies; without propagation, residual
// tool-call content would survive into the next turn and be spoken twice.
func TestResetStreamBufferReachesTerminalThroughChain(t *testing.T) {
	terminal := &bufferResetRecorder{}
	jsonWriter := NewJSONFieldOrPlainStreamWriter(terminal, "final_answer")
	chain := newCancelOnFirstWriteWriter(jsonWriter, func() {})

	resetStreamBuffer(chain)

	if terminal.resetCalls != 1 {
		t.Fatalf("expected ResetBuffer to reach terminal once, got %d", terminal.resetCalls)
	}
}

// TestResetStreamBufferNoopWithoutSupport verifies that resetStreamBuffer is a
// safe no-op when the writer chain does not support buffer resetting.
func TestResetStreamBufferNoopWithoutSupport(t *testing.T) {
	// A plain writer with no ResetBuffer method must not panic.
	resetStreamBuffer(&bytes.Buffer{})
	resetStreamBuffer(nil)
}
