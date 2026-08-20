package main

import (
	"context"

	"aiden-agent/internal/agent"
)

// runtimeAgentTaskRunner adapts the legacy agent Runtime to the narrow
// orchestration boundary owned by internal/agenttask.
type runtimeAgentTaskRunner struct {
	runtime *agent.Runtime
}

func (r runtimeAgentTaskRunner) Run(ctx context.Context, prompt string) (string, error) {
	result, err := r.runtime.Run(ctx, agent.RunRequest{
		Input:                   prompt,
		Turn:                    agent.NewTextTurnInput(prompt, nil),
		AsyncEpisodeMaintenance: true,
	})
	return result.Output, err
}
