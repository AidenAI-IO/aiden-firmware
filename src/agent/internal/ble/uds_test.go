package ble

import (
	"bytes"
	"encoding/json"
	"testing"
)

type fakeWakeBackend struct {
	delivered bool
	payloadID uint64
	reason    string
}

func (b *fakeWakeBackend) NotifyWake(sequence uint64, reason string) (bool, error) {
	b.payloadID = sequence
	b.reason = reason
	return b.delivered, nil
}

func TestUDSOperations(t *testing.T) {
	service := NewService(4)
	backend := &fakeWakeBackend{delivered: true}
	service.setBackend(backend)
	server := NewUDSServer("unused", service)

	response := decodeResponse(t, server.handleRequest([]byte(`{"op":"wake","reason":"queue"}`), nil))
	if response["status"] != "OK" || response["wake_id"] != "1" || response["delivered"] != true {
		t.Fatalf("unexpected wake response: %#v", response)
	}
	if backend.payloadID != 1 || backend.reason != "queue" {
		t.Fatalf("wake was not forwarded: %#v", backend)
	}

	service.store.Append(NotificationEvent{NotificationUID: 42, Event: "added"})
	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"events_since","since":"0","limit":10}`), nil))
	if response["status"] != "OK" {
		t.Fatalf("unexpected events response: %#v", response)
	}
	events, ok := response["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("unexpected event list: %#v", response["events"])
	}

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"events_since","since":"bad"}`), nil))
	if response["status"] != "INVALID_ARGUMENT" {
		t.Fatalf("invalid cursor was accepted: %#v", response)
	}
}

func TestUDSMessageRoundTrip(t *testing.T) {
	header := []byte(`{"op":"status"}`)
	payload := []byte{1, 2, 3}
	var buffer bytes.Buffer
	if err := writeUDSMessage(&buffer, header, payload); err != nil {
		t.Fatal(err)
	}
	gotHeader, gotPayload, err := readUDSMessage(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotHeader, header) || !bytes.Equal(gotPayload, payload) {
		t.Fatalf("round trip mismatch header=%q payload=%v", gotHeader, gotPayload)
	}
}

func decodeResponse(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response %q: %v", encoded, err)
	}
	return response
}
