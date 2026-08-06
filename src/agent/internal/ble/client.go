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

func RequestWake(ctx context.Context, socketPath, reason string) (WakeResult, error) {
	var result WakeResult
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return result, fmt.Errorf("connect ble_service: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}

	request, err := json.Marshal(map[string]string{"op": "wake", "reason": reason})
	if err != nil {
		return result, err
	}
	if err := writeUDSMessage(connection, request, nil); err != nil {
		return result, fmt.Errorf("write ble_service wake: %w", err)
	}
	header, _, err := readUDSMessage(connection)
	if err != nil {
		return result, fmt.Errorf("read ble_service wake: %w", err)
	}
	var response struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		WakeID    string `json:"wake_id"`
		Delivered bool   `json:"delivered"`
	}
	if err := json.Unmarshal(header, &response); err != nil {
		return result, fmt.Errorf("decode ble_service wake: %w", err)
	}
	if response.Status != "OK" {
		return result, fmt.Errorf("ble_service wake failed (%s): %s", response.Status, response.Error)
	}
	result.WakeID = response.WakeID
	result.Delivered = response.Delivered
	return result, nil
}
