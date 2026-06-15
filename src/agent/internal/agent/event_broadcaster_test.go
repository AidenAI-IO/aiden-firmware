package agent

import (
	"sync"
	"testing"
	"time"
)

func TestEventBroadcasterSubscribeUnsubscribe(t *testing.T) {
	broadcaster := NewEventBroadcaster()

	ch := broadcaster.Subscribe()
	if ch == nil {
		t.Fatal("Subscribe returned nil channel")
	}

	broadcaster.Unsubscribe(ch)

	// Channel should be closed after unsubscribe — receive returns zero value, ok=false
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Channel should be closed after Unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Reading from closed channel should not block")
	}
}

func TestEventBroadcasterBroadcastsToAllSubscribers(t *testing.T) {
	broadcaster := NewEventBroadcaster()

	ch1 := broadcaster.Subscribe()
	ch2 := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch1)
	defer broadcaster.Unsubscribe(ch2)

	want := Message{Type: "user", Content: "test message"}
	broadcaster.Broadcast(want)

	for i, ch := range []chan Message{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Content != want.Content {
				t.Errorf("subscriber %d: got content %q, want %q", i, got.Content, want.Content)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: timeout waiting for broadcast", i)
		}
	}
}

func TestEventBroadcasterDropsForSlowSubscribers(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	ch := broadcaster.Subscribe()
	defer broadcaster.Unsubscribe(ch)

	// Send more messages than buffer (16); broadcaster must not block on slow subscribers.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			broadcaster.Broadcast(Message{Type: "user", Content: "msg"})
		}
		close(done)
	}()

	select {
	case <-done:
		// Expected: broadcaster returned without blocking even though we never read from ch.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Broadcast blocked on slow subscriber — should drop instead")
	}
}

func TestEventBroadcasterUnsubscribeIsIdempotent(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	ch := broadcaster.Subscribe()

	broadcaster.Unsubscribe(ch)
	// Second call must not panic (e.g., closing an already-closed channel).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Unsubscribe panicked: %v", r)
		}
	}()
	broadcaster.Unsubscribe(ch)
}

func TestEventBroadcasterConcurrentSubscribeBroadcast(t *testing.T) {
	broadcaster := NewEventBroadcaster()
	const subscribers = 20
	const messages = 50

	chans := make([]chan Message, subscribers)
	for i := range chans {
		chans[i] = broadcaster.Subscribe()
	}
	defer func() {
		for _, ch := range chans {
			broadcaster.Unsubscribe(ch)
		}
	}()

	// Drain in parallel to keep buffers from filling.
	var wg sync.WaitGroup
	for _, ch := range chans {
		wg.Add(1)
		go func(c chan Message) {
			defer wg.Done()
			for range c {
			}
		}(ch)
	}

	for i := 0; i < messages; i++ {
		broadcaster.Broadcast(Message{Type: "user", Content: "concurrent"})
	}

	// Close subscribers to unblock drainers.
	for _, ch := range chans {
		broadcaster.Unsubscribe(ch)
	}
	wg.Wait()
}
