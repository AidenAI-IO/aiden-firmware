package model

import (
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type ModelResolver interface {
	Get() (llms.Model, error)
	CallOptions() []chains.ChainCallOption
	// Spec returns capabilities (context window, max output) for the configured
	// model. Implementations return a zero-value ModelSpec for unknown models;
	// callers must fall back to a configured default.
	Spec() ModelSpec
}
