package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	phoneBridgeRestoreTimeout      = 8 * time.Second
	phoneBridgeRestorePollInterval = 100 * time.Millisecond
	returnEntryDynamicIslandX      = 500.0
	returnEntryDynamicIslandY      = 30.0
)

// PhoneBridgeRestorer turns a confirmed Dynamic Island return entry into a
// foreground bridge before app-side tools send their command.
type PhoneBridgeRestorer struct {
	mu             sync.Mutex
	bridge         *PhoneBridge
	pc             *pointerController
	tapReturnEntry func(context.Context, PhoneBridgeStatus) error
	waitTimeout    time.Duration
}

func NewPhoneBridgeRestorer(bridge *PhoneBridge, pc *pointerController) *PhoneBridgeRestorer {
	return &PhoneBridgeRestorer{
		bridge:      bridge,
		pc:          pc,
		waitTimeout: phoneBridgeRestoreTimeout,
	}
}

func (r *PhoneBridgeRestorer) SetBridge(bridge *PhoneBridge) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.bridge = bridge
	r.mu.Unlock()
}

func (r *PhoneBridgeRestorer) EnsureForeground(ctx context.Context) (bool, error) {
	if r == nil {
		return false, fmt.Errorf("phone bridge restore is not configured")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bridge := r.bridge
	if bridge == nil {
		return false, fmt.Errorf("phone bridge is not initialized")
	}
	status := bridge.Status()
	if phoneBridgeReadyForCommand(status) {
		return false, nil
	}
	if !phoneBridgeCanRestoreFromReturnEntry(status) {
		return false, phoneBridgeRestoreUnavailableError(status)
	}

	if bridge.logger != nil {
		bridge.logger.Info("phone-bridge: restoring foreground via %s before app command", firstNonEmptyPhoneField(status.ReturnEntry, "return_entry"))
	}
	if err := r.tap(ctx, status); err != nil {
		return false, fmt.Errorf("tap Dynamic Island return entry: %w", err)
	}
	if _, err := r.waitForeground(ctx); err != nil {
		return true, err
	}
	return true, nil
}

func (r *PhoneBridgeRestorer) tap(ctx context.Context, status PhoneBridgeStatus) error {
	if r.tapReturnEntry != nil {
		return r.tapReturnEntry(ctx, status)
	}
	if r.pc == nil {
		return fmt.Errorf("no HID pointer controller configured")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	x, y := phoneBridgeDynamicIslandTapPoint()
	absX, absY := normalizedToAbsolutePoint(x, y)
	return tapPointer(r.pc, absX, absY, mouseButtonByte("left"))
}

func (r *PhoneBridgeRestorer) waitForeground(ctx context.Context) (PhoneBridgeStatus, error) {
	timeout := r.waitTimeout
	if timeout <= 0 {
		timeout = phoneBridgeRestoreTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(phoneBridgeRestorePollInterval)
	defer ticker.Stop()

	for {
		status := r.bridge.Status()
		if phoneBridgeReadyForCommand(status) {
			return status, nil
		}
		select {
		case <-waitCtx.Done():
			return status, fmt.Errorf("phone bridge did not return to foreground within %s (connected=%t app_state=%s return_entry=%s available=%t)", timeout, status.Connected, status.AppState, status.ReturnEntry, phoneBridgeHasReturnEntry(status))
		case <-ticker.C:
		}
	}
}

func ensurePhoneBridgeReadyForCommand(ctx context.Context, bridge *PhoneBridge, restorer *PhoneBridgeRestorer) (bool, error) {
	if bridge == nil {
		return false, fmt.Errorf("phone bridge is not initialized")
	}
	if phoneBridgeReadyForCommand(bridge.Status()) {
		return false, nil
	}
	if restorer == nil {
		return false, phoneBridgeRestoreUnavailableError(bridge.Status())
	}
	if restorer.bridge == nil {
		restorer.SetBridge(bridge)
	}
	return restorer.EnsureForeground(ctx)
}

func sendForegroundBridgeCommand(ctx context.Context, bridge *PhoneBridge, restorer *PhoneBridgeRestorer, cmd BridgeCommand) (BridgeCommandResponse, bool, error) {
	restored, err := ensurePhoneBridgeReadyForCommand(ctx, bridge, restorer)
	if err != nil {
		return BridgeCommandResponse{}, restored, err
	}
	resp, err := bridge.SendCommand(ctx, cmd)
	return resp, restored, err
}

func phoneBridgeReadyForCommand(status PhoneBridgeStatus) bool {
	if !status.Connected {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(status.Platform), "ios") {
		return true
	}
	state := strings.ToLower(strings.TrimSpace(status.AppState))
	return state == "" || state == "active"
}

func phoneBridgeCanRestoreFromReturnEntry(status PhoneBridgeStatus) bool {
	if status.ReturnEntryAvailable == nil || !*status.ReturnEntryAvailable {
		return false
	}
	entry := strings.ToLower(strings.TrimSpace(status.ReturnEntry))
	return entry == "dynamic_island"
}

func phoneBridgeRestoreUnavailableError(status PhoneBridgeStatus) error {
	if status.Connected {
		state := strings.TrimSpace(status.AppState)
		if state == "" {
			return fmt.Errorf("phone bridge is connected but not ready for foreground command")
		}
		return fmt.Errorf("phone bridge app is %s and no supported Dynamic Island return entry is available", state)
	}
	return fmt.Errorf("phone bridge not connected and no supported Dynamic Island return entry is available")
}

func phoneBridgeDynamicIslandTapPoint() (float64, float64) {
	return returnEntryDynamicIslandX, returnEntryDynamicIslandY
}
