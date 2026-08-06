package ble

import (
	"strconv"
	"sync"
	"time"
)

// NotificationEvent is the normalized, memory-independent representation of
// one ANCS notification change. IDs are decimal strings so every UDS client can
// safely retain cursors without losing uint64 precision.
type NotificationEvent struct {
	ID               string   `json:"id"`
	NotificationUID  uint32   `json:"notification_uid"`
	Event            string   `json:"event"`
	Flags            []string `json:"flags"`
	CategoryID       uint8    `json:"category_id"`
	Category         string   `json:"category"`
	CategoryCount    uint8    `json:"category_count"`
	AppIdentifier    string   `json:"app_identifier,omitempty"`
	Title            string   `json:"title,omitempty"`
	Subtitle         string   `json:"subtitle,omitempty"`
	Message          string   `json:"message,omitempty"`
	Date             string   `json:"date,omitempty"`
	MetadataComplete bool     `json:"metadata_complete"`
	MetadataError    string   `json:"metadata_error,omitempty"`
	ReceivedAt       string   `json:"received_at"`
	sequence         uint64
}

type EventPage struct {
	Events    []NotificationEvent `json:"events"`
	Truncated bool                `json:"truncated"`
	OldestID  string              `json:"oldest_id"`
	LastID    string              `json:"last_id"`
}

type EventStats struct {
	Count    int
	OldestID string
	LastID   string
}

type EventStore struct {
	mu       sync.RWMutex
	capacity int
	next     uint64
	events   []NotificationEvent
}

func NewEventStore(capacity int) *EventStore {
	if capacity <= 0 {
		capacity = 512
	}
	return &EventStore{capacity: capacity}
}

func (s *EventStore) Append(event NotificationEvent) NotificationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.next++
	event.sequence = s.next
	event.ID = strconv.FormatUint(s.next, 10)
	if event.ReceivedAt == "" {
		event.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Flags == nil {
		event.Flags = []string{}
	}
	s.events = append(s.events, event)
	if len(s.events) > s.capacity {
		overflow := len(s.events) - s.capacity
		copy(s.events, s.events[overflow:])
		s.events = s.events[:s.capacity]
	}
	return event
}

func (s *EventStore) Page(since uint64, limit int) EventPage {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	page := EventPage{Events: []NotificationEvent{}}
	if len(s.events) == 0 {
		return page
	}
	oldest := s.events[0].sequence
	last := s.events[len(s.events)-1].sequence
	page.OldestID = strconv.FormatUint(oldest, 10)
	page.LastID = strconv.FormatUint(last, 10)
	page.Truncated = since > 0 && oldest > since && oldest-since > 1

	for _, event := range s.events {
		if event.sequence <= since {
			continue
		}
		page.Events = append(page.Events, event)
		if len(page.Events) == limit {
			break
		}
	}
	return page
}

func (s *EventStore) Stats() EventStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := EventStats{Count: len(s.events)}
	if len(s.events) > 0 {
		stats.OldestID = strconv.FormatUint(s.events[0].sequence, 10)
		stats.LastID = strconv.FormatUint(s.events[len(s.events)-1].sequence, 10)
	}
	return stats
}
