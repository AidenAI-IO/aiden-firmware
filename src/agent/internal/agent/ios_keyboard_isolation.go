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
	defaultIOSKeyboardIsolationControl = "/oem/usr/bin/aiden-dynamic-keyboard"
	iosKeyboardProfileSwitchTimeout    = 30 * time.Second
	iosKeyboardRestoreRetryCooldown    = 5 * time.Second
)

type iosKeyboardIsolationCommand func(context.Context, string, ...string) ([]byte, error)

// iosKeyboardIsolationController serializes access to the three HID devices.
// Keyboard actions whose HID reports contain modifiers temporarily use a
// pointer-free USB profile. Unmodified text, pointer input, and Consumer
// Control actions do not trigger profile switching.
type iosKeyboardIsolationController struct {
	controlPath        string
	keyboardDev        *HIDDevice
	pointerDev         *HIDDevice
	extraKeysDev       *HIDDevice
	run                iosKeyboardIsolationCommand
	mu                 sync.Mutex
	needsRestore       bool
	lastRestoreErr     error
	lastRestoreFailure time.Time
}

func newIOSKeyboardIsolationController(cfg HIDConfig, keyboardDev, pointerDev, extraKeysDev *HIDDevice) *iosKeyboardIsolationController {
	if cfg.InputBackendADB() || cfg.PointerModeOrDefault() != "absolute" {
		return nil
	}
	info, err := os.Stat(defaultIOSKeyboardIsolationControl)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil
	}
	return &iosKeyboardIsolationController{
		controlPath:  defaultIOSKeyboardIsolationControl,
		keyboardDev:  keyboardDev,
		pointerDev:   pointerDev,
		extraKeysDev: extraKeysDev,
		run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, args...).CombinedOutput()
		},
	}
}

func (c *iosKeyboardIsolationController) closeHIDDevices() {
	if c == nil {
		return
	}
	if c.keyboardDev != nil {
		c.keyboardDev.Close()
	}
	if c.pointerDev != nil {
		c.pointerDev.Close()
	}
	if c.extraKeysDev != nil {
		c.extraKeysDev.Close()
	}
}

func (c *iosKeyboardIsolationController) switchProfile(command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), iosKeyboardProfileSwitchTimeout)
	defer cancel()
	output, err := c.run(ctx, c.controlPath, command)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("switch iOS HID profile with %s failed: %w", command, err)
	}
	return fmt.Errorf("switch iOS HID profile with %s failed: %w: %s", command, err, detail)
}

func (c *iosKeyboardIsolationController) ensureNormalProfileLocked() error {
	if !c.needsRestore {
		return nil
	}
	if c.lastRestoreErr != nil && time.Since(c.lastRestoreFailure) < iosKeyboardRestoreRetryCooldown {
		return c.lastRestoreErr
	}
	err := c.switchProfile("restore")
	c.recordRestoreResult(err)
	return err
}

func (c *iosKeyboardIsolationController) recordRestoreResult(err error) {
	c.needsRestore = err != nil
	c.lastRestoreErr = err
	if err != nil {
		c.lastRestoreFailure = time.Now()
	} else {
		c.lastRestoreFailure = time.Time{}
	}
}

func (c *iosKeyboardIsolationController) withKeyboard(ctx context.Context, isolate bool, action func() error) (err error) {
	if c == nil {
		return action()
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := c.ensureNormalProfileLocked(); err != nil {
		return err
	}
	if !isolate {
		return action()
	}

	c.closeHIDDevices()
	if isolateErr := c.switchProfile("isolate"); isolateErr != nil {
		restoreErr := c.switchProfile("restore")
		c.recordRestoreResult(restoreErr)
		return errors.Join(isolateErr, restoreErr)
	}
	c.needsRestore = true
	defer func() {
		c.closeHIDDevices()
		restoreErr := c.switchProfile("restore")
		c.recordRestoreResult(restoreErr)
		err = errors.Join(err, restoreErr)
	}()
	return action()
}

func (c *iosKeyboardIsolationController) withPointerCall(ctx context.Context, action func(context.Context) (string, error)) (string, error) {
	if c == nil {
		return action(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureNormalProfileLocked(); err != nil {
		return "", err
	}
	return action(ctx)
}

func (c *iosKeyboardIsolationController) withExtraKeys(action func() error) error {
	if c == nil {
		return action()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureNormalProfileLocked(); err != nil {
		return err
	}
	return action()
}
