package main

import (
	"flag"
	"fmt"
	"os"

	"aiden-agent/internal/agent"
)

func main() {
	var (
		configDir = flag.String("config", "", "path to config directory (required)")
		addr      = flag.String("addr", ":8080", "HTTP server address")
	)
	flag.Parse()

	if *configDir == "" {
		fmt.Fprintln(os.Stderr, "missing -config flag: must specify config directory")
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

	server := agent.NewServer(runtime, *addr)

	fmt.Printf("🚀 Aiden Agent daemon starting on %s\n", *addr)
	fmt.Printf("📂 Config directory: %s\n", *configDir)
	fmt.Printf("🌐 Web UI: http://localhost%s\n", *addr)
	fmt.Printf("📝 Logs: %s/log/\n", *configDir)

	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
