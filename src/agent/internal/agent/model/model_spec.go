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
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
	// API is the provider API endpoint when the metadata source publishes one.
	// APIShape identifies the wire shape (for example "responses" or
	// "completions") when that distinction is available.
	API                    string   `json:"api,omitempty"`
	APIShape               string   `json:"api_shape,omitempty"`
	ContextWindow          int      `json:"context_window,omitempty"`
	MaxOutput              int      `json:"max_output,omitempty"`
	DefaultTemperature     *float64 `json:"default_temperature,omitempty"`
	DefaultReasoningEffort *string  `json:"default_reasoning_effort,omitempty"`
	// Thinking describes the model's native reasoning controls. A nil value
	// means the capability is unknown; Supported=false is an explicit
	// declaration that the model does not expose thinking.
	Thinking *ThinkingSpec `json:"thinking,omitempty"`
}

// ThinkingSpec is the provider/model-independent description of thinking
// controls. Efforts are the values accepted by an effort-style API. Older
// Claude models expose only a token budget, represented by BudgetTokensMin and
// BudgetTokensMax. CanDisable is true when the API has an explicit off switch
// or when omitting the thinking object disables it.
type ThinkingSpec struct {
	Supported bool `json:"supported"`
	// Mode identifies the primary control exposed by the model. "effort" is
	// an adaptive effort selector, "budget_tokens" is a numeric thinking
	// budget, and "toggle" is a boolean switch. A model may publish both
	// efforts and budget limits; in that case effort remains the preferred UI
	// control and the budget fields are available as an advanced override.
	Mode            string   `json:"mode,omitempty"`
	Efforts         []string `json:"efforts,omitempty"`
	DefaultEffort   string   `json:"default_effort,omitempty"`
	CanDisable      bool     `json:"can_disable"`
	BudgetTokensMin int      `json:"budget_tokens_min,omitempty"`
	BudgetTokensMax int      `json:"budget_tokens_max,omitempty"`
}
