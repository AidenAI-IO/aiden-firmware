package model

import (
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type Model interface {
	llms.Model
	CallOptions() []chains.ChainCallOption
	Spec() ModelSpec
}
