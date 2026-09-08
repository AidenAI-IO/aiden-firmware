package wifiproxy

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

func Run(args []string) int {
	flags := flag.NewFlagSet("wifi-proxy", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	listenAddress := flags.String("listen", DefaultListenAddress, "loopback HTTP/SOCKS5 proxy listen address")
	configPath := flags.String("config", DefaultConfigPath, "per-Wi-Fi proxy configuration path")
	environmentPath := flags.String("environment", DefaultEnvironmentPath, "generated local proxy environment path")
	wifiInterface := flags.String("wifi-interface", "wlan0", "Wi-Fi interface used to resolve the active SSID")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", flags.Args())
		return 1
	}
	server, err := NewServer(*listenAddress, *configPath, *wifiInterface, UpstreamsFromEnvironment())
	if err != nil {
		fmt.Fprintf(os.Stderr, "wifi-proxy: %v\n", err)
		return 1
	}
	server.SetEnvironmentPath(*environmentPath)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "wifi-proxy: %v\n", err)
			return 1
		}
		return 0
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "wifi-proxy shutdown: %v\n", err)
			return 1
		}
		return 0
	}
}
