package ble

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type ForgetResult struct {
	Removed   int           `json:"removed"`
	Bluetooth RuntimeStatus `json:"bluetooth"`
}

type RequestError struct {
	Status  string
	Message string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("ble_service request failed (%s): %s", e.Status, e.Message)
}

func RequestStatus(ctx context.Context, socketPath string) (RuntimeStatus, error) {
	var response struct {
		Status    string        `json:"status"`
		Error     string        `json:"error"`
		Bluetooth RuntimeStatus `json:"bluetooth"`
	}
	if err := request(ctx, socketPath, map[string]string{"op": "status"}, &response); err != nil {
		return RuntimeStatus{}, err
	}
	return response.Bluetooth, nil
}

func RequestPairingStart(ctx context.Context, socketPath string) (RuntimeStatus, error) {
	var response struct {
		Status    string        `json:"status"`
		Error     string        `json:"error"`
		Bluetooth RuntimeStatus `json:"bluetooth"`
	}
	if err := request(ctx, socketPath, map[string]string{"op": "pairing_start"}, &response); err != nil {
		return RuntimeStatus{}, err
	}
	return response.Bluetooth, nil
}

func RequestDisconnect(ctx context.Context, socketPath string) (RuntimeStatus, error) {
	var response struct {
		Status    string        `json:"status"`
		Error     string        `json:"error"`
		Bluetooth RuntimeStatus `json:"bluetooth"`
	}
	if err := request(ctx, socketPath, map[string]string{"op": "disconnect"}, &response); err != nil {
		return RuntimeStatus{}, err
	}
	return response.Bluetooth, nil
}

func RequestPairingForget(ctx context.Context, socketPath string) (ForgetResult, error) {
	var response struct {
		Status    string        `json:"status"`
		Error     string        `json:"error"`
		Removed   int           `json:"removed"`
		Bluetooth RuntimeStatus `json:"bluetooth"`
	}
	if err := request(ctx, socketPath, map[string]string{"op": "pairing_forget"}, &response); err != nil {
		return ForgetResult{}, err
	}
	return ForgetResult{Removed: response.Removed, Bluetooth: response.Bluetooth}, nil
}

func RequestPublishNotifications(
	ctx context.Context,
	socketPath string,
	phoneID string,
	events []NotificationEvent,
) (NotificationPublishResult, error) {
	var response struct {
		Status     string `json:"status"`
		Error      string `json:"error"`
		Accepted   int    `json:"accepted"`
		Duplicates int    `json:"duplicates"`
		LastID     string `json:"last_id"`
	}
	requestBytes, err := marshalNotificationPublishRequest(phoneID, events)
	if err != nil {
		return NotificationPublishResult{}, err
	}
	if err := requestEncoded(ctx, socketPath, requestBytes, &response); err != nil {
		return NotificationPublishResult{}, err
	}
	return NotificationPublishResult{
		Accepted:   response.Accepted,
		Duplicates: response.Duplicates,
		LastID:     response.LastID,
	}, nil
}

// ValidateNotificationPublishRequestFrame verifies that the request's exact
// UDS representation fits before an HTTP caller attempts to publish it.
func ValidateNotificationPublishRequestFrame(phoneID string, events []NotificationEvent) error {
	_, err := marshalNotificationPublishRequest(phoneID, events)
	return err
}

func marshalNotificationPublishRequest(phoneID string, events []NotificationEvent) ([]byte, error) {
	requestValue := struct {
		Op      string              `json:"op"`
		PhoneID string              `json:"phone_id"`
		Events  []NotificationEvent `json:"events"`
	}{
		Op:      "notification_publish",
		PhoneID: phoneID,
		Events:  events,
	}
	requestBytes, err := json.Marshal(requestValue)
	if err != nil {
		return nil, err
	}
	if len(requestBytes) > maxUDSHeaderBytes {
		return nil, fmt.Errorf(
			"notification publish request exceeds BLE UDS frame limit: %d > %d bytes",
			len(requestBytes),
			maxUDSHeaderBytes,
		)
	}
	return requestBytes, nil
}

func request(ctx context.Context, socketPath string, requestValue any, responseValue any) error {
	requestBytes, err := json.Marshal(requestValue)
	if err != nil {
		return err
	}
	return requestEncoded(ctx, socketPath, requestBytes, responseValue)
}

func requestEncoded(ctx context.Context, socketPath string, requestBytes []byte, responseValue any) error {
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect ble_service: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(requestDeadline(ctx, time.Now()))

	if err := writeUDSMessage(connection, requestBytes, nil); err != nil {
		return fmt.Errorf("write ble_service request: %w", err)
	}
	header, _, err := readUDSMessage(connection)
	if err != nil {
		return fmt.Errorf("read ble_service response: %w", err)
	}
	var envelope struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(header, &envelope); err != nil {
		return fmt.Errorf("decode ble_service response: %w", err)
	}
	if envelope.Status != "OK" {
		return &RequestError{Status: envelope.Status, Message: envelope.Error}
	}
	if err := json.Unmarshal(header, responseValue); err != nil {
		return fmt.Errorf("decode ble_service response body: %w", err)
	}
	return nil
}

func requestDeadline(ctx context.Context, now time.Time) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return now.Add(5 * time.Second)
}
