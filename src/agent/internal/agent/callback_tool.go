package agent

import (
	"context"

	"github.com/tmc/langchaingo/callbacks"
	langtools "github.com/tmc/langchaingo/tools"
)

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
