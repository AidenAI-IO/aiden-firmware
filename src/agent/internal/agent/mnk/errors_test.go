package mnk

import (
	"context"
	"errors"
	"testing"
)

func TestAdapterValidationErrorsAreInvalidArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		call  func() error
		want  string
		kind  ErrorKind
	}{
		{
			name: "keyboard empty keys",
			call: func() error {
				_, err := NewKeyboardTapToolAdapter(NewMockProvider()).Call(context.Background(), `{"keys":[]}`)
				return err
			},
			want: "keys array is required",
			kind: ErrInvalidArguments,
		},
		{
			name: "keyboard invalid json",
			call: func() error {
				_, err := NewKeyboardTapToolAdapter(NewMockProvider()).Call(context.Background(), `{`)
				return err
			},
			kind: ErrInvalidArguments,
		},
		{
			name: "touch missing type",
			call: func() error {
				_, err := NewTouchGestureToolAdapter(NewMockProvider(), nil).Call(context.Background(), `{}`)
				return err
			},
			want: "type is required",
			kind: ErrInvalidArguments,
		},
		{
			name: "mouse scroll out of range",
			call: func() error {
				_, err := NewMouseScrollToolAdapter(NewMockProvider()).Call(context.Background(), `{"delta": 200}`)
				return err
			},
			want: "delta must be between -127 and 127",
			kind: ErrInvalidArguments,
		},
		{
			name: "keyboard nil provider",
			call: func() error {
				_, err := NewKeyboardTapToolAdapter(nil).Call(context.Background(), `{"keys":["enter"]}`)
				return err
			},
			want: "keyboard_tap is not configured",
			kind: ErrModuleUnavailable,
		},
		{
			name: "touch nil provider with valid args",
			call: func() error {
				_, err := NewTouchGestureToolAdapter(nil, nil).Call(context.Background(), `{"type":"tap","point":{"x":1,"y":1}}`)
				return err
			},
			want: "touch_gesture is not configured",
			kind: ErrModuleUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			got := AsError(err)
			if got == nil {
				t.Fatalf("AsError(%v) = nil, want kind %q", err, tc.kind)
			}
			if got.Kind != tc.kind {
				t.Fatalf("Kind = %q, want %q (err=%v)", got.Kind, tc.kind, err)
			}
			if tc.want != "" && got.Error() != tc.want {
				t.Fatalf("Error() = %q, want %q", got.Error(), tc.want)
			}
			if !errors.As(err, new(*Error)) {
				t.Fatalf("errors.As failed for %v", err)
			}
		})
	}
}

func TestWrapExecutionFailedPreservesTypedError(t *testing.T) {
	t.Parallel()
	inner := InvalidArguments("bad")
	wrapped := WrapExecutionFailed(inner)
	got := AsError(wrapped)
	if got == nil || got.Kind != ErrInvalidArguments || got.Error() != "bad" {
		t.Fatalf("WrapExecutionFailed preserved typed error incorrectly: %#v", got)
	}
}
