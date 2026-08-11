package ble

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakePairingBackend struct {
	pairingStarted bool
	pairingErr     error
	disconnected   bool
	disconnectErr  error
	forgetRemoved  int
	forgetErr      error
}

func (b *fakePairingBackend) StartPairing() error {
	b.pairingStarted = true
	return b.pairingErr
}

func (b *fakePairingBackend) Disconnect() error {
	b.disconnected = true
	return b.disconnectErr
}

func (b *fakePairingBackend) ForgetPairing() (int, error) {
	return b.forgetRemoved, b.forgetErr
}

func TestUDSOperations(t *testing.T) {
	service := NewService(4)
	backend := &fakePairingBackend{}
	service.setBackend(backend)
	server := NewUDSServer("unused", service)

	response := decodeResponse(t, server.handleRequest([]byte(`{"op":"status"}`), nil))
	bluetooth, ok := response["bluetooth"].(map[string]any)
	if !ok || bluetooth["pairing_service_uuid"] != PairingServiceUUID {
		t.Fatalf("status does not expose pairing service identity: %#v", response)
	}

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_start"}`), nil))
	if response["status"] != "OK" || !backend.pairingStarted {
		t.Fatalf("pairing start was not forwarded: %#v backend=%#v", response, backend)
	}

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"disconnect"}`), nil))
	if response["status"] != "OK" || !backend.disconnected {
		t.Fatalf("disconnect was not forwarded: %#v backend=%#v", response, backend)
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
	generation, ok := response["generation"].(string)
	if !ok || generation == "" || response["reset_required"] != false {
		t.Fatalf("events_since did not expose its generation: %#v", response)
	}

	restarted := NewService(4)
	restarted.store.Append(NotificationEvent{NotificationUID: 43, Event: "added"})
	restartedServer := NewUDSServer("unused", restarted)
	response = decodeResponse(t, restartedServer.handleRequest(
		[]byte(`{"op":"events_since","since":"1","generation":"`+generation+`","limit":10}`),
		nil,
	))
	if response["status"] != "OK" || response["reset_required"] != true {
		t.Fatalf("events_since did not detect a service restart: %#v", response)
	}
	if events, ok := response["events"].([]any); !ok || len(events) != 0 {
		t.Fatalf("restart response must require a zero-cursor retry: %#v", response)
	}

	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"events_since","since":"bad"}`), nil))
	if response["status"] != "INVALID_ARGUMENT" {
		t.Fatalf("invalid cursor was accepted: %#v", response)
	}

	publishRequest := []byte(`{"op":"notification_publish","phone_id":"android-1","events":[{"source_id":"notification-key","source_event_id":"event-1","event":"added","app_identifier":"com.example","title":"Hello"}]}`)
	response = decodeResponse(t, server.handleRequest(publishRequest, nil))
	if response["status"] != "OK" || response["accepted"] != float64(1) || response["last_id"] != "2" {
		t.Fatalf("notification publish failed: %#v", response)
	}
	response = decodeResponse(t, server.handleRequest(publishRequest, nil))
	if response["status"] != "OK" || response["duplicates"] != float64(1) || response["last_id"] != "2" {
		t.Fatalf("notification retry was not deduplicated: %#v", response)
	}
}

func TestUDSPairingFailures(t *testing.T) {
	service := NewService(4)
	server := NewUDSServer("unused", service)
	response := decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_start"}`), nil))
	if response["status"] != "SERVICE_UNAVAILABLE" {
		t.Fatalf("missing backend status=%#v", response)
	}

	backend := &fakePairingBackend{pairingErr: errors.New("pairing failed")}
	service.setBackend(backend)
	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"pairing_start"}`), nil))
	if response["status"] != "INTERNAL_ERROR" {
		t.Fatalf("pairing failure status=%#v", response)
	}

	backend.disconnectErr = errors.New("disconnect failed")
	response = decodeResponse(t, server.handleRequest([]byte(`{"op":"disconnect"}`), nil))
	if response["status"] != "INTERNAL_ERROR" {
		t.Fatalf("disconnect failure status=%#v", response)
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
