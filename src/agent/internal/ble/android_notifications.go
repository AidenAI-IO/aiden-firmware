package ble

import (
	"fmt"
	"hash/fnv"
	"strings"
)

const (
	maxPublishedNotifications = 8
	maxPhoneIDLength          = 128
	maxSourceIDLength         = 512
	maxSourceEventIDLength    = 128
	maxAppIdentifierLength    = 255
	maxNotificationTitle      = 512
	maxNotificationSubtitle   = 512
	maxNotificationMessage    = 4096
	maxNotificationCategory   = 128
	maxNotificationDate       = 64
	maxNotificationFlags      = 16
	maxNotificationFlagLength = 64
)

type NotificationPublishResult struct {
	Accepted   int    `json:"accepted"`
	Duplicates int    `json:"duplicates"`
	LastID     string `json:"last_id"`
}

// PublishAndroidNotifications validates and appends notification events sent
// by the Android companion app over the USB-restricted Agent HTTP endpoint.
func (s *Service) PublishAndroidNotifications(
	phoneID string,
	events []NotificationEvent,
) (NotificationPublishResult, error) {
	normalized, err := normalizeAndroidNotifications(phoneID, events)
	if err != nil {
		return NotificationPublishResult{}, err
	}

	result := NotificationPublishResult{}
	for _, event := range normalized {
		_, created := s.store.AppendUnique(event)
		if created {
			result.Accepted++
		} else {
			result.Duplicates++
		}
	}
	result.LastID = s.store.Stats().LastID
	return result, nil
}

func normalizeAndroidNotifications(
	phoneID string,
	events []NotificationEvent,
) ([]NotificationEvent, error) {
	phoneID = strings.TrimSpace(phoneID)
	if phoneID == "" || len(phoneID) > maxPhoneIDLength {
		return nil, fmt.Errorf("phone_id must contain 1-%d bytes", maxPhoneIDLength)
	}
	if len(events) == 0 || len(events) > maxPublishedNotifications {
		return nil, fmt.Errorf("events must contain 1-%d items", maxPublishedNotifications)
	}

	normalized := make([]NotificationEvent, 0, len(events))
	for index, input := range events {
		event := input
		event.Event = strings.TrimSpace(event.Event)
		if event.Event != "added" && event.Event != "modified" && event.Event != "removed" {
			return nil, fmt.Errorf("events[%d].event must be added, modified, or removed", index)
		}
		event.SourceID = strings.TrimSpace(event.SourceID)
		if event.SourceID == "" || len(event.SourceID) > maxSourceIDLength {
			return nil, fmt.Errorf("events[%d].source_id must contain 1-%d bytes", index, maxSourceIDLength)
		}
		event.SourceEventID = strings.TrimSpace(event.SourceEventID)
		if event.SourceEventID == "" || len(event.SourceEventID) > maxSourceEventIDLength {
			return nil, fmt.Errorf(
				"events[%d].source_event_id must contain 1-%d bytes",
				index,
				maxSourceEventIDLength,
			)
		}
		event.AppIdentifier = strings.TrimSpace(event.AppIdentifier)
		if event.AppIdentifier == "" || len(event.AppIdentifier) > maxAppIdentifierLength {
			return nil, fmt.Errorf(
				"events[%d].app_identifier must contain 1-%d bytes",
				index,
				maxAppIdentifierLength,
			)
		}
		if err := validateNotificationText(index, "title", event.Title, maxNotificationTitle); err != nil {
			return nil, err
		}
		if err := validateNotificationText(index, "subtitle", event.Subtitle, maxNotificationSubtitle); err != nil {
			return nil, err
		}
		if err := validateNotificationText(index, "message", event.Message, maxNotificationMessage); err != nil {
			return nil, err
		}
		if err := validateNotificationText(index, "category", event.Category, maxNotificationCategory); err != nil {
			return nil, err
		}
		if err := validateNotificationText(index, "date", event.Date, maxNotificationDate); err != nil {
			return nil, err
		}
		if len(event.Flags) > maxNotificationFlags {
			return nil, fmt.Errorf("events[%d].flags exceeds %d items", index, maxNotificationFlags)
		}
		for flagIndex, flag := range event.Flags {
			event.Flags[flagIndex] = strings.TrimSpace(flag)
			if event.Flags[flagIndex] == "" || len(event.Flags[flagIndex]) > maxNotificationFlagLength {
				return nil, fmt.Errorf(
					"events[%d].flags[%d] must contain 1-%d bytes",
					index,
					flagIndex,
					maxNotificationFlagLength,
				)
			}
		}
		if event.NotificationUID == 0 {
			event.NotificationUID = notificationSourceHash(event.SourceID)
		}
		event.ID = ""
		event.Source = "android"
		event.DeviceID = phoneID
		event.MetadataComplete = true
		event.MetadataError = ""
		event.ReceivedAt = ""
		event.sequence = 0
		if event.Flags == nil {
			event.Flags = []string{}
		}
		normalized = append(normalized, event)
	}
	return normalized, nil
}

func validateNotificationText(index int, field, value string, maximum int) error {
	if len(value) > maximum {
		return fmt.Errorf("events[%d].%s exceeds %d bytes", index, field, maximum)
	}
	return nil
}

func notificationSourceHash(sourceID string) uint32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(sourceID))
	value := hash.Sum32()
	if value == 0 {
		return 1
	}
	return value
}
