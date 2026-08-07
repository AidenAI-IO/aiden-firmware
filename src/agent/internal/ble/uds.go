package ble

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	maxUDSHeaderBytes  = 64 * 1024
	maxUDSPayloadBytes = 1024 * 1024
)

type UDSServer struct {
	path      string
	service   *Service
	listener  *net.UnixListener
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func NewUDSServer(path string, service *Service) *UDSServer {
	return &UDSServer{path: path, service: service}
}

func (s *UDSServer) Start() error {
	if s.path == "" {
		return errors.New("BLE service socket path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create BLE socket directory: %w", err)
	}
	if info, err := os.Lstat(s.path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path %s", s.path)
		}
		if err := os.Remove(s.path); err != nil {
			return fmt.Errorf("remove stale BLE socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect BLE socket: %w", err)
	}

	address := &net.UnixAddr{Name: s.path, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		return fmt.Errorf("listen on BLE socket: %w", err)
	}
	s.listener = listener
	if err := os.Chmod(s.path, 0o660); err != nil {
		_ = listener.Close()
		return fmt.Errorf("set BLE socket permissions: %w", err)
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

func (s *UDSServer) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.listener != nil {
			closeErr = s.listener.Close()
		}
		s.wg.Wait()
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (s *UDSServer) acceptLoop() {
	defer s.wg.Done()
	for {
		connection, err := s.listener.AcceptUnix()
		if err != nil {
			if temporary, ok := err.(net.Error); ok && temporary.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer connection.Close()
			s.serveConnection(connection)
		}()
	}
}

func (s *UDSServer) serveConnection(connection *net.UnixConn) {
	for {
		_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
		header, payload, err := readUDSMessage(connection)
		if err != nil {
			return
		}
		response := s.handleRequest(header, payload)
		if err := writeUDSMessage(connection, response, nil); err != nil {
			return
		}
	}
}

type udsRequest struct {
	Op         string          `json:"op"`
	Since      json.RawMessage `json:"since"`
	Generation string          `json:"generation"`
	Limit      int             `json:"limit"`
	Reason     string          `json:"reason"`
}

func (s *UDSServer) handleRequest(header, payload []byte) []byte {
	if len(payload) != 0 {
		return marshalResponse(map[string]any{
			"status": "INVALID_ARGUMENT",
			"error":  "BLE service operations do not accept a binary payload",
		})
	}
	var request udsRequest
	if err := json.Unmarshal(header, &request); err != nil {
		return marshalResponse(map[string]any{"status": "INVALID_ARGUMENT", "error": err.Error()})
	}
	switch request.Op {
	case "status":
		return marshalResponse(map[string]any{
			"status":    "OK",
			"bluetooth": s.service.Status(),
		})
	case "wake":
		sequence, delivered, err := s.service.Wake(request.Reason)
		if err != nil {
			status := "INTERNAL_ERROR"
			if errors.Is(err, ErrBluetoothUnavailable) {
				status = "SERVICE_UNAVAILABLE"
			}
			return marshalResponse(map[string]any{"status": status, "error": err.Error()})
		}
		return marshalResponse(map[string]any{
			"status":    "OK",
			"wake_id":   strconv.FormatUint(sequence, 10),
			"delivered": delivered,
		})
	case "pairing_start":
		if err := s.service.StartPairing(); err != nil {
			return marshalBLEError(err)
		}
		return marshalResponse(map[string]any{
			"status":    "OK",
			"bluetooth": s.service.Status(),
		})
	case "pairing_forget":
		removed, err := s.service.ForgetPairing()
		if err != nil {
			return marshalBLEError(err)
		}
		return marshalResponse(map[string]any{
			"status":    "OK",
			"removed":   removed,
			"bluetooth": s.service.Status(),
		})
	case "events_since":
		since, err := parseCursor(request.Since)
		if err != nil {
			return marshalResponse(map[string]any{"status": "INVALID_ARGUMENT", "error": err.Error()})
		}
		page := s.service.EventsSince(since, request.Limit, request.Generation)
		return marshalResponse(map[string]any{
			"status":         "OK",
			"events":         page.Events,
			"generation":     page.Generation,
			"reset_required": page.ResetRequired,
			"truncated":      page.Truncated,
			"oldest_id":      page.OldestID,
			"last_id":        page.LastID,
		})
	default:
		return marshalResponse(map[string]any{
			"status": "INVALID_ARGUMENT",
			"error":  fmt.Sprintf("unsupported operation %q", request.Op),
		})
	}
}

func marshalBLEError(err error) []byte {
	status := "INTERNAL_ERROR"
	switch {
	case errors.Is(err, ErrBluetoothUnavailable):
		status = "SERVICE_UNAVAILABLE"
	case errors.Is(err, ErrAlreadyPaired):
		status = "FAILED_PRECONDITION"
	}
	return marshalResponse(map[string]any{"status": status, "error": err.Error()})
}

func parseCursor(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, fmt.Errorf("invalid since cursor: %w", err)
		}
	} else {
		text = string(raw)
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid since cursor %q", text)
	}
	return value, nil
}

func marshalResponse(response map[string]any) []byte {
	encoded, err := json.Marshal(response)
	if err != nil {
		return []byte(`{"status":"INTERNAL_ERROR","error":"response encoding failed"}`)
	}
	if len(encoded) > maxUDSHeaderBytes {
		return []byte(`{"status":"RESOURCE_EXHAUSTED","error":"response exceeds frame limit; retry with a smaller limit"}`)
	}
	return encoded
}

func readUDSMessage(reader io.Reader) ([]byte, []byte, error) {
	prefix := make([]byte, 12)
	if _, err := io.ReadFull(reader, prefix); err != nil {
		return nil, nil, err
	}
	headerLength := binary.LittleEndian.Uint32(prefix[:4])
	payloadLength := binary.LittleEndian.Uint64(prefix[4:])
	if headerLength == 0 || headerLength > maxUDSHeaderBytes {
		return nil, nil, fmt.Errorf("invalid BLE UDS header length %d", headerLength)
	}
	if payloadLength > maxUDSPayloadBytes {
		return nil, nil, fmt.Errorf("invalid BLE UDS payload length %d", payloadLength)
	}
	header := make([]byte, int(headerLength))
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, nil, err
	}
	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, nil, err
	}
	return header, payload, nil
}

func writeUDSMessage(writer io.Writer, header, payload []byte) error {
	if len(header) == 0 || len(header) > maxUDSHeaderBytes {
		return fmt.Errorf("invalid BLE UDS header length %d", len(header))
	}
	if len(payload) > maxUDSPayloadBytes {
		return fmt.Errorf("invalid BLE UDS payload length %d", len(payload))
	}
	prefix := make([]byte, 12)
	binary.LittleEndian.PutUint32(prefix[:4], uint32(len(header)))
	binary.LittleEndian.PutUint64(prefix[4:], uint64(len(payload)))
	for _, chunk := range [][]byte{prefix, header, payload} {
		for len(chunk) > 0 {
			written, err := writer.Write(chunk)
			if err != nil {
				return err
			}
			if written <= 0 {
				return io.ErrShortWrite
			}
			chunk = chunk[written:]
		}
	}
	return nil
}
