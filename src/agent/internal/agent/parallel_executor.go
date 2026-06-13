package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
)

type parallelToolExecutor struct {
	Agent            agents.Agent
	Memory           schema.Memory
	CallbacksHandler callbacks.Handler
	MaxIterations    int
}

var _ chains.Chain = (*parallelToolExecutor)(nil)
var _ callbacks.HandlerHaver = (*parallelToolExecutor)(nil)

func newParallelToolExecutor(agent agents.Agent, mem schema.Memory, maxIterations int, handler callbacks.Handler) *parallelToolExecutor {
	if mem == nil {
		mem = memory.NewSimple()
	}
	return &parallelToolExecutor{
		Agent:            agent,
		Memory:           mem,
		MaxIterations:    maxIterations,
		CallbacksHandler: handler,
	}
}

func (e *parallelToolExecutor) Call(ctx context.Context, inputValues map[string]any, options ...chains.ChainCallOption) (map[string]any, error) {
	inputs, err := executorInputsToString(inputValues)
	if err != nil {
		return nil, err
	}
	toolSpecs := NewToolSpecs(e.Agent.GetTools())
	steps := make([]schema.AgentStep, 0)

	for i := 0; i < e.MaxIterations; i++ {
		var finish map[string]any
		steps, finish, err = e.doIteration(ctx, steps, toolSpecs, inputs, options...)
		if finish != nil || err != nil {
			return finish, err
		}
	}

	if e.CallbacksHandler != nil {
		e.CallbacksHandler.HandleAgentFinish(ctx, schema.AgentFinish{
			ReturnValues: map[string]any{"output": agents.ErrNotFinished.Error()},
		})
	}
	return map[string]any{"output": ""}, agents.ErrNotFinished
}

func (e *parallelToolExecutor) doIteration(
	ctx context.Context,
	steps []schema.AgentStep,
	toolSpecs *ToolSpecs,
	inputs map[string]string,
	options ...chains.ChainCallOption,
) ([]schema.AgentStep, map[string]any, error) {
	actions, finish, err := e.Agent.Plan(ctx, steps, inputs, options...)
	if errors.Is(err, agents.ErrUnableToParseOutput) {
		steps = append(steps, schema.AgentStep{Observation: err.Error()})
		return steps, nil, nil
	}
	if err != nil {
		return steps, nil, err
	}
	if len(actions) == 0 && finish == nil {
		return steps, nil, agents.ErrAgentNoReturn
	}
	if finish != nil {
		if e.CallbacksHandler != nil {
			e.CallbacksHandler.HandleAgentFinish(ctx, *finish)
		}
		return steps, finish.ReturnValues, nil
	}

	newSteps, err := e.doActions(ctx, toolSpecs, actions)
	if err != nil {
		return steps, nil, err
	}
	steps = append(steps, newSteps...)
	return steps, nil, nil
}

func (e *parallelToolExecutor) doActions(ctx context.Context, toolSpecs *ToolSpecs, actions []schema.AgentAction) ([]schema.AgentStep, error) {
	action := actions[0]
	execution := executeToolCall(ctx, ToolCallExecution{
		Specs:    toolSpecs,
		Action:   action,
		Callback: e.CallbacksHandler,
	})
	if execution.Error != nil {
		return nil, execution.Error
	}
	return []schema.AgentStep{execution.Step}, nil
}

func (e *parallelToolExecutor) GetInputKeys() []string {
	return e.Agent.GetInputKeys()
}

func (e *parallelToolExecutor) GetOutputKeys() []string {
	return e.Agent.GetOutputKeys()
}

func (e *parallelToolExecutor) GetMemory() schema.Memory {
	return e.Memory
}

func (e *parallelToolExecutor) GetCallbackHandler() callbacks.Handler {
	return e.CallbacksHandler
}

func executorInputsToString(inputValues map[string]any) (map[string]string, error) {
	inputs := make(map[string]string, len(inputValues))
	for key, value := range inputValues {
		valueStr, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %s", agents.ErrExecutorInputNotString, key)
		}
		inputs[key] = valueStr
	}
	return inputs, nil
}
