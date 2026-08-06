package ble

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestANCSConsumerFetchesAndNormalizesAttributes(t *testing.T) {
	store := NewEventStore(8)
	consumer := NewANCSConsumer(store)
	var command []byte
	consumer.SetControlPointWriter(func(value []byte) error {
		command = append([]byte(nil), value...)
		return nil
	})

	uid := uint32(0x01020304)
	source := []byte{ancsEventAdded, 0x02, 6, 1, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(source[4:], uid)
	if err := consumer.HandleNotificationSource(source); err != nil {
		t.Fatalf("HandleNotificationSource: %v", err)
	}
	if len(command) == 0 || command[0] != ancsCommandGetNotificationAttributes ||
		binary.LittleEndian.Uint32(command[1:5]) != uid {
		t.Fatalf("unexpected Control Point command: %v", command)
	}
	if store.Stats().Count != 0 {
		t.Fatal("event must wait for requested metadata")
	}

	response := []byte{ancsCommandGetNotificationAttributes, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(response[1:5], uid)
	response = appendAttribute(response, ancsAttributeAppIdentifier, "com.example.mail")
	response = appendAttribute(response, ancsAttributeTitle, "New message")
	response = appendAttribute(response, ancsAttributeSubtitle, "Alice")
	response = appendAttribute(response, ancsAttributeMessage, "Hello from ANCS")
	response = appendAttribute(response, ancsAttributeDate, "20260806T103000")
	cut := len(response) / 2
	if err := consumer.HandleDataSource(response[:cut]); err != nil {
		t.Fatalf("first Data Source chunk: %v", err)
	}
	if store.Stats().Count != 0 {
		t.Fatal("partial Data Source response must not emit an event")
	}
	if err := consumer.HandleDataSource(response[cut:]); err != nil {
		t.Fatalf("second Data Source chunk: %v", err)
	}

	page := store.Page(0, 10)
	if len(page.Events) != 1 {
		t.Fatalf("expected one normalized event, got %#v", page.Events)
	}
	event := page.Events[0]
	if event.Event != "added" || event.Category != "email" ||
		event.AppIdentifier != "com.example.mail" || event.Title != "New message" ||
		event.Message != "Hello from ANCS" || !event.MetadataComplete {
		t.Fatalf("unexpected normalized event: %#v", event)
	}
	if !bytes.Equal([]byte(event.Flags[0]), []byte("important")) {
		t.Fatalf("unexpected flags: %#v", event.Flags)
	}
}

func TestANCSRemovedEventDoesNotRequestAttributes(t *testing.T) {
	store := NewEventStore(8)
	consumer := NewANCSConsumer(store)
	called := false
	consumer.SetControlPointWriter(func([]byte) error {
		called = true
		return nil
	})
	source := []byte{ancsEventRemoved, 0, 4, 0, 7, 0, 0, 0}
	if err := consumer.HandleNotificationSource(source); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("removed notifications must not request attributes")
	}
	page := store.Page(0, 10)
	if len(page.Events) != 1 || page.Events[0].Event != "removed" || !page.Events[0].MetadataComplete {
		t.Fatalf("unexpected removal event: %#v", page.Events)
	}
}

func TestParseNotificationSourceRejectsPartialPacket(t *testing.T) {
	if _, err := ParseNotificationSource(make([]byte, 7)); err == nil {
		t.Fatal("expected invalid packet length error")
	}
}

func appendAttribute(buffer []byte, attributeID uint8, value string) []byte {
	buffer = append(buffer, attributeID, 0, 0)
	binary.LittleEndian.PutUint16(buffer[len(buffer)-2:], uint16(len(value)))
	return append(buffer, value...)
}
