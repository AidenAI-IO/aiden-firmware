package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"aiden-agent/internal/ble"
)

const defaultBLEServiceSocketPath = "/run/ble_service/ble_service.sock"

type bluetoothStatusResponse struct {
	OK        bool              `json:"ok"`
	Bluetooth ble.RuntimeStatus `json:"bluetooth"`
	Removed   int               `json:"removed,omitempty"`
	Error     string            `json:"error,omitempty"`
}

func (s *Server) handleBluetoothStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeBluetoothError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	status, err := s.bluetoothStatusRequest()(ctx, s.bluetoothServiceSocketPath())
	if err != nil {
		writeBluetoothRequestError(w, err)
		return
	}
	writeBluetoothJSON(w, http.StatusOK, bluetoothStatusResponse{OK: true, Bluetooth: status})
}

func (s *Server) handleBluetoothPairingStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeBluetoothError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	status, err := s.bluetoothPairingStartRequest()(ctx, s.bluetoothServiceSocketPath())
	if err != nil {
		writeBluetoothRequestError(w, err)
		return
	}
	writeBluetoothJSON(w, http.StatusOK, bluetoothStatusResponse{OK: true, Bluetooth: status})
}

func (s *Server) handleBluetoothPairingForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeBluetoothError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	result, err := s.bluetoothPairingForgetRequest()(ctx, s.bluetoothServiceSocketPath())
	if err != nil {
		writeBluetoothRequestError(w, err)
		return
	}
	writeBluetoothJSON(w, http.StatusOK, bluetoothStatusResponse{
		OK:        true,
		Bluetooth: result.Bluetooth,
		Removed:   result.Removed,
	})
}

func (s *Server) bluetoothServiceSocketPath() string {
	if path := strings.TrimSpace(s.bleSocketPath); path != "" {
		return path
	}
	return defaultBLEServiceSocketPath
}

func (s *Server) bluetoothStatusRequest() func(context.Context, string) (ble.RuntimeStatus, error) {
	if s.bleStatusRequest != nil {
		return s.bleStatusRequest
	}
	return ble.RequestStatus
}

func (s *Server) bluetoothPairingStartRequest() func(context.Context, string) (ble.RuntimeStatus, error) {
	if s.blePairingStartRequest != nil {
		return s.blePairingStartRequest
	}
	return ble.RequestPairingStart
}

func (s *Server) bluetoothPairingForgetRequest() func(context.Context, string) (ble.ForgetResult, error) {
	if s.blePairingForgetRequest != nil {
		return s.blePairingForgetRequest
	}
	return ble.RequestPairingForget
}

func writeBluetoothRequestError(w http.ResponseWriter, err error) {
	statusCode := http.StatusServiceUnavailable
	var requestErr *ble.RequestError
	if errors.As(err, &requestErr) {
		switch requestErr.Status {
		case "FAILED_PRECONDITION":
			statusCode = http.StatusConflict
		case "INVALID_ARGUMENT":
			statusCode = http.StatusBadRequest
		case "SERVICE_UNAVAILABLE":
			statusCode = http.StatusServiceUnavailable
		default:
			statusCode = http.StatusBadGateway
		}
	}
	writeBluetoothError(w, statusCode, err.Error())
}

func writeBluetoothError(w http.ResponseWriter, statusCode int, message string) {
	writeBluetoothJSON(w, statusCode, bluetoothStatusResponse{OK: false, Error: message})
}

func writeBluetoothJSON(w http.ResponseWriter, statusCode int, payload bluetoothStatusResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
