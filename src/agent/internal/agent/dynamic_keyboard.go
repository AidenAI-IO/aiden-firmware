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
	controller *dynamicKeyboardController
	mode       dynamicHIDMode
}

type dynamicKeyboardSessionCall func(context.Context) (string, error)

// dynamicKeyboardController owns every pointer/keyboard profile transition.
// One Agent process can therefore never interleave two UDC re-enumerations or
// write through a HID fd that belongs to the previous USB profile.
type dynamicKeyboardController struct {
	enabled     bool
	controlPath string
	dev         *HIDDevice // standard keyboard (/dev/hidg0)
	pointerDev  *HIDDevice // pointer/touch (/dev/hidg1)
	run         dynamicKeyboardCommand
	mu          sync.Mutex
}

func newDynamicKeyboardController(cfg HIDConfig, dev, pointerDev *HIDDevice) *dynamicKeyboardController {
	if !cfg.DynamicKeyboard {
		return nil
	}
	return &dynamicKeyboardController{
		enabled:     true,
		controlPath: cfg.DynamicKeyboardControlOrDefault(),
		dev:         dev,
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
	if c.dev != nil {
		c.dev.Close()
	}
	if c.pointerDev != nil {
		c.pointerDev.Close()
	}
}

func (c *dynamicKeyboardController) switchMode(ctx context.Context, mode string) error {
	if c == nil || !c.enabled {
		return nil
	}
	output, err := c.run(ctx, c.controlPath, mode)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("dynamic keyboard %s failed: %w", mode, err)
	}
	return fmt.Errorf("dynamic keyboard %s failed: %w: %s", mode, err, detail)
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

// withSessionCall keeps one serialized HID profile session across its owner
// scope. Runtime.Run uses that scope for the whole Agent loop, while direct HTTP
// and nested high-level tools use one tool transaction. Keyboard and pointer
// tools may switch profiles multiple times, and the pointer profile is restored
// before the owner returns, fails, or is canceled.
func (c *dynamicKeyboardController) withSessionCall(ctx context.Context, action dynamicKeyboardSessionCall) (output string, err error) {
	if c == nil || !c.enabled {
		return action(ctx)
	}
	if c.sessionFromContext(ctx) != nil {
		return action(ctx)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	session := &dynamicKeyboardSession{controller: c}
	sessionCtx := context.WithValue(ctx, dynamicKeyboardSessionContextKey{}, session)
	defer func() {
		if session.mode != dynamicHIDModeKeyboard {
			return
		}
		c.closeHIDDevices()
		offCtx, cancel := context.WithTimeout(context.Background(), dynamicKeyboardSwitchTimeout)
		defer cancel()
		offErr := c.switchMode(offCtx, "off")
		if offErr == nil {
			session.mode = dynamicHIDModePointer
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

	command := "off"
	if mode == dynamicHIDModeKeyboard {
		command = "on"
	} else if mode != dynamicHIDModePointer {
		return fmt.Errorf("unknown dynamic HID mode %q", mode)
	}

	c.closeHIDDevices()
	switchCtx, cancel := context.WithTimeout(context.Background(), dynamicKeyboardSwitchTimeout)
	switchErr := c.switchMode(switchCtx, command)
	cancel()
	if switchErr == nil {
		session.mode = mode
		return nil
	}
	session.mode = dynamicHIDModeUnknown

	if mode != dynamicHIDModeKeyboard {
		return switchErr
	}
	// A failed keyboard enumeration must not strand the phone without its
	// pointer profile. Restore the normal profile before returning the error.
	offCtx, offCancel := context.WithTimeout(context.Background(), dynamicKeyboardSwitchTimeout)
	offErr := c.switchMode(offCtx, "off")
	offCancel()
	if offErr == nil {
		session.mode = dynamicHIDModePointer
	}
	return errors.Join(switchErr, offErr)
}

func (c *dynamicKeyboardController) withKeyboard(ctx context.Context, action func() error) (err error) {
	if c == nil || !c.enabled {
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
			// The gadget can be switched externally while a run-owned session is
			// active (for example by config maintenance or watchdog recovery).
			// Re-enumerate once instead of leaving every later keyboard action in
			// the run stuck on a vanished /dev/hidg0.
			session.mode = dynamicHIDModeUnknown
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
	if c == nil || !c.enabled {
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
