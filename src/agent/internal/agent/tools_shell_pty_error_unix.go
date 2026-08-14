//go:build unix

package agent

import (
	"errors"
	"io"
	"syscall"
)

func shellPTYCopyError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) {
		return nil
	}
	return err
}
