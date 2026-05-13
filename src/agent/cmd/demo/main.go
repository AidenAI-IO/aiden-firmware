package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"aiden-agent/internal/agent"
)

func main() {
	var (
		configDir  = flag.String("config", "", "path to config directory (required)")
		agentName  = flag.String("agent", "", "agent name, defaults to config.default_agent")
		skillCSV   = flag.String("skills", "", "comma separated skills")
		input      = flag.String("input", "", "input text")
		showMemory = flag.Bool("show-memory", false, "print memory snapshot after the run")
		clearFirst = flag.Bool("clear-memory", false, "clear agent memory before the run")
	)
	flag.Parse()

	if *configDir == "" {
		fmt.Fprintln(os.Stderr, "missing -config flag: must specify config directory")
		os.Exit(1)
	}

	requestText := strings.TrimSpace(*input)
	if requestText == "" && len(flag.Args()) > 0 {
		requestText = strings.Join(flag.Args(), " ")
	}
	if requestText == "" {
		fmt.Fprintln(os.Stderr, "missing input, use -input or trailing arguments")
		os.Exit(1)
	}

	cfg, err := agent.LoadConfigFromDir(*configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	runtime, err := agent.NewRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create runtime: %v\n", err)
		os.Exit(1)
	}
	defer runtime.Close()

	ctx := context.Background()

	targetAgent := *agentName
	if targetAgent == "" {
		targetAgent = cfg.DefaultAgent
	}

	if *clearFirst {
		if err := runtime.ClearMemory(ctx, targetAgent); err != nil {
			fmt.Fprintf(os.Stderr, "clear memory: %v\n", err)
			os.Exit(1)
		}
	}

	result, err := runtime.Run(ctx, agent.RunRequest{
		AgentName:    *agentName,
		Input:        requestText,
		Skills:       splitCSV(*skillCSV),
		StreamWriter: os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "run agent: %v\n", err)
		os.Exit(1)
	}

	// Print metrics after response
	if result.Metrics != nil {
		fmt.Fprintf(os.Stderr, "\n\n")
		fmt.Fprintf(os.Stderr, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Fprintf(os.Stderr, "📊 Response Metrics\n")
		fmt.Fprintf(os.Stderr, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		fmt.Fprintf(os.Stderr, "⏱️  Total Duration:    %.2f ms (%.2f s)\n", result.Metrics.TotalDuration, result.Metrics.TotalDuration/1000)
		if result.Metrics.FirstTokenTime > 0 {
			fmt.Fprintf(os.Stderr, "⚡ First Token Time:   %.2f ms\n", result.Metrics.FirstTokenTime)
		}
		if result.Metrics.TotalTokens > 0 {
			fmt.Fprintf(os.Stderr, "🔤 Prompt Tokens:      %d\n", result.Metrics.PromptTokens)
			fmt.Fprintf(os.Stderr, "🔤 Completion Tokens:  %d\n", result.Metrics.CompletionTokens)
			fmt.Fprintf(os.Stderr, "🔤 Total Tokens:       %d\n", result.Metrics.TotalTokens)
		}
		if result.Metrics.TotalDuration > 0 && result.Metrics.CompletionTokens > 0 {
			tokensPerSecond := float64(result.Metrics.CompletionTokens) / (result.Metrics.TotalDuration / 1000)
			fmt.Fprintf(os.Stderr, "🚀 Speed:              %.2f tokens/s\n", tokensPerSecond)
		}
		fmt.Fprintf(os.Stderr, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *showMemory {
		data, err := json.MarshalIndent(result.Memory, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal memory: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
	}
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
