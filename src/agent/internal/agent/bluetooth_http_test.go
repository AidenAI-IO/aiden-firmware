package agent

import (
	"context"
	"errors"
	"net"
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
	}

	recorder := httptest.NewRecorder()
	server.handleBluetoothPairingStart(recorder, bluetoothControlRequest(http.MethodPost))
	if recorder.Code != http.StatusOK || !containsAll(recorder.Body.String(), `"pairing_open":true`) {
		t.Fatalf("start code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBluetoothHTTPMapsPairingConflict(t *testing.T) {
	server := &Server{
		blePairingStartRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			return ble.RuntimeStatus{}, &ble.RequestError{Status: "FAILED_PRECONDITION", Message: "already paired"}
		},
	}
	recorder := httptest.NewRecorder()
	server.handleBluetoothPairingStart(recorder, bluetoothControlRequest(http.MethodPost))
	if recorder.Code != http.StatusConflict || !containsAll(recorder.Body.String(), "already paired") {
		t.Fatalf("conflict code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBluetoothHTTPPairingStartRejectsNonUSBRequest(t *testing.T) {
	called := false
	server := &Server{
		blePairingStartRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			called = true
			return ble.RuntimeStatus{}, nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/bluetooth/pairing/start", nil)
	request.RemoteAddr = "192.168.50.140:12345"
	request = request.WithContext(context.WithValue(
		request.Context(),
		http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.50.10"), Port: 8080},
	))
	recorder := httptest.NewRecorder()
	server.handleBluetoothPairingStart(recorder, request)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("external pairing request code=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
}

func TestConfiguredBLEServiceSocketPath(t *testing.T) {
	t.Setenv("AIDEN_BLE_SERVICE_SOCKET", "/tmp/custom-ble.sock")
	if got := configuredBLEServiceSocketPath(); got != "/tmp/custom-ble.sock" {
		t.Fatalf("configuredBLEServiceSocketPath() = %q", got)
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

func bluetoothControlRequest(method string) *http.Request {
	request := httptest.NewRequest(method, "/api/bluetooth/pairing/start", nil)
	request.RemoteAddr = "192.168.42.100:12345"
	return request.WithContext(context.WithValue(
		request.Context(),
		http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.42.1"), Port: 8080},
	))
}
