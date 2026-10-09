package realtimevoice

import (
	"errors"
	"strings"
	"testing"
)

func TestGeminiServerErrorClassifiesIdleClose(t *testing.T) {
	tests := []struct {
		name    string
		message string
		idle    bool
	}{
		{
			name:    "observed idle close",
			message: "Your session was closed because no response was generated for 180 seconds",
			idle:    true,
		},
		{
			name:    "different idle window",
			message: "Your session was closed because no response was generated for 600 seconds",
			idle:    true,
		},
		{
			name:    "quota error keeps its plain form",
			message: "Quota exceeded for this project",
			idle:    false,
		},
		{
			name:    "unrelated session close",
			message: "Session closed: internal error",
			idle:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := geminiServerError(tt.message)
			if got := errors.Is(err, ErrSessionIdleTimeout); got != tt.idle {
				t.Errorf("errors.Is(err, ErrSessionIdleTimeout) = %t, want %t", got, tt.idle)
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error %q lost the provider message %q", err.Error(), tt.message)
			}
		})
	}
}
