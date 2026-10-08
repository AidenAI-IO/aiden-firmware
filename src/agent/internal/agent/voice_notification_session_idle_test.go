package agent

import (
	"errors"
	"testing"
)

func TestTurnFailureFromError_SessionIdle(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "gemini 180 seconds timeout",
			err:      errors.New("Your session was closed because no response was generated for 180 seconds"),
			wantCode: TurnFailureSessionIdle,
		},
		{
			name:     "session closed partial match",
			err:      errors.New("session was closed because no response was sent"),
			wantCode: TurnFailureSessionIdle,
		},
		{
			name:     "generic error should not match",
			err:      errors.New("some other error"),
			wantCode: TurnFailureLLMUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failure := TurnFailureFromError(tt.err)
			if failure == nil {
				t.Fatal("expected failure, got nil")
			}
			if failure.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", failure.Code, tt.wantCode)
			}
		})
	}
}

func TestDefaultTurnFailureVoiceNotificationText_SessionIdle(t *testing.T) {
	tests := []struct {
		name   string
		locale string
		want   string
	}{
		{
			name:   "zh-cn",
			locale: "zh-cn",
			want:   "长时间没有对话，本次会话已自动结束，你可以随时开始新的对话。",
		},
		{
			name:   "en",
			locale: "en-US",
			want:   "The conversation has ended due to inactivity. Feel free to start a new one anytime.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultTurnFailureVoiceNotificationText(tt.locale, TurnFailureSessionIdle)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
