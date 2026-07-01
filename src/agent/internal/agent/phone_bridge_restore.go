package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	phoneBridgeRestoreTimeout      = 8 * time.Second
	phoneBridgeRestorePollInterval = 100 * time.Millisecond
	phoneBridgeRestoreFailureCache = 30 * time.Second
	returnEntryDynamicIslandX      = 500.0
	returnEntryDynamicIslandY      = 30.0
)

// PhoneBridgeRestorer turns a confirmed Dynamic Island return entry into a
// foreground bridge before app-side tools send their command.
type PhoneBridgeRestorer struct {
	mu             sync.Mutex
	bridge         *PhoneBridge
	pc             *pointerController
	screen         *screenState
	tapReturnEntry func(context.Context, PhoneBridgeStatus) error
	waitTimeout    time.Duration
	failureCache   time.Duration
	lastFailure    phoneBridgeRestoreFailure
	now            func() time.Time
}

type phoneBridgeRestoreFailure struct {
	status PhoneBridgeStatus
	at     time.Time
	err    error
}

func NewPhoneBridgeRestorer(bridge *PhoneBridge, pc *pointerController, screens ...*screenState) *PhoneBridgeRestorer {
	var screen *screenState
	if len(screens) > 0 {
		screen = screens[0]
	}
	return &PhoneBridgeRestorer{
		bridge:       bridge,
		pc:           pc,
		screen:       screen,
		waitTimeout:  phoneBridgeRestoreTimeout,
		failureCache: phoneBridgeRestoreFailureCache,
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
		r.clearLastFailureLocked()
		return false, nil
	}
	if err := r.cachedFailureLocked(status); err != nil {
		if bridge.logger != nil {
			bridge.logger.Info("phone-bridge: foreground restore suppressed after recent failure: %v", err)
		}
		return false, err
	}
	if !phoneBridgeCanRestoreFromReturnEntry(status) {
		return false, phoneBridgeRestoreUnavailableError(status)
	}

	if bridge.logger != nil {
		bridge.logger.Info("phone-bridge: restoring foreground via %s before app command", firstNonEmptyPhoneField(status.ReturnEntry, "return_entry"))
	}
	if err := r.tap(ctx, status); err != nil {
		if contextErr := phoneBridgeCallerContextErr(ctx, err); contextErr != nil {
			return false, contextErr
		}
		err = &phoneBridgeReturnEntryTapError{entry: firstNonEmptyPhoneField(status.ReturnEntry, "return_entry"), err: err}
		r.recordFailureLocked(status, err)
		return false, err
	}
	if _, err := r.waitForeground(ctx); err != nil {
		if contextErr := phoneBridgeCallerContextErr(ctx, err); contextErr != nil {
			return true, contextErr
		}
		r.recordFailureLocked(status, err)
		return true, err
	}
	r.clearLastFailureLocked()
	return true, nil
}

func (r *PhoneBridgeRestorer) cachedFailureLocked(status PhoneBridgeStatus) error {
	if r == nil || r.lastFailure.err == nil || r.lastFailure.at.IsZero() {
		return nil
	}
	cache := r.failureCache
	if cache <= 0 {
		cache = phoneBridgeRestoreFailureCache
	}
	if r.nowTime().Sub(r.lastFailure.at) >= cache {
		r.clearLastFailureLocked()
		return nil
	}
	if !phoneBridgeRestoreFailureApplies(status, r.lastFailure.status) {
		r.clearLastFailureLocked()
		return nil
	}
	return &phoneBridgeRestoreSuppressedError{
		until: r.lastFailure.at.Add(cache),
		cause: r.lastFailure.err,
	}
}

func (r *PhoneBridgeRestorer) recordFailureLocked(status PhoneBridgeStatus, err error) {
	if r == nil || err == nil {
		return
	}
	r.lastFailure = phoneBridgeRestoreFailure{
		status: status,
		at:     r.nowTime(),
		err:    err,
	}
}

func (r *PhoneBridgeRestorer) clearLastFailureLocked() {
	if r == nil {
		return
	}
	r.lastFailure = phoneBridgeRestoreFailure{}
}

func phoneBridgeCallerContextErr(ctx context.Context, err error) error {
	if ctx == nil || err == nil {
		return nil
	}
	contextErr := ctx.Err()
	if contextErr == nil {
		return nil
	}
	if errors.Is(err, contextErr) {
		return contextErr
	}
	return nil
}

func (r *PhoneBridgeRestorer) nowTime() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}

func phoneBridgeRestoreFailureApplies(current, failed PhoneBridgeStatus) bool {
	if phoneBridgeReadyForCommand(current) {
		return false
	}
	if current.Connected != failed.Connected {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(current.Platform), strings.TrimSpace(failed.Platform)) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(current.AppState), strings.TrimSpace(failed.AppState)) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(current.ReturnEntry), strings.TrimSpace(failed.ReturnEntry)) {
		return false
	}
	return phoneBridgeReturnEntryAvailability(current) == phoneBridgeReturnEntryAvailability(failed)
}

func phoneBridgeReturnEntryAvailability(status PhoneBridgeStatus) string {
	if status.ReturnEntryAvailable == nil {
		return "unknown"
	}
	if *status.ReturnEntryAvailable {
		return "true"
	}
	return "false"
}

type phoneBridgeReturnEntryTapError struct {
	entry string
	err   error
}

func (e *phoneBridgeReturnEntryTapError) Error() string {
	if e == nil {
		return ""
	}
	entry := firstNonEmptyPhoneField(e.entry, "return entry")
	if e.err == nil {
		return fmt.Sprintf("tap %s return entry failed", entry)
	}
	return fmt.Sprintf("tap %s return entry failed: %v", entry, e.err)
}

func (e *phoneBridgeReturnEntryTapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type phoneBridgeRestoreSuppressedError struct {
	until time.Time
	cause error
}

func (e *phoneBridgeRestoreSuppressedError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return "phone bridge foreground restore suppressed after recent failure"
	}
	if e.until.IsZero() {
		return fmt.Sprintf("phone bridge foreground restore suppressed after recent failure: %v", e.cause)
	}
	return fmt.Sprintf("phone bridge foreground restore suppressed after recent failure until %s: %v", e.until.Format(time.RFC3339), e.cause)
}

func (e *phoneBridgeRestoreSuppressedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
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
	absX, absY, err := normalizedToAbsolutePointForSurface(r.screen, r.pc.touchscreen, x, y)
	if err != nil {
		return fmt.Errorf("resolve %s return entry tap point: %w", firstNonEmptyPhoneField(status.ReturnEntry, "return_entry"), err)
	}
	if r.bridge != nil && r.bridge.logger != nil {
		r.bridge.logger.Info("phone-bridge: tapping %s return entry at normalized=(%.0f,%.0f) absolute=(%d,%d) pointer_mode=%s", firstNonEmptyPhoneField(status.ReturnEntry, "return_entry"), x, y, absX, absY, pointerControllerModeName(r.pc))
	}
	return tapPointer(r.pc, absX, absY, mouseButtonByte("left"))
}

func pointerControllerModeName(pc *pointerController) string {
	if pc != nil && pc.touchscreen {
		return "touchscreen"
	}
	return "absolute"
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
			if err := ctx.Err(); err != nil {
				return status, err
			}
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

func phoneBridgeCommandPreconditionToolError(status PhoneBridgeStatus, err error) *ToolError {
	if err == nil {
		return nil
	}
	var tapErr *phoneBridgeReturnEntryTapError
	if errors.As(err, &tapErr) {
		return NewToolErrorWithDetails(CodeAppBackgrounded,
			"Aiden companion app is backgrounded and automatic return-entry restore failed because HID input is unavailable. This is not a Phone Bridge WebSocket disconnect; do not retry companion-app tools until Aiden is foreground or HID input is restored.",
			map[string]any{
				"return_entry":  firstNonEmptyPhoneField(status.ReturnEntry, tapErr.entry),
				"connected":     status.Connected,
				"app_state":     status.AppState,
				"restore_error": tapErr.Error(),
			})
	}
	if phoneBridgeAppNeedsForeground(status) {
		return NewToolErrorWithDetails(CodeAppBackgrounded, err.Error(), map[string]any{
			"connected":    status.Connected,
			"app_state":    status.AppState,
			"return_entry": status.ReturnEntry,
		})
	}
	return NewToolError(CodeBridgeNotConnected, err.Error())
}

func phoneBridgeCommandPreconditionToolErrorFromBridge(bridge *PhoneBridge, err error) *ToolError {
	status := PhoneBridgeStatus{}
	if bridge != nil {
		status = bridge.Status()
	}
	return phoneBridgeCommandPreconditionToolError(status, err)
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
