package backup

import "fmt"

type Error struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Cause }

func errorf(code string, cause error, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Cause: cause}
}

func ErrorCode(err error) string {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			return typed.Code
		}
		type unwrapper interface{ Unwrap() error }
		if value, ok := err.(unwrapper); ok {
			err = value.Unwrap()
		} else {
			break
		}
	}
	return ""
}
