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
//
// DefaultReasoningEffort, when non-nil, is the reasoning_effort applied when the
// user has not set model.reasoning_effort explicitly (empty string). Some models
// (e.g. Kimi K3) are forced-reasoning and default to a heavy effort that adds
// several seconds of latency before any content streams; pinning a lighter
// default keeps voice interactions responsive. A nil value leaves reasoning in
// auto mode (field omitted from the request). It is only a default: an explicit
// model.reasoning_effort always wins.
type ModelSpec struct {
	Provider               string
	Name                   string
	ContextWindow          int
	MaxOutput              int
	DefaultTemperature     *float64
	DefaultReasoningEffort *string
}
