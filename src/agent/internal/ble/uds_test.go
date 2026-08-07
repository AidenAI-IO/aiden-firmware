package ble

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeWakeBackend struct {
	delivered      bool
	payloadID      uint64
	reason         string
	err            error
	pairingStarted bool
	pairingErr     error
	forgetRemoved  int
	forgetErr      error
}

func (b *fakeWakeBackend) NotifyWake(sequence uint64, reason string) (bool, error) {
	b.payloadID = sequence
	b.reason = reason
	return b.delivered, b.err
}

func (b *fakeWakeBackend) StartPairing() error {
	b.pairingStarted = true
	return b.pairingErr
}

func (b *fakeWakeBackend) ForgetPairing() (int, error) {
	return b.forgetRemoved, b.forgetErr
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

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"status"}`), nil))
	bluetooth, ok := response["bluetooth"].(map[string]any)
	if !ok || bluetooth["hid_service_uuid"] != HIDServiceUUID {
		t.Fatalf("status does not expose HOGP identity: %#v", response)
	}

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_start"}`), nil))
	if response["status"] != "OK" || !backend.pairingStarted {
		t.Fatalf("pairing start was not forwarded: %#v backend=%#v", response, backend)
	}

	backend.forgetRemoved = 1
	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_forget"}`), nil))
	if response["status"] != "OK" || response["removed"] != float64(1) {
		t.Fatalf("pairing forget was not forwarded: %#v", response)
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
	event, ok := events[0].(map[string]any)
	if !ok || event["id"] != "1" || event["notification_uid"] != float64(42) || event["event"] != "added" {
		t.Fatalf("events_since lost cursor or event content: %#v", events[0])
	}

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"events_since","since":"bad"}`), nil))
	if response["status"] != "INVALID_ARGUMENT" {
		t.Fatalf("invalid cursor was accepted: %#v", response)
	}
}

func TestUDSPairingFailures(t *testing.T) {
	service := NewService(4)
	server := NewUDSServer("unused", service)
	response := decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_start"}`), nil))
	if response["status"] != "SERVICE_UNAVAILABLE" {
		t.Fatalf("missing backend status=%#v", response)
	}

	backend := &fakeWakeBackend{pairingErr: ErrAlreadyPaired}
	service.setBackend(backend)
	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_start"}`), nil))
	if response["status"] != "FAILED_PRECONDITION" {
		t.Fatalf("already-paired status=%#v", response)
	}
}

func TestUDSWakeFailures(t *testing.T) {
	tests := []struct {
		name       string
		backend    wakeBackend
		wantStatus string
	}{
		{name: "backend unavailable", backend: nil, wantStatus: "SERVICE_UNAVAILABLE"},
		{name: "backend failure", backend: &fakeWakeBackend{err: errors.New("notify failed")}, wantStatus: "INTERNAL_ERROR"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(4)
			if test.backend != nil {
				service.setBackend(test.backend)
			}
			server := NewUDSServer("unused", service)
			response := decodeResponse(t, server.handleRequest([]byte(`{"op":"wake","reason":"queue"}`), nil))
			if response["status"] != test.wantStatus || response["error"] == "" {
				t.Fatalf("unexpected wake failure response: %#v", response)
			}
		})
	}
}

func TestUDSEventsSinceRejectsOversizedResponse(t *testing.T) {
	service := NewService(20)
	for index := 0; index < 20; index++ {
		service.store.Append(NotificationEvent{
			NotificationUID: uint32(index + 1),
			Event:           "added",
			Message:         strings.Repeat("x", 8*1024),
		})
	}
	server := NewUDSServer("unused", service)
	response := decodeResponse(t, server.handleRequest([]byte(`{"op":"events_since","since":"0","limit":20}`), nil))
	if response["status"] != "RESOURCE_EXHAUSTED" {
		t.Fatalf("oversized events response was emitted: %#v", response)
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

func TestWriteUDSMessageEnforcesFrameLimits(t *testing.T) {
	if err := writeUDSMessage(&bytes.Buffer{}, make([]byte, maxUDSHeaderBytes+1), nil); err == nil {
		t.Fatal("oversized header was accepted")
	}
	if err := writeUDSMessage(&bytes.Buffer{}, []byte(`{}`), make([]byte, maxUDSPayloadBytes+1)); err == nil {
		t.Fatal("oversized payload was accepted")
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
