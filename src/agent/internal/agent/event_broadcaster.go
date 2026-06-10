package agent

import "sync"

// EventBroadcaster fans out messages to multiple subscribers via channels.
// Slow subscribers (channel full) are skipped so a single stuck client cannot
// block message persistence or other broadcasters.
type EventBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan Message]struct{}
}

const eventSubscriberBufferSize = 16

func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{
		subscribers: make(map[chan Message]struct{}),
	}
}

// Subscribe returns a buffered channel that receives every Broadcast call.
// Callers must Unsubscribe when done; the broadcaster does not garbage-collect
// orphaned channels.
func (b *EventBroadcaster) Subscribe() chan Message {
	ch := make(chan Message, eventSubscriberBufferSize)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel from the subscriber set and closes it. Safe
// to call multiple times — subsequent calls are no-ops.
func (b *EventBroadcaster) Unsubscribe(ch chan Message) {
	b.mu.Lock()
	if _, ok := b.subscribers[ch]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.subscribers, ch)
	b.mu.Unlock()
	close(ch)
}

// Broadcast sends msg to every current subscriber. If a subscriber's channel
// is full the message is dropped for that subscriber rather than blocking.
func (b *EventBroadcaster) Broadcast(msg Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- msg:
		default:
			// Subscriber not draining fast enough — drop the message for them.
		}
	}
}
