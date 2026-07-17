package model

// ModelSpec describes static capabilities of a chat completion model that the
// runtime needs at decision time (e.g. when to compress session history).
//
// ContextWindow is the model's total context window in tokens (input + output).
// MaxOutput is the per-request output cap the model advertises; 0 means unknown.
type ModelSpec struct {
	ContextWindow int
	MaxOutput     int
}
