package volcengine

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestVolcengineProtocolEncodesStartConnection(t *testing.T) {
	frame := encodeClientEvent(eventStartConnection, "", []byte("{}"))
	if len(frame) < 12 {
		t.Fatalf("frame too short: %d", len(frame))
	}
	if frame[0] != 0x11 || frame[1] != 0x14 || frame[2] != 0x10 || frame[3] != 0x00 {
		t.Fatalf("header = % x, want 11 14 10 00", frame[:4])
	}
	if got := int32(binary.BigEndian.Uint32(frame[4:8])); got != eventStartConnection {
		t.Fatalf("event = %d, want %d", got, eventStartConnection)
	}
	if got := binary.BigEndian.Uint32(frame[8:12]); got != 2 {
		t.Fatalf("payload length = %d, want 2", got)
	}
	if string(frame[12:]) != "{}" {
		t.Fatalf("payload = %q, want {}", string(frame[12:]))
	}
}

func TestVolcengineProtocolEncodesSessionTaskRequest(t *testing.T) {
	frame := encodeClientEvent(eventTaskRequest, "session-1", []byte(`{"req_params":{"text":"hi"}}`))
	if got := int32(binary.BigEndian.Uint32(frame[4:8])); got != eventTaskRequest {
		t.Fatalf("event = %d, want %d", got, eventTaskRequest)
	}
	sessionLen := int(binary.BigEndian.Uint32(frame[8:12]))
	if sessionLen != len("session-1") {
		t.Fatalf("session length = %d", sessionLen)
	}
	if string(frame[12:12+sessionLen]) != "session-1" {
		t.Fatalf("session id = %q", string(frame[12:12+sessionLen]))
	}
}

func TestVolcengineProtocolParsesAudioFrame(t *testing.T) {
	frame := encodeServerAudioFrame(eventTTSResponse, "session-1", []byte{1, 2, 3})
	msg, err := parseServerFrame(frame)
	if err != nil {
		t.Fatalf("parseServerFrame() error = %v", err)
	}
	if msg.event != eventTTSResponse || msg.sessionID != "session-1" || !bytes.Equal(msg.payload, []byte{1, 2, 3}) || msg.messageType != messageTypeAudioOnlyResponse {
		t.Fatalf("parsed message = %#v", msg)
	}
}

func TestVolcengineProtocolParsesFullServerResponseWithConnectionAndSessionIDs(t *testing.T) {
	payload := []byte(`{"status_code":20000000,"message":"ok"}`)
	frame := encodeServerJSONFrame(eventSessionStarted, "conn-1", "session-1", payload)

	msg, err := parseServerFrame(frame)
	if err != nil {
		t.Fatalf("parseServerFrame() error = %v", err)
	}
	if msg.connectionID != "conn-1" || msg.sessionID != "session-1" || !bytes.Equal(msg.payload, payload) {
		t.Fatalf("parsed message = %#v, want both IDs and payload %s", msg, payload)
	}
}
