package tts

import "errors"

var (
	ErrProviderNotFound    = errors.New("tts: provider not found")
	ErrConnectionFailed    = errors.New("tts: connection failed")
	ErrInsufficientBalance = errors.New("tts: insufficient balance")
	ErrAuthFailed          = errors.New("tts: authentication failed")
	ErrSessionClosed       = errors.New("tts: session already closed")
	ErrRateLimited         = errors.New("tts: rate limited")
	ErrUnsupportedFormat   = errors.New("tts: unsupported audio format")
)
