package agent

import (
	"context"
	"fmt"
	"strings"
)

// EnsureADBReverse forwards a TCP port on the Android device back to the host.
// It lets the companion app connect to a computer-hosted Agent through
// http://127.0.0.1:<devicePort> when adb input backend is used locally.
func EnsureADBReverse(ctx context.Context, devicePort, hostPort string) error {
	devicePort = strings.TrimSpace(devicePort)
	hostPort = strings.TrimSpace(hostPort)
	if devicePort == "" || hostPort == "" {
		return fmt.Errorf("devicePort and hostPort are required")
	}

	client := NewADBScreenClient()
	adbPath, err := client.adbPath()
	if err != nil {
		return err
	}
	serial, err := client.resolveSerial(ctx, adbPath)
	if err != nil {
		return err
	}

	args := make([]string, 0, 6)
	if serial != "" {
		args = append(args, "-s", serial)
	}
	args = append(args, "reverse", "tcp:"+devicePort, "tcp:"+hostPort)
	stdout, stderr, err := runADBCommand(ctx, adbPath, args...)
	if err != nil {
		if detail := strings.TrimSpace(string(stderr)); detail != "" {
			return fmt.Errorf("adb reverse failed: %s", detail)
		}
		if detail := strings.TrimSpace(string(stdout)); detail != "" {
			return fmt.Errorf("adb reverse failed: %s", detail)
		}
		return err
	}
	return nil
}
