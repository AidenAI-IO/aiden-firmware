package configweb

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Run serves the device configuration portal until SIGINT or SIGTERM.
func Run(args []string) int {
	options := DefaultOptions()
	fs := flag.NewFlagSet("config-web", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&options.BindAddress, "bind", options.BindAddress, "HTTP bind address")
	fs.IntVar(&options.Port, "port", options.Port, "HTTP port")
	fs.StringVar(&options.AgentConfigPath, "config", options.AgentConfigPath, "agent TOML path")
	fs.StringVar(&options.WiFiConfigPath, "wifi-config", options.WiFiConfigPath, "wpa_supplicant config path")
	fs.StringVar(&options.WiFiInterface, "wifi-interface", options.WiFiInterface, "Wi-Fi interface")
	fs.StringVar(&options.OTAStatePath, "ota-state", options.OTAStatePath, "OTA state JSON path")
	fs.StringVar(&options.CmdlinePath, "cmdline", options.CmdlinePath, "kernel command line path")
	fs.StringVar(&options.SystemEnvPath, "system-env", options.SystemEnvPath, "system environment file path")
	fs.StringVar(&options.StorageStatePath, "storage-state", options.StorageStatePath, "storage state path")
	fs.StringVar(&options.WebRoot, "web-root", options.WebRoot, "config web static asset root")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", fs.Args())
		return 1
	}

	server, err := NewServer(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config-web: %v\n", err)
		return 1
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "config-web: %v\n", err)
			return 1
		}
		return 0
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "config-web shutdown: %v\n", err)
			return 1
		}
		return 0
	}
}
