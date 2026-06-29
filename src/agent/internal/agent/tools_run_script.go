package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	langtools "github.com/tmc/langchaingo/tools"
)

const (
	maxRunScriptLineBytes = 1 << 20
	maxRunScriptSteps     = 1000
	runScriptOutputLimit  = 2000
	maxRunScriptWait      = 30 * time.Second
)

type runScriptSpeaker func(context.Context, string) error
type runScriptToolLookup func(string) (langtools.Tool, bool)
type runScriptSleeper func(context.Context, time.Duration) error
type runScriptSpeakerContextKey struct{}

func contextWithRunScriptSpeaker(ctx context.Context, speaker runScriptSpeaker) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if speaker == nil {
		return ctx
	}
	return context.WithValue(ctx, runScriptSpeakerContextKey{}, speaker)
}

func runScriptSpeakerFromContext(ctx context.Context) runScriptSpeaker {
	if ctx == nil {
		return nil
	}
	speaker, _ := ctx.Value(runScriptSpeakerContextKey{}).(runScriptSpeaker)
	return speaker
}

// RunScriptTool executes local JSONL demo scripts without involving the LLM
// between scripted steps.
type RunScriptTool struct {
	lookup     runScriptToolLookup
	speaker    runScriptSpeaker
	sleeper    runScriptSleeper
	readFile   func(string) ([]byte, error)
	scriptsDir string
	mu         sync.RWMutex
}

func NewRunScriptTool(scriptsDir string, lookup runScriptToolLookup) *RunScriptTool {
	return &RunScriptTool{
		lookup:     lookup,
		sleeper:    runScriptSleepContext,
		readFile:   os.ReadFile,
		scriptsDir: scriptsDir,
	}
}

func (t *RunScriptTool) Name() string { return "run_script" }

func (t *RunScriptTool) Description() string {
	return `Execute a local JSONL demo script without LLM involvement between steps. ` +
		`Input JSON: {"file":"demo.jsonl"}. The file name is resolved under the agent config directory's scripts/ folder; full paths and directory traversal are rejected. ` +
		`Each non-empty line is one JSON object: {"type":"wait","ms":500}, {"type":"tts","text":"..."}, or {"type":"call","tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}. Short forms such as {"wait":500}, {"tts":"..."}, and {"call":{"tool":"screenshot","input":{}}} are also accepted. ` +
		`The tts step starts speech playback asynchronously and immediately continues to the next line. The call step invokes an existing tool with the supplied input. Script execution stops on the first synchronous error. This tool is intended for controlled demo recording scripts.`
}

func (t *RunScriptTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"file": stringArgSchema("Script file name under the agent config directory's scripts/ folder, for example demo.jsonl. Do not pass a path."),
	}, "file")
}

func (t *RunScriptTool) SetSpeaker(speaker runScriptSpeaker) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.speaker = speaker
}

func (t *RunScriptTool) speakerFn(ctx context.Context) runScriptSpeaker {
	if speaker := runScriptSpeakerFromContext(ctx); speaker != nil {
		return speaker
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.speaker
}

func (t *RunScriptTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return fmt.Sprintf("error: invalid input: %v. Expected JSON: {\"file\":\"demo.jsonl\"}", err), nil
	}
	file := strings.TrimSpace(args.File)
	if file == "" {
		return "error: file is required", nil
	}
	path, err := t.resolveScriptPath(file)
	if err != nil {
		return fmt.Sprintf("error: %v", err), nil
	}

	readFile := t.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if err != nil {
		return fmt.Sprintf("error: read script %q: %v", file, err), nil
	}

	result := runScriptResult{
		OK:   true,
		File: file,
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxRunScriptLineBytes)

	for scanner.Scan() {
		lineNo := result.LinesRead + 1
		result.LinesRead = lineNo
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(result.Steps) >= maxRunScriptSteps {
			result.OK = false
			result.Error = fmt.Sprintf("script exceeds maximum step count %d", maxRunScriptSteps)
			break
		}

		step, err := parseRunScriptStep([]byte(line), lineNo)
		if err != nil {
			result.OK = false
			result.Error = err.Error()
			result.Steps = append(result.Steps, runScriptStepResult{
				Line:  lineNo,
				OK:    false,
				Error: err.Error(),
			})
			break
		}

		stepResult := t.executeStep(ctx, step)
		result.Steps = append(result.Steps, stepResult)
		if !stepResult.OK {
			result.OK = false
			result.Error = stepResult.Error
			break
		}
	}
	if err := scanner.Err(); err != nil && result.OK {
		result.OK = false
		result.Error = fmt.Sprintf("read script %q: %v", file, err)
	}
	result.StepsRun = len(result.Steps)

	out, _ := json.Marshal(result)
	return string(out), nil
}

func (t *RunScriptTool) resolveScriptPath(file string) (string, error) {
	if strings.TrimSpace(t.scriptsDir) == "" {
		return "", fmt.Errorf("scripts directory is not configured")
	}
	if file == "" || file == "." || file == ".." {
		return "", fmt.Errorf("invalid script file name %q", file)
	}
	if filepath.IsAbs(file) || strings.ContainsAny(file, `/\`) || strings.Contains(file, "..") || filepath.Base(file) != file {
		return "", fmt.Errorf("script file must be a file name under scripts/, got %q", file)
	}
	return filepath.Join(t.scriptsDir, file), nil
}

func (t *RunScriptTool) executeStep(ctx context.Context, step runScriptStep) (result runScriptStepResult) {
	start := time.Now()
	result = runScriptStepResult{
		Line: step.Line,
		Type: step.Type,
		Tool: step.Tool,
		OK:   true,
	}
	defer func() {
		result.DurationMs = time.Since(start).Milliseconds()
	}()

	switch step.Type {
	case "wait":
		if step.Wait <= 0 {
			result.OK = false
			result.Error = "wait duration must be greater than zero"
			return result
		}
		if step.Wait > maxRunScriptWait {
			result.OK = false
			result.Error = fmt.Sprintf("wait duration must be <= %s", maxRunScriptWait)
			return result
		}
		sleeper := t.sleeper
		if sleeper == nil {
			sleeper = runScriptSleepContext
		}
		if err := sleeper(ctx, step.Wait); err != nil {
			result.OK = false
			result.Error = err.Error()
			return result
		}
	case "tts":
		speaker := t.speakerFn(ctx)
		if speaker == nil {
			result.OK = false
			result.Error = "tts is not configured"
			return result
		}
		if strings.TrimSpace(step.Text) == "" {
			result.OK = false
			result.Error = "tts text is required"
			return result
		}
		go func(text string) {
			_ = speaker(context.Background(), text)
		}(step.Text)
		result.Text = step.Text
		result.Output = "queued"
	case "call":
		if t.lookup == nil {
			result.OK = false
			result.Error = "tool lookup is not configured"
			return result
		}
		if step.Tool == "" {
			result.OK = false
			result.Error = "call tool is required"
			return result
		}
		if step.Tool == t.Name() {
			result.OK = false
			result.Error = "run_script cannot call itself"
			return result
		}
		tool, ok := t.lookup(step.Tool)
		if !ok || tool == nil {
			result.OK = false
			result.Error = fmt.Sprintf("tool %q is not available", step.Tool)
			return result
		}
		toolCtx, _ := WithToolError(ctx)
		output, err := tool.Call(toolCtx, step.Input)
		if err != nil {
			result.OK = false
			result.Error = err.Error()
			return result
		}
		result.Output = runScriptOutputPreview(output)
		if toolErr := ToolErrorFromContext(toolCtx); toolErr != nil {
			result.OK = false
			result.Error = toolErr.Message
			if result.Output == "" {
				result.Output = toolErr.Message
			}
			return result
		}
		if legacyToolOutputLooksLikeError(output) {
			result.OK = false
			result.Error = output
			return result
		}
	default:
		result.OK = false
		result.Error = fmt.Sprintf("unsupported step type %q", step.Type)
	}
	return result
}

type runScriptResult struct {
	OK        bool                  `json:"ok"`
	File      string                `json:"file"`
	LinesRead int                   `json:"lines_read"`
	StepsRun  int                   `json:"steps_run"`
	Error     string                `json:"error,omitempty"`
	Steps     []runScriptStepResult `json:"steps"`
}

type runScriptStepResult struct {
	Line       int    `json:"line"`
	Type       string `json:"type,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Text       string `json:"text,omitempty"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

type runScriptStep struct {
	Line  int
	Type  string
	Wait  time.Duration
	Text  string
	Tool  string
	Input string
}

type runScriptRawStep struct {
	Type       string          `json:"type"`
	MS         int             `json:"ms"`
	WaitMS     int             `json:"wait_ms"`
	DurationMS int             `json:"duration_ms"`
	Seconds    float64         `json:"seconds"`
	Text       string          `json:"text"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	RawInput   *string         `json:"raw_input"`
}

func parseRunScriptStep(data []byte, line int) (runScriptStep, error) {
	var raw runScriptRawStep
	if err := json.Unmarshal(data, &raw); err != nil {
		return runScriptStep{}, fmt.Errorf("line %d: invalid JSONL instruction: %w", line, err)
	}
	raw.Type = strings.ToLower(strings.TrimSpace(raw.Type))
	if raw.Type == "" {
		nested, err := parseNestedRunScriptStep(data)
		if err != nil {
			return runScriptStep{}, fmt.Errorf("line %d: %w", line, err)
		}
		raw = nested
	}
	step := runScriptStep{
		Line: line,
		Type: raw.Type,
		Text: raw.Text,
		Tool: strings.TrimSpace(raw.Tool),
	}
	switch step.Type {
	case "wait":
		waitMs := raw.MS
		if waitMs == 0 {
			waitMs = raw.WaitMS
		}
		if waitMs == 0 {
			waitMs = raw.DurationMS
		}
		if waitMs == 0 && raw.Seconds > 0 {
			waitMs = int(raw.Seconds * 1000)
		}
		step.Wait = time.Duration(waitMs) * time.Millisecond
	case "tts":
		step.Text = strings.TrimSpace(raw.Text)
	case "call":
		input, err := runScriptCallInput(raw.Input, raw.RawInput)
		if err != nil {
			return runScriptStep{}, fmt.Errorf("line %d: %w", line, err)
		}
		step.Input = input
	default:
		return runScriptStep{}, fmt.Errorf("line %d: unsupported step type %q", line, raw.Type)
	}
	return step, nil
}

func parseNestedRunScriptStep(data []byte) (runScriptRawStep, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return runScriptRawStep{}, err
	}
	for _, key := range []string{"wait", "tts", "call"} {
		payload, ok := envelope[key]
		if !ok {
			continue
		}
		var raw runScriptRawStep
		trimmed := strings.TrimSpace(string(payload))
		if key == "wait" && trimmed != "" && trimmed != "null" && !strings.HasPrefix(trimmed, "{") {
			var ms int
			if err := json.Unmarshal(payload, &ms); err != nil {
				return runScriptRawStep{}, fmt.Errorf("wait payload is invalid: %w", err)
			}
			raw.MS = ms
		} else if key == "tts" && strings.HasPrefix(trimmed, `"`) {
			var text string
			if err := json.Unmarshal(payload, &text); err != nil {
				return runScriptRawStep{}, fmt.Errorf("tts payload is invalid: %w", err)
			}
			raw.Text = text
		} else if len(payload) > 0 && trimmed != "null" {
			if err := json.Unmarshal(payload, &raw); err != nil {
				return runScriptRawStep{}, fmt.Errorf("%s payload is invalid: %w", key, err)
			}
		}
		raw.Type = key
		return raw, nil
	}
	return runScriptRawStep{}, fmt.Errorf("missing type; expected wait, tts, or call")
}

func runScriptCallInput(input json.RawMessage, rawInput *string) (string, error) {
	if rawInput != nil {
		return *rawInput, nil
	}
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" {
		return "{}", nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(input, &text); err != nil {
			return "", fmt.Errorf("input string is invalid: %w", err)
		}
		return text, nil
	}
	return trimmed, nil
}

func runScriptOutputPreview(output string) string {
	output = strings.TrimSpace(stripScreenshotData(output))
	if len([]rune(output)) <= runScriptOutputLimit {
		return output
	}
	runes := []rune(output)
	return string(runes[:runScriptOutputLimit]) + "...(truncated)"
}

func runScriptSleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
