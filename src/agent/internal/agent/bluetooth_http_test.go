package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aiden-agent/internal/ble"
)

func TestBluetoothHTTPStatus(t *testing.T) {
	server := &Server{
		bleStatusRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			return ble.RuntimeStatus{DeviceName: "Aiden-1234", BackendAvailable: true}, nil
		},
	}
	recorder := httptest.NewRecorder()
	server.handleBluetoothStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/bluetooth/status", nil))
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"ok":true`, `"device_name":"Aiden-1234"`) {
		t.Fatalf("status code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBluetoothHTTPPairingActions(t *testing.T) {
	server := &Server{
		blePairingStartRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			return ble.RuntimeStatus{PairingOpen: true}, nil
		},
		blePairingForgetRequest: func(context.Context, string) (ble.ForgetResult, error) {
			return ble.ForgetResult{Removed: 1, Bluetooth: ble.RuntimeStatus{Paired: false}}, nil
		},
	}

	recorder := httptest.NewRecorder()
	server.handleBluetoothPairingStart(recorder, httptest.NewRequest(http.MethodPost, "/api/bluetooth/pairing/start", nil))
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"pairing_open":true`) {
		t.Fatalf("start code=%d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.handleBluetoothPairingForget(recorder, httptest.NewRequest(http.MethodPost, "/api/bluetooth/pairing/forget", nil))
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"removed":1`, `"paired":false`) {
		t.Fatalf("forget code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBluetoothHTTPMapsPairingConflict(t *testing.T) {
	server := &Server{
		blePairingStartRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			return ble.RuntimeStatus{}, &ble.RequestError{Status: "FAILED_PRECONDITION", Message: "already paired"}
		},
	}
	recorder := httptest.NewRecorder()
	server.handleBluetoothPairingStart(recorder, httptest.NewRequest(http.MethodPost, "/api/bluetooth/pairing/start", nil))
	if recorder.Code != http.StatusConflict || !containsAll(recorder.Body.String(), "already paired") {
		t.Fatalf("conflict code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBluetoothHTTPMethodAndUnavailableErrors(t *testing.T) {
	server := &Server{
		bleStatusRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			return ble.RuntimeStatus{}, errors.New("socket unavailable")
		},
	}
	recorder := httptest.NewRecorder()
	server.handleBluetoothStatus(recorder, httptest.NewRequest(http.MethodPost, "/api/bluetooth/status", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	server.handleBluetoothStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/bluetooth/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func containsAll(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
}
