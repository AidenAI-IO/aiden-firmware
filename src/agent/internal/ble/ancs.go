package ble

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	ancsEventAdded    = 0
	ancsEventModified = 1
	ancsEventRemoved  = 2

	ancsCommandGetNotificationAttributes = 0
	ancsAttributeAppIdentifier           = 0
	ancsAttributeTitle                   = 1
	ancsAttributeSubtitle                = 2
	ancsAttributeMessage                 = 3
	ancsAttributeDate                    = 5
)

var requestedANCSAttributes = []uint8{
	ancsAttributeAppIdentifier,
	ancsAttributeTitle,
	ancsAttributeSubtitle,
	ancsAttributeMessage,
	ancsAttributeDate,
}

type NotificationSourceEvent struct {
	EventID       uint8
	EventFlags    uint8
	CategoryID    uint8
	CategoryCount uint8
	UID           uint32
}

func ParseNotificationSource(data []byte) ([]NotificationSourceEvent, error) {
	if len(data) == 0 || len(data)%8 != 0 {
		return nil, fmt.Errorf("invalid ANCS Notification Source length %d", len(data))
	}
	events := make([]NotificationSourceEvent, 0, len(data)/8)
	for offset := 0; offset < len(data); offset += 8 {
		events = append(events, NotificationSourceEvent{
			EventID:       data[offset],
			EventFlags:    data[offset+1],
			CategoryID:    data[offset+2],
			CategoryCount: data[offset+3],
			UID:           binary.LittleEndian.Uint32(data[offset+4 : offset+8]),
		})
	}
	return events, nil
}

type attributeRequest struct {
	event  NotificationEvent
	buffer []byte
}

type ANCSConsumer struct {
	mu     sync.Mutex
	store  *EventStore
	writer func([]byte) error
	queue  []NotificationEvent
	active *attributeRequest
}

func NewANCSConsumer(store *EventStore) *ANCSConsumer {
	return &ANCSConsumer{store: store}
}

func (c *ANCSConsumer) SetControlPointWriter(writer func([]byte) error) {
	c.mu.Lock()
	c.writer = writer
	c.mu.Unlock()
}

func (c *ANCSConsumer) ResetConnection(reason string) {
	c.mu.Lock()
	var pending []NotificationEvent
	if c.active != nil {
		pending = append(pending, c.active.event)
	}
	pending = append(pending, c.queue...)
	c.active = nil
	c.queue = nil
	c.writer = nil
	c.mu.Unlock()

	for _, event := range pending {
		event.MetadataComplete = false
		event.MetadataError = reason
		c.store.Append(event)
	}
}

func (c *ANCSConsumer) HandleNotificationSource(data []byte) error {
	sourceEvents, err := ParseNotificationSource(data)
	if err != nil {
		return err
	}

	var command []byte
	for _, source := range sourceEvents {
		event := NotificationEvent{
			NotificationUID: source.UID,
			Event:           eventName(source.EventID),
			Flags:           eventFlags(source.EventFlags),
			CategoryID:      source.CategoryID,
			Category:        categoryName(source.CategoryID),
			CategoryCount:   source.CategoryCount,
		}
		if source.EventID == ancsEventRemoved {
			event.MetadataComplete = true
			c.store.Append(event)
			continue
		}

		c.mu.Lock()
		c.queue = append(c.queue, event)
		if command == nil {
			command = c.prepareNextLocked()
		}
		c.mu.Unlock()
	}
	c.dispatch(command)
	return nil
}

func (c *ANCSConsumer) HandleDataSource(data []byte) error {
	c.mu.Lock()
	if c.active == nil {
		c.mu.Unlock()
		return errors.New("ANCS Data Source response without an active request")
	}
	c.active.buffer = append(c.active.buffer, data...)
	attributes, complete, err := parseAttributeResponse(c.active.buffer, c.active.event.NotificationUID)
	if err != nil {
		event := c.active.event
		event.MetadataError = err.Error()
		c.active = nil
		command := c.prepareNextLocked()
		c.mu.Unlock()
		c.store.Append(event)
		c.dispatch(command)
		return err
	}
	if !complete {
		c.mu.Unlock()
		return nil
	}

	event := c.active.event
	event.AppIdentifier = attributes[ancsAttributeAppIdentifier]
	event.Title = attributes[ancsAttributeTitle]
	event.Subtitle = attributes[ancsAttributeSubtitle]
	event.Message = attributes[ancsAttributeMessage]
	event.Date = attributes[ancsAttributeDate]
	event.MetadataComplete = true
	c.active = nil
	command := c.prepareNextLocked()
	c.mu.Unlock()

	c.store.Append(event)
	c.dispatch(command)
	return nil
}

func (c *ANCSConsumer) prepareNextLocked() []byte {
	if c.active != nil || len(c.queue) == 0 {
		return nil
	}
	event := c.queue[0]
	c.queue = c.queue[1:]
	c.active = &attributeRequest{event: event}
	return buildNotificationAttributeRequest(event.NotificationUID)
}

func (c *ANCSConsumer) dispatch(command []byte) {
	for len(command) > 0 {
		c.mu.Lock()
		writer := c.writer
		c.mu.Unlock()
		var err error
		if writer == nil {
			err = errors.New("ANCS Control Point is unavailable")
		} else {
			err = writer(command)
		}
		if err == nil {
			return
		}

		c.mu.Lock()
		if c.active == nil {
			c.mu.Unlock()
			return
		}
		event := c.active.event
		event.MetadataError = err.Error()
		c.active = nil
		command = c.prepareNextLocked()
		c.mu.Unlock()
		c.store.Append(event)
	}
}

func buildNotificationAttributeRequest(uid uint32) []byte {
	request := make([]byte, 0, 18)
	request = append(request, ancsCommandGetNotificationAttributes)
	uidBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(uidBytes, uid)
	request = append(request, uidBytes...)
	request = append(request, ancsAttributeAppIdentifier)
	request = append(request, ancsAttributeTitle, 128, 0)
	request = append(request, ancsAttributeSubtitle, 128, 0)
	request = append(request, ancsAttributeMessage, 0, 2)
	request = append(request, ancsAttributeDate)
	return request
}

func parseAttributeResponse(data []byte, expectedUID uint32) (map[uint8]string, bool, error) {
	if len(data) < 5 {
		return nil, false, nil
	}
	if data[0] != ancsCommandGetNotificationAttributes {
		return nil, false, fmt.Errorf("unexpected ANCS command id %d", data[0])
	}
	uid := binary.LittleEndian.Uint32(data[1:5])
	if uid != expectedUID {
		return nil, false, fmt.Errorf("ANCS response UID %d does not match %d", uid, expectedUID)
	}

	attributes := make(map[uint8]string)
	offset := 5
	for offset < len(data) {
		if len(data)-offset < 3 {
			return nil, false, nil
		}
		attributeID := data[offset]
		length := int(binary.LittleEndian.Uint16(data[offset+1 : offset+3]))
		offset += 3
		if len(data)-offset < length {
			return nil, false, nil
		}
		attributes[attributeID] = strings.ToValidUTF8(string(data[offset:offset+length]), "�")
		offset += length
	}
	for _, attributeID := range requestedANCSAttributes {
		if _, ok := attributes[attributeID]; !ok {
			return nil, false, nil
		}
	}
	return attributes, true, nil
}

func eventName(eventID uint8) string {
	switch eventID {
	case ancsEventAdded:
		return "added"
	case ancsEventModified:
		return "modified"
	case ancsEventRemoved:
		return "removed"
	default:
		return fmt.Sprintf("unknown_%d", eventID)
	}
}

func eventFlags(flags uint8) []string {
	result := make([]string, 0, 5)
	for bit, name := range []string{"silent", "important", "pre_existing", "positive_action", "negative_action"} {
		if flags&(1<<uint(bit)) != 0 {
			result = append(result, name)
		}
	}
	return result
}

func categoryName(categoryID uint8) string {
	names := []string{
		"other", "incoming_call", "missed_call", "voicemail", "social",
		"schedule", "email", "news", "health_and_fitness", "business_and_finance",
		"location", "entertainment",
	}
	if int(categoryID) < len(names) {
		return names[categoryID]
	}
	return fmt.Sprintf("unknown_%d", categoryID)
}
