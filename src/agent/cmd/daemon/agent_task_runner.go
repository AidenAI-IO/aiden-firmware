package main

import (
	"context"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/agenttask"
)

// runtimeAgentTaskRunner adapts the legacy agent Runtime to the narrow
// orchestration boundary owned by internal/agenttask.
type runtimeAgentTaskRunner struct {
	runtime *agent.Runtime
}

func (r runtimeAgentTaskRunner) Run(ctx context.Context, prompt string) (string, error) {
	var actionHandler agent.UserActionHandler
	if handler := agenttask.UserActionHandlerFromContext(ctx); handler != nil {
		actionHandler = func(_ context.Context, req agent.HumanHandoffRequest) error {
			handler(agenttask.UserAction{Reason: req.Reason, Details: req.Details, SuggestedAction: req.SuggestedAction})
			return nil
		}
	}
	result, err := r.runtime.Run(ctx, agent.RunRequest{
		Input:                   prompt,
		Turn:                    agent.NewTextTurnInput(prompt, nil),
		AsyncEpisodeMaintenance: true,
		UserActionHandler:       actionHandler,
	})
	return result.Output, err
}
