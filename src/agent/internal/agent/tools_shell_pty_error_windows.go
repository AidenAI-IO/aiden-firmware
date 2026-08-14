//go:build windows

package agent

import (
	"errors"
	"io"
)

func shellPTYCopyError(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
