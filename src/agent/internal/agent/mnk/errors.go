package mnk

import (
	"errors"
	"fmt"
)

// ErrorKind classifies adapter/provider failures so agent tools can map them
// onto structured ToolError codes without importing the agent package.
type ErrorKind string

const (
	// ErrInvalidArguments is a caller/input problem (bad JSON, missing fields, etc).
	ErrInvalidArguments ErrorKind = "invalid_arguments"
	// ErrModuleUnavailable means the backend/device is not configured or usable.
	ErrModuleUnavailable ErrorKind = "module_unavailable"
	// ErrExecutionFailed is a runtime failure while performing a valid request.
	ErrExecutionFailed ErrorKind = "execution_failed"
)

// Error is a typed MNK failure used by adapters and providers.
type Error struct {
	Kind ErrorKind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" {
		return e.Msg
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AsError extracts an *Error from err, including wrapped values.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// InvalidArgumentsf returns an invalid-arguments error.
func InvalidArgumentsf(format string, args ...any) error {
	return &Error{Kind: ErrInvalidArguments, Msg: fmt.Sprintf(format, args...)}
}

// InvalidArguments returns an invalid-arguments error with a fixed message.
func InvalidArguments(message string) error {
	return &Error{Kind: ErrInvalidArguments, Msg: message}
}

// ModuleUnavailablef returns a module-unavailable error.
func ModuleUnavailablef(format string, args ...any) error {
	return &Error{Kind: ErrModuleUnavailable, Msg: fmt.Sprintf(format, args...)}
}

// ModuleUnavailable returns a module-unavailable error with a fixed message.
func ModuleUnavailable(message string) error {
	return &Error{Kind: ErrModuleUnavailable, Msg: message}
}

// ExecutionFailedf returns an execution-failed error.
func ExecutionFailedf(format string, args ...any) error {
	return &Error{Kind: ErrExecutionFailed, Msg: fmt.Sprintf(format, args...)}
}

// WrapExecutionFailed wraps a provider/runtime error as execution-failed,
// preserving typed MNK errors that already carry a kind.
func WrapExecutionFailed(err error) error {
	if err == nil {
		return nil
	}
	if e := AsError(err); e != nil {
		return e
	}
	return &Error{Kind: ErrExecutionFailed, Msg: err.Error(), Err: err}
}
