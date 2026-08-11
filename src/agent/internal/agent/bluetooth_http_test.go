package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestBluetoothHTTPDisconnectsPhysicalLink(t *testing.T) {
	server := &Server{
		bleDisconnectRequest: func(context.Context, string) (ble.RuntimeStatus, error) {
			return ble.RuntimeStatus{Paired: true, Connected: false, ANCSSubscribed: false}, nil
		},
	}

	recorder := httptest.NewRecorder()
	server.handleBluetoothDisconnect(recorder, bluetoothControlRequestForPath(
		http.MethodPost,
		"/api/bluetooth/disconnect",
	))
	if recorder.Code != http.StatusOK || !containsAll(
		recorder.Body.String(),
		`"paired":true`,
		`"connected":false`,
		`"ancs_subscribed":false`,
	) {
		t.Fatalf("disconnect code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBluetoothHTTPResetsStaleBond(t *testing.T) {
	server := &Server{
		blePairingForgetRequest: func(context.Context, string) (ble.ForgetResult, error) {
			return ble.ForgetResult{
				Removed:   1,
				Bluetooth: ble.RuntimeStatus{Paired: false, Connected: false},
			}, nil
		},
	}

	recorder := httptest.NewRecorder()
	server.handleBluetoothPairingReset(recorder, bluetoothControlRequestForPath(
		http.MethodPost,
		"/api/bluetooth/pairing/reset",
	))
	if recorder.Code != http.StatusOK || !containsAll(
		recorder.Body.String(),
		`"removed":1`,
		`"paired":false`,
		`"connected":false`,
	) {
		t.Fatalf("pairing reset code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPhoneNotificationEventsPublishesAndroidBatch(t *testing.T) {
	var gotPhoneID string
	var gotEvents []ble.NotificationEvent
	server := &Server{
		bleNotifyRequest: func(
			_ context.Context,
			_ string,
			phoneID string,
			events []ble.NotificationEvent,
		) (ble.NotificationPublishResult, error) {
			gotPhoneID = phoneID
			gotEvents = events
			return ble.NotificationPublishResult{Accepted: 1, LastID: "17"}, nil
		},
	}
	body := []byte(`{"phone_id":"android-1","events":[{"source_id":"key","source_event_id":"event-1","event":"added","app_identifier":"com.example"}]}`)
	request := bluetoothControlRequestForPath(http.MethodPost, "/api/phone-notifications/events")
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	recorder := httptest.NewRecorder()
	server.handlePhoneNotificationEvents(recorder, request)
	if recorder.Code != http.StatusOK || !containsAll(
		recorder.Body.String(),
		`"ok":true`,
		`"accepted":1`,
		`"last_id":"17"`,
	) {
		t.Fatalf("publish code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if gotPhoneID != "android-1" || len(gotEvents) != 1 || gotEvents[0].SourceEventID != "event-1" {
		t.Fatalf("unexpected publish request phone=%q events=%#v", gotPhoneID, gotEvents)
	}
}

func TestPhoneNotificationEventsRejectsInvalidAndNonUSBRequests(t *testing.T) {
	called := false
	server := &Server{
		bleNotifyRequest: func(
			context.Context,
			string,
			string,
			[]ble.NotificationEvent,
		) (ble.NotificationPublishResult, error) {
			called = true
			return ble.NotificationPublishResult{}, nil
		},
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/phone-notifications/events",
		strings.NewReader(`{"phone_id":"android-1","events":[]}`),
	)
	request.RemoteAddr = "192.168.50.140:12345"
	recorder := httptest.NewRecorder()
	server.handlePhoneNotificationEvents(recorder, request)
	if recorder.Code != http.StatusForbidden || called {
		t.Fatalf("external publish code=%d called=%v", recorder.Code, called)
	}

	request = bluetoothControlRequestForPath(http.MethodPost, "/api/phone-notifications/events")
	request.Body = io.NopCloser(strings.NewReader(`{"phone_id":`))
	recorder = httptest.NewRecorder()
	server.handlePhoneNotificationEvents(recorder, request)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("invalid publish code=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
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
	return bluetoothControlRequestForPath(method, "/api/bluetooth/pairing/start")
}

func bluetoothControlRequestForPath(method, path string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = "192.168.42.100:12345"
	return request.WithContext(context.WithValue(
		request.Context(),
		http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.168.42.1"), Port: 8080},
	))
}
