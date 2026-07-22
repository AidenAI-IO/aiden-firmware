package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

const (
	defaultDynamicKeyboardControl = "/oem/usr/bin/aiden-dynamic-keyboard"
	// The script can spend up to 10 seconds waiting for a concurrent switch and
	// another 10 seconds waiting for host configuration. Keep the process alive
	// long enough for its EXIT trap to release the cross-process lock.
	dynamicKeyboardSwitchTimeout = 30 * time.Second
)

type dynamicKeyboardCommand func(context.Context, string, ...string) ([]byte, error)

type dynamicKeyboardSessionContextKey struct{}

type dynamicHIDMode string

const (
	dynamicHIDModeUnknown  dynamicHIDMode = ""
	dynamicHIDModePointer  dynamicHIDMode = "pointer"
	dynamicHIDModeKeyboard dynamicHIDMode = "keyboard"
)

type dynamicKeyboardSession struct {
	controller  *dynamicKeyboardController
	mode        dynamicHIDMode
	profileUsed bool
}

type dynamicKeyboardSessionCall func(context.Context) (string, error)

type dynamicKeyboardSessionTool struct {
	inner      langtools.Tool
	controller *dynamicKeyboardController
}

func newDynamicKeyboardSessionTool(inner langtools.Tool, controller *dynamicKeyboardController) langtools.Tool {
	if controller == nil {
		return inner
	}
	return &dynamicKeyboardSessionTool{inner: inner, controller: controller}
}

func (t *dynamicKeyboardSessionTool) Name() string        { return t.inner.Name() }
func (t *dynamicKeyboardSessionTool) Description() string { return t.inner.Description() }
func (t *dynamicKeyboardSessionTool) Call(ctx context.Context, input string) (string, error) {
	return t.controller.withSessionCall(ctx, func(sessionCtx context.Context) (string, error) {
		return t.inner.Call(sessionCtx, input)
	})
}

func (t *dynamicKeyboardSessionTool) ArgsSchema() map[string]any {
	structured, ok := t.inner.(structuredInputTool)
	if !ok {
		return nil
	}
	return structured.ArgsSchema()
}

func (t *dynamicKeyboardSessionTool) SetPlatformFn(fn func() string) {
	type platformConfigurable interface {
		SetPlatformFn(func() string)
	}
	if tool, ok := t.inner.(platformConfigurable); ok {
		tool.SetPlatformFn(fn)
	}
}

// dynamicKeyboardController owns every pointer/keyboard profile transition.
// One Agent process can therefore never interleave two UDC re-enumerations or
// write through a HID fd that belongs to the previous USB profile.
type dynamicKeyboardController struct {
	controlPath string
	keyboardDev *HIDDevice // standard keyboard (/dev/hidg0)
	pointerDev  *HIDDevice // pointer/touch (/dev/hidg1)
	run         dynamicKeyboardCommand
	mu          sync.Mutex
	mode        dynamicHIDMode
}

func newDynamicKeyboardController(cfg HIDConfig, keyboardDev, pointerDev *HIDDevice) *dynamicKeyboardController {
	if !cfg.DynamicKeyboard {
		return nil
	}
	return &dynamicKeyboardController{
		controlPath: cfg.DynamicKeyboardControlOrDefault(),
		keyboardDev: keyboardDev,
		pointerDev:  pointerDev,
		run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, args...).CombinedOutput()
		},
	}
}

func (c *dynamicKeyboardController) closeHIDDevices() {
	if c == nil {
		return
	}
	if c.keyboardDev != nil {
		c.keyboardDev.Close()
	}
	if c.pointerDev != nil {
		c.pointerDev.Close()
	}
}

func (c *dynamicKeyboardController) switchProfile(ctx context.Context, mode dynamicHIDMode) error {
	if c == nil {
		return nil
	}
	command := ""
	switch mode {
	case dynamicHIDModeKeyboard:
		command = "on"
	case dynamicHIDModePointer:
		command = "off"
	default:
		return fmt.Errorf("unknown dynamic HID mode %q", mode)
	}
	output, err := c.run(ctx, c.controlPath, command)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("switch dynamic HID profile to %s failed: %w", mode, err)
	}
	return fmt.Errorf("switch dynamic HID profile to %s failed: %w: %s", mode, err, detail)
}

func (c *dynamicKeyboardController) sessionFromContext(ctx context.Context) *dynamicKeyboardSession {
	if c == nil || ctx == nil {
		return nil
	}
	session, _ := ctx.Value(dynamicKeyboardSessionContextKey{}).(*dynamicKeyboardSession)
	if session == nil || session.controller != c {
		return nil
	}
	return session
}

func (c *dynamicKeyboardController) setMode(session *dynamicKeyboardSession, mode dynamicHIDMode) {
	session.mode = mode
	c.mode = mode
}

// withSessionCall keeps one serialized HID profile session across one
// high-level input transaction. Keyboard and pointer subtools may switch
// profiles multiple times, and the pointer profile is restored before the
// transaction returns, fails, or is canceled.
func (c *dynamicKeyboardController) withSessionCall(ctx context.Context, action dynamicKeyboardSessionCall) (output string, err error) {
	if c == nil {
		return action(ctx)
	}
	if c.sessionFromContext(ctx) != nil {
		return action(ctx)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	session := &dynamicKeyboardSession{controller: c, mode: c.mode}
	sessionCtx := context.WithValue(ctx, dynamicKeyboardSessionContextKey{}, session)
	defer func() {
		if !session.profileUsed || session.mode == dynamicHIDModePointer {
			return
		}
		c.closeHIDDevices()
		offCtx, cancel := context.WithTimeout(context.Background(), dynamicKeyboardSwitchTimeout)
		defer cancel()
		offErr := c.switchProfile(offCtx, dynamicHIDModePointer)
		if offErr == nil {
			c.setMode(session, dynamicHIDModePointer)
		} else {
			c.setMode(session, dynamicHIDModeUnknown)
		}
		err = errors.Join(err, offErr)
	}()

	return action(sessionCtx)
}

func (c *dynamicKeyboardController) ensureSessionMode(session *dynamicKeyboardSession, mode dynamicHIDMode) error {
	if session == nil {
		return errors.New("dynamic HID session is missing")
	}
	if session.mode == mode {
		return nil
	}

	if mode != dynamicHIDModeKeyboard && mode != dynamicHIDModePointer {
		return fmt.Errorf("unknown dynamic HID mode %q", mode)
	}

	session.profileUsed = true
	c.closeHIDDevices()
	switchCtx, cancel := context.WithTimeout(context.Background(), dynamicKeyboardSwitchTimeout)
	switchErr := c.switchProfile(switchCtx, mode)
	cancel()
	if switchErr == nil {
		c.setMode(session, mode)
		return nil
	}
	c.setMode(session, dynamicHIDModeUnknown)

	if mode != dynamicHIDModeKeyboard {
		return switchErr
	}
	// A failed keyboard enumeration must not strand the phone without its
	// pointer profile. Restore the normal profile before returning the error.
	offCtx, offCancel := context.WithTimeout(context.Background(), dynamicKeyboardSwitchTimeout)
	offErr := c.switchProfile(offCtx, dynamicHIDModePointer)
	offCancel()
	if offErr == nil {
		c.setMode(session, dynamicHIDModePointer)
	}
	return errors.Join(switchErr, offErr)
}

func (c *dynamicKeyboardController) withKeyboard(ctx context.Context, action func() error) (err error) {
	if c == nil {
		return action()
	}
	if session := c.sessionFromContext(ctx); session != nil {
		if err := c.ensureSessionMode(session, dynamicHIDModeKeyboard); err != nil {
			return err
		}
		err := action()
		if err != nil {
			c.closeHIDDevices()
		}
		if errors.Is(err, os.ErrNotExist) {
			// The gadget can be switched externally while a transaction-owned
			// session is active (for example by config maintenance or recovery).
			// Re-enumerate once instead of leaving every later keyboard action in
			// the run stuck on a vanished /dev/hidg0.
			c.setMode(session, dynamicHIDModeUnknown)
			if reattachErr := c.ensureSessionMode(session, dynamicHIDModeKeyboard); reattachErr != nil {
				return errors.Join(err, reattachErr)
			}
			return action()
		}
		return err
	}

	_, err = c.withSessionCall(ctx, func(sessionCtx context.Context) (string, error) {
		return "", c.withKeyboard(sessionCtx, action)
	})
	return err
}

func (c *dynamicKeyboardController) withPointerCall(ctx context.Context, action dynamicKeyboardSessionCall) (string, error) {
	if c == nil {
		return action(ctx)
	}
	if session := c.sessionFromContext(ctx); session != nil {
		if err := c.ensureSessionMode(session, dynamicHIDModePointer); err != nil {
			return "", err
		}
		return action(ctx)
	}
	return c.withSessionCall(ctx, func(sessionCtx context.Context) (string, error) {
		return c.withPointerCall(sessionCtx, action)
	})
}
