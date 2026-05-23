package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/memory"
	"github.com/tmc/langchaingo/schema"
	langtools "github.com/tmc/langchaingo/tools"
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
	nameToTool := executorNameToTool(e.Agent.GetTools())
	steps := make([]schema.AgentStep, 0)

	for i := 0; i < e.MaxIterations; i++ {
		var finish map[string]any
		steps, finish, err = e.doIteration(ctx, steps, nameToTool, inputs, options...)
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
	nameToTool map[string]langtools.Tool,
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

	newSteps, err := e.doActions(ctx, nameToTool, actions)
	if err != nil {
		return steps, nil, err
	}
	steps = append(steps, newSteps...)
	return steps, nil, nil
}

func (e *parallelToolExecutor) doActions(ctx context.Context, nameToTool map[string]langtools.Tool, actions []schema.AgentAction) ([]schema.AgentStep, error) {
	for _, action := range actions {
		if e.CallbacksHandler != nil {
			e.CallbacksHandler.HandleAgentAction(ctx, action)
		}
	}
	if len(actions) == 1 {
		step, err := e.callTool(ctx, nameToTool, actions[0])
		if err != nil {
			return nil, err
		}
		return []schema.AgentStep{step}, nil
	}

	type actionResult struct {
		index int
		step  schema.AgentStep
		err   error
	}
	results := make(chan actionResult, len(actions))
	var wg sync.WaitGroup
	wg.Add(len(actions))
	for i, action := range actions {
		go func(index int, action schema.AgentAction) {
			defer wg.Done()
			step, err := e.callTool(ctx, nameToTool, action)
			results <- actionResult{index: index, step: step, err: err}
		}(i, action)
	}
	wg.Wait()
	close(results)

	steps := make([]schema.AgentStep, len(actions))
	for result := range results {
		if result.err != nil {
			return nil, result.err
		}
		steps[result.index] = result.step
	}
	return steps, nil
}

func (e *parallelToolExecutor) callTool(ctx context.Context, nameToTool map[string]langtools.Tool, action schema.AgentAction) (schema.AgentStep, error) {
	tool, ok := nameToTool[strings.ToUpper(action.Tool)]
	if !ok {
		return schema.AgentStep{
			Action:      action,
			Observation: fmt.Sprintf("%s is not a valid tool, try another one", action.Tool),
		}, nil
	}
	observation, err := tool.Call(ctx, strings.TrimSuffix(action.ToolInput, "\nObservation:"))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return schema.AgentStep{}, err
		}
		return schema.AgentStep{
			Action:      action,
			Observation: fmt.Sprintf("error: %s failed: %v", action.Tool, err),
		}, nil
	}
	return schema.AgentStep{Action: action, Observation: observation}, nil
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

func executorNameToTool(tools []langtools.Tool) map[string]langtools.Tool {
	if len(tools) == 0 {
		return nil
	}
	nameToTool := make(map[string]langtools.Tool, len(tools))
	for _, tool := range tools {
		nameToTool[strings.ToUpper(tool.Name())] = tool
	}
	return nameToTool
}
