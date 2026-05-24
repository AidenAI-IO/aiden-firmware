package agent

import (
	"context"

	"github.com/tmc/langchaingo/callbacks"
	langtools "github.com/tmc/langchaingo/tools"
)

type namedToolCallbackHandler interface {
	HandleNamedToolStart(ctx context.Context, name, input string)
	HandleNamedToolEnd(ctx context.Context, name, input, output string)
	HandleNamedToolError(ctx context.Context, name, input string, err error)
}

type callbackTool struct {
	inner   langtools.Tool
	handler callbacks.Handler
}

func (t *callbackTool) Name() string {
	return t.inner.Name()
}

func (t *callbackTool) Description() string {
	return t.inner.Description()
}

func (t *callbackTool) Call(ctx context.Context, input string) (string, error) {
	if named, ok := t.handler.(namedToolCallbackHandler); ok {
		named.HandleNamedToolStart(ctx, t.Name(), input)
		output, err := t.inner.Call(ctx, input)
		if err != nil {
			named.HandleNamedToolError(ctx, t.Name(), input, err)
			return "", err
		}
		named.HandleNamedToolEnd(ctx, t.Name(), input, output)
		return output, nil
	}

	if t.handler != nil {
		t.handler.HandleToolStart(ctx, input)
	}

	output, err := t.inner.Call(ctx, input)
	if err != nil {
		if t.handler != nil {
			t.handler.HandleToolError(ctx, err)
		}
		return "", err
	}

	if t.handler != nil {
		t.handler.HandleToolEnd(ctx, output)
	}
	return output, nil
}

func (t *callbackTool) ReturnsVisualObservation() bool {
	if visual, ok := t.inner.(visualObservationTool); ok {
		return visual.ReturnsVisualObservation()
	}
	return false
}
