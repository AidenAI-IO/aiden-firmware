package model

// ModelSpec describes static capabilities of a chat completion model that the
// runtime needs at decision time (e.g. when to compress session history).
//
// ContextWindow is the model's total context window in tokens (input + output).
// MaxOutput is the per-request output cap the model advertises; 0 means unknown.
//
// DefaultTemperature, when non-nil, is the sampling temperature applied when the
// user has not set model.temperature explicitly. Some models (e.g. Kimi K3) only
// accept a single fixed temperature, so a global default would be rejected. A
// nil value means the model imposes no requirement and the global fallback
// applies. It is only a default: an explicit model.temperature always wins.
type ModelSpec struct {
	ContextWindow      int
	MaxOutput          int
	DefaultTemperature *float64
}
