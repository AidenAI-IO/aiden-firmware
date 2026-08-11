package ble

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

// NotificationEvent is the normalized, memory-independent representation of
// one phone notification change. IDs are decimal strings so every UDS client
// can safely retain cursors without losing uint64 precision.
type NotificationEvent struct {
	ID               string   `json:"id"`
	Source           string   `json:"source,omitempty"`
	SourceID         string   `json:"source_id,omitempty"`
	SourceEventID    string   `json:"source_event_id,omitempty"`
	DeviceID         string   `json:"device_id,omitempty"`
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
	Events        []NotificationEvent `json:"events"`
	Generation    string              `json:"generation"`
	ResetRequired bool                `json:"reset_required"`
	Truncated     bool                `json:"truncated"`
	OldestID      string              `json:"oldest_id"`
	LastID        string              `json:"last_id"`
}

type EventStats struct {
	Count      int
	Generation string
	OldestID   string
	LastID     string
}

type EventStore struct {
	mu         sync.RWMutex
	capacity   int
	generation string
	next       uint64
	events     []NotificationEvent
	sourceSeen map[notificationDedupeKey]NotificationEvent
}

type notificationDedupeKey struct {
	deviceID      string
	sourceEventID string
}

func NewEventStore(capacity int) *EventStore {
	if capacity <= 0 {
		capacity = 512
	}
	return &EventStore{
		capacity:   capacity,
		generation: newEventGeneration(),
		sourceSeen: make(map[notificationDedupeKey]NotificationEvent),
	}
}

func newEventGeneration() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err == nil {
		return hex.EncodeToString(value)
	}
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
}

func (s *EventStore) Append(event NotificationEvent) NotificationEvent {
	appended, _ := s.AppendUnique(event)
	return appended
}

// AppendUnique appends an event unless its source_event_id is still present in
// the bounded ring. External publishers use this to retry safely after an HTTP
// timeout without duplicating notification changes.
func (s *EventStore) AppendUnique(event NotificationEvent) (NotificationEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dedupeKey, hasDedupeKey := notificationEventDedupeKey(event)
	if hasDedupeKey {
		if existing, ok := s.sourceSeen[dedupeKey]; ok {
			return existing, false
		}
	}

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
	if hasDedupeKey {
		s.sourceSeen[dedupeKey] = event
	}
	if len(s.events) > s.capacity {
		overflow := len(s.events) - s.capacity
		for _, expired := range s.events[:overflow] {
			expiredKey, hasExpiredKey := notificationEventDedupeKey(expired)
			if !hasExpiredKey {
				continue
			}
			if seen, ok := s.sourceSeen[expiredKey]; ok && seen.sequence == expired.sequence {
				delete(s.sourceSeen, expiredKey)
			}
		}
		copy(s.events, s.events[overflow:])
		s.events = s.events[:s.capacity]
	}
	return event, true
}

func notificationEventDedupeKey(event NotificationEvent) (notificationDedupeKey, bool) {
	if event.SourceEventID == "" {
		return notificationDedupeKey{}, false
	}
	return notificationDedupeKey{
		deviceID:      event.DeviceID,
		sourceEventID: event.SourceEventID,
	}, true
}

func (s *EventStore) Page(since uint64, limit int) EventPage {
	return s.PageForGeneration(since, limit, s.Generation())
}

func (s *EventStore) PageForGeneration(since uint64, limit int, generation string) EventPage {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	page := EventPage{Events: []NotificationEvent{}, Generation: s.generation}
	if len(s.events) == 0 {
		page.ResetRequired = since > 0 && generation != s.generation
		return page
	}
	oldest := s.events[0].sequence
	last := s.events[len(s.events)-1].sequence
	page.OldestID = strconv.FormatUint(oldest, 10)
	page.LastID = strconv.FormatUint(last, 10)
	if since > 0 && generation != s.generation {
		page.ResetRequired = true
		return page
	}
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

func (s *EventStore) Generation() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

func (s *EventStore) Stats() EventStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := EventStats{Count: len(s.events), Generation: s.generation}
	if len(s.events) > 0 {
		stats.OldestID = strconv.FormatUint(s.events[0].sequence, 10)
		stats.LastID = strconv.FormatUint(s.events[len(s.events)-1].sequence, 10)
	}
	return stats
}
