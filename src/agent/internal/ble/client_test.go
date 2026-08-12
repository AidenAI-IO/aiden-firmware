package ble

import (
	"context"
	"testing"
	"time"
)

func TestRequestDeadlineUsesCallerOrFallback(t *testing.T) {
	now := time.Unix(100, 0)
	if got := requestDeadline(context.Background(), now); !got.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("fallback deadline = %s", got)
	}

	want := now.Add(time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), want)
	defer cancel()
	if got := requestDeadline(ctx, now); !got.Equal(want) {
		t.Fatalf("caller deadline = %s, want %s", got, want)
	}
}
