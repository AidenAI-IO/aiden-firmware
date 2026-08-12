package ble

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type WakeResult struct {
	WakeID    string `json:"wake_id"`
	Delivered bool   `json:"delivered"`
}

type ForgetResult struct {
	Removed   int           `json:"removed"`
	Bluetooth RuntimeStatus `json:"bluetooth"`
}

// RequestEvents returns notification changes retained by ble_service. Cursors
// stay as strings so callers do not lose uint64 precision when persisting them.
func RequestEvents(
	ctx context.Context,
	socketPath string,
	since string,
	generation string,
	limit int,
) (EventPage, error) {
	var response struct {
		Status        string              `json:"status"`
		Error         string              `json:"error"`
		Events        []NotificationEvent `json:"events"`
		Generation    string              `json:"generation"`
		ResetRequired bool                `json:"reset_required"`
		Truncated     bool                `json:"truncated"`
		OldestID      string              `json:"oldest_id"`
		LastID        string              `json:"last_id"`
	}
	requestValue := struct {
		Op         string `json:"op"`
		Since      string `json:"since"`
		Generation string `json:"generation,omitempty"`
		Limit      int    `json:"limit,omitempty"`
	}{
		Op:         "events_since",
		Since:      since,
		Generation: generation,
		Limit:      limit,
	}
	if err := request(ctx, socketPath, requestValue, &response); err != nil {
		return EventPage{}, err
	}
	return EventPage{
		Events:        response.Events,
		Generation:    response.Generation,
		ResetRequired: response.ResetRequired,
		Truncated:     response.Truncated,
		OldestID:      response.OldestID,
		LastID:        response.LastID,
	}, nil
}

type RequestError struct {
	Status  string
	Message string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("ble_service request failed (%s): %s", e.Status, e.Message)
}

func RequestWake(ctx context.Context, socketPath, reason string) (WakeResult, error) {
	var result WakeResult
	var response struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		WakeID    string `json:"wake_id"`
		Delivered bool   `json:"delivered"`
	}
	if err := request(ctx, socketPath, map[string]string{"op": "wake", "reason": reason}, &response); err != nil {
		return result, err
	}
	result.WakeID = response.WakeID
	result.Delivered = response.Delivered
	return result, nil
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

func request(ctx context.Context, socketPath string, requestValue any, responseValue any) error {
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect ble_service: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(requestDeadline(ctx, time.Now()))

	requestBytes, err := json.Marshal(requestValue)
	if err != nil {
		return err
	}
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
