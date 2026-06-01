package volcengine

import (
	"encoding/binary"
	"fmt"
)

const (
	messageTypeFullClientRequest  = 0x1
	messageTypeFullServerResponse = 0x9
	messageTypeAudioOnlyResponse  = 0xb
	messageTypeError              = 0xf

	messageFlagEvent = 0x4

	serializationRaw  = 0x0
	serializationJSON = 0x1

	compressionNone = 0x0

	eventStartConnection    int32 = 1
	eventFinishConnection   int32 = 2
	eventConnectionStarted  int32 = 50
	eventConnectionFailed   int32 = 51
	eventConnectionFinished int32 = 52
	eventStartSession       int32 = 100
	eventCancelSession      int32 = 101
	eventFinishSession      int32 = 102
	eventSessionStarted     int32 = 150
	eventSessionFinished    int32 = 152
	eventSessionFailed      int32 = 153
	eventTaskRequest        int32 = 200
	eventTTSResponse        int32 = 352
)

type serverMessage struct {
	messageType  int
	event        int32
	connectionID string
	sessionID    string
	errorCode    int32
	payload      []byte
}

func encodeClientEvent(event int32, sessionID string, payload []byte) []byte {
	frame := []byte{0x11, byte(messageTypeFullClientRequest<<4 | messageFlagEvent), byte(serializationJSON<<4 | compressionNone), 0x00}
	frame = binary.BigEndian.AppendUint32(frame, uint32(event))
	if sessionID != "" {
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(sessionID)))
		frame = append(frame, sessionID...)
	}
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
	frame = append(frame, payload...)
	return frame
}

func encodeServerJSONFrame(event int32, connectionID, sessionID string, payload []byte) []byte {
	frame := []byte{0x11, byte(messageTypeFullServerResponse<<4 | messageFlagEvent), byte(serializationJSON<<4 | compressionNone), 0x00}
	frame = binary.BigEndian.AppendUint32(frame, uint32(event))
	if connectionID != "" {
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(connectionID)))
		frame = append(frame, connectionID...)
	}
	if sessionID != "" {
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(sessionID)))
		frame = append(frame, sessionID...)
	}
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
	frame = append(frame, payload...)
	return frame
}

func encodeServerAudioFrame(event int32, sessionID string, payload []byte) []byte {
	frame := []byte{0x11, byte(messageTypeAudioOnlyResponse<<4 | messageFlagEvent), byte(serializationRaw<<4 | compressionNone), 0x00}
	frame = binary.BigEndian.AppendUint32(frame, uint32(event))
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(sessionID)))
	frame = append(frame, sessionID...)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
	frame = append(frame, payload...)
	return frame
}

func parseServerFrame(frame []byte) (serverMessage, error) {
	if len(frame) < 4 {
		return serverMessage{}, fmt.Errorf("frame too short")
	}
	headerUnits := int(frame[0] & 0x0f)
	if headerUnits < 1 {
		return serverMessage{}, fmt.Errorf("invalid header size")
	}
	off := headerUnits * 4
	if len(frame) < off {
		return serverMessage{}, fmt.Errorf("truncated header")
	}
	msg := serverMessage{messageType: int(frame[1] >> 4)}
	if msg.messageType == messageTypeError {
		if len(frame) < off+8 {
			return serverMessage{}, fmt.Errorf("truncated error frame")
		}
		msg.errorCode = int32(binary.BigEndian.Uint32(frame[off : off+4]))
		off += 4
		payload, next, err := readBytes(frame, off)
		if err != nil {
			return serverMessage{}, err
		}
		_ = next
		msg.payload = payload
		return msg, nil
	}
	if len(frame) < off+4 {
		return serverMessage{}, fmt.Errorf("missing event")
	}
	msg.event = int32(binary.BigEndian.Uint32(frame[off : off+4]))
	off += 4

	switch msg.messageType {
	case messageTypeFullClientRequest:
		if msg.event != eventStartConnection && msg.event != eventFinishConnection {
			value, next, err := readString(frame, off)
			if err != nil {
				return serverMessage{}, err
			}
			msg.sessionID = value
			off = next
		}
	case messageTypeFullServerResponse:
		fields, err := readFields(frame, off)
		if err != nil {
			return serverMessage{}, err
		}
		if len(fields) == 0 {
			return serverMessage{}, fmt.Errorf("missing payload")
		}
		ids := fields[:len(fields)-1]
		msg.payload = fields[len(fields)-1]
		if msg.event == eventConnectionStarted || msg.event == eventConnectionFailed || msg.event == eventConnectionFinished {
			if len(ids) > 0 {
				msg.connectionID = string(ids[0])
			}
			if len(ids) > 1 {
				msg.sessionID = string(ids[1])
			}
		} else {
			if len(ids) == 1 {
				msg.sessionID = string(ids[0])
			} else if len(ids) > 1 {
				msg.connectionID = string(ids[0])
				msg.sessionID = string(ids[1])
			}
		}
		return msg, nil
	case messageTypeAudioOnlyResponse:
		value, next, err := readString(frame, off)
		if err != nil {
			return serverMessage{}, err
		}
		msg.sessionID = value
		off = next
	default:
		return serverMessage{}, fmt.Errorf("unsupported message type %x", msg.messageType)
	}
	payload, _, err := readBytes(frame, off)
	if err != nil {
		return serverMessage{}, err
	}
	msg.payload = payload
	return msg, nil
}

func readString(frame []byte, off int) (string, int, error) {
	data, next, err := readBytes(frame, off)
	return string(data), next, err
}

func readFields(frame []byte, off int) ([][]byte, error) {
	fields := [][]byte{}
	for off < len(frame) {
		data, next, err := readBytes(frame, off)
		if err != nil {
			return nil, err
		}
		fields = append(fields, data)
		off = next
	}
	return fields, nil
}

func readBytes(frame []byte, off int) ([]byte, int, error) {
	if len(frame) < off+4 {
		return nil, off, fmt.Errorf("missing length")
	}
	n := int(binary.BigEndian.Uint32(frame[off : off+4]))
	off += 4
	if n < 0 || len(frame) < off+n {
		return nil, off, fmt.Errorf("truncated payload")
	}
	return frame[off : off+n], off + n, nil
}
