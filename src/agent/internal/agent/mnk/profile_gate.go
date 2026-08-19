package mnk

import "context"

// ProfileGate serializes HID access and switches the iOS absolute-mouse USB
// profile when modifier keyboard chords conflict with the pointer gadget.
//
// Within an agent-loop batch context, WithKeyboard(isolate=true) sticks until
// WithPointer or the batch ends restores the normal (pointer-present) profile.
// A nil ProfileGate is a no-op (ADB / remote HTTP / non-iOS paths).
type ProfileGate interface {
	WithKeyboard(ctx context.Context, isolate bool, fn func() error) error
	WithExtraKeys(ctx context.Context, fn func() error) error
	WithPointer(ctx context.Context, fn func() error) error
}

// Device is the minimal HID gadget writer used by HIDProvider.
type Device interface {
	Write(data []byte) error
	Close()
}

func runKeyboardGate(gate ProfileGate, ctx context.Context, isolate bool, fn func() error) error {
	if gate == nil {
		return fn()
	}
	return gate.WithKeyboard(ctx, isolate, fn)
}

func runExtraKeysGate(gate ProfileGate, ctx context.Context, fn func() error) error {
	if gate == nil {
		return fn()
	}
	return gate.WithExtraKeys(ctx, fn)
}

func runPointerGate(gate ProfileGate, ctx context.Context, fn func() error) error {
	if gate == nil {
		return fn()
	}
	return gate.WithPointer(ctx, fn)
}
