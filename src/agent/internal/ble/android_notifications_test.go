package ble

import "testing"

func TestPublishAndroidNotificationsNormalizesAndDeduplicates(t *testing.T) {
	service := NewService(8)
	input := []NotificationEvent{{
		SourceID:      "0|com.example.mail|42|message",
		SourceEventID: "event-1",
		Event:         "added",
		AppIdentifier: "com.example.mail",
		Title:         "New message",
		Message:       "Hello",
		Category:      "msg",
		CategoryCount: 1,
		Flags:         []string{"clearable"},
	}}

	result, err := service.PublishAndroidNotifications("android-phone-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || result.Duplicates != 0 || result.LastID != "1" {
		t.Fatalf("unexpected publish result: %#v", result)
	}
	page := service.EventsSince(0, 10, "")
	if len(page.Events) != 1 {
		t.Fatalf("event count=%d", len(page.Events))
	}
	event := page.Events[0]
	if event.Source != "android" || event.DeviceID != "android-phone-1" ||
		event.SourceID != input[0].SourceID || event.NotificationUID == 0 ||
		!event.MetadataComplete {
		t.Fatalf("event was not normalized: %#v", event)
	}

	result, err = service.PublishAndroidNotifications("android-phone-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 0 || result.Duplicates != 1 || result.LastID != "1" {
		t.Fatalf("retry was not deduplicated: %#v", result)
	}
	if count := service.store.Stats().Count; count != 1 {
		t.Fatalf("deduplicated store count=%d", count)
	}
}

func TestPublishAndroidNotificationsValidatesBatch(t *testing.T) {
	service := NewService(8)
	tests := []struct {
		name    string
		phoneID string
		events  []NotificationEvent
	}{
		{name: "missing phone", events: validAndroidNotification()},
		{name: "empty events", phoneID: "phone"},
		{name: "invalid event", phoneID: "phone", events: []NotificationEvent{{
			SourceID: "source", SourceEventID: "event", Event: "unknown", AppIdentifier: "app",
		}}},
		{name: "missing source", phoneID: "phone", events: []NotificationEvent{{
			SourceEventID: "event", Event: "added", AppIdentifier: "app",
		}}},
		{name: "missing event id", phoneID: "phone", events: []NotificationEvent{{
			SourceID: "source", Event: "added", AppIdentifier: "app",
		}}},
		{name: "missing app", phoneID: "phone", events: []NotificationEvent{{
			SourceID: "source", SourceEventID: "event", Event: "added",
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.PublishAndroidNotifications(test.phoneID, test.events); err == nil {
				t.Fatal("invalid notification batch was accepted")
			}
		})
	}
}

func TestAndroidNotificationDedupeIsScopedByPhone(t *testing.T) {
	service := NewService(8)
	events := validAndroidNotification()
	for _, phoneID := range []string{"phone-a", "phone-b"} {
		result, err := service.PublishAndroidNotifications(phoneID, events)
		if err != nil {
			t.Fatal(err)
		}
		if result.Accepted != 1 {
			t.Fatalf("phone %s result=%#v", phoneID, result)
		}
	}
}

func validAndroidNotification() []NotificationEvent {
	return []NotificationEvent{{
		SourceID:      "source",
		SourceEventID: "event",
		Event:         "added",
		AppIdentifier: "com.example",
	}}
}
