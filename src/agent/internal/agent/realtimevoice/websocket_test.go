package realtimevoice

import (
	"context"
	"strings"
	"testing"
)

func TestJSONWebSocketTransportWriteHonorsCanceledContext(t *testing.T) {
	transport := writeStateTestTransport()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := transport.writeJSON(ctx, map[string]string{"type": "test"}); err != context.Canceled {
		t.Fatalf("writeJSON error = %v, want context.Canceled", err)
	}
}

func TestJSONWebSocketTransportWriteRejectsClosedSession(t *testing.T) {
	transport := writeStateTestTransport()
	close(transport.done)

	err := transport.writeJSON(context.Background(), map[string]string{"type": "test"})
	if err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("writeJSON error = %v, want closed session", err)
	}
}

func writeStateTestTransport() *jsonWebSocketTransport {
	transport := &jsonWebSocketTransport{
		label:     "test realtime",
		done:      make(chan struct{}),
		writeGate: make(chan struct{}, 1),
	}
	transport.writeGate <- struct{}{}
	return transport
}
