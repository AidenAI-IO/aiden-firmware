//go:build unix

package agent

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
)

func TestShellPTYCopyErrorTreatsTerminalReadErrorsAsEOF(t *testing.T) {
	for _, err := range []error{
		nil,
		io.EOF,
		fmt.Errorf("wrapped EOF: %w", io.EOF),
		syscall.EIO,
		fmt.Errorf("wrapped EIO: %w", syscall.EIO),
	} {
		if got := shellPTYCopyError(err); got != nil {
			t.Errorf("shellPTYCopyError(%v) = %v, want nil", err, got)
		}
	}
}

func TestShellPTYCopyErrorPreservesUnexpectedErrors(t *testing.T) {
	want := errors.New("unexpected PTY read failure")
	if got := shellPTYCopyError(want); !errors.Is(got, want) {
		t.Fatalf("shellPTYCopyError(%v) = %v, want original error", want, got)
	}
}
