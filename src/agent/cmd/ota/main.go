package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aiden-agent/internal/ota"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	command, args := splitCommandAndFlags(args)
	positional := flagArgs(args)
	if (command == "health" || command == "update" || command == "check-now" || command == "status") && len(positional) != 0 {
		return usage()
	}
	config, err := parseConfigFlags(args)
	if err != nil {
		return err
	}
	updater, err := ota.NewUpdater(config, rebootForConfig(config))
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch command {
	case "health":
		return updater.ProcessPendingHealthOnce(ctx)
	case "update", "check-now":
		result, err := updater.CheckOnce(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(result)
	case "status":
		state, ab, err := updater.Status()
		if err != nil {
			return err
		}
		active, activeOK := ab.ActiveSlot()
		return json.NewEncoder(out).Encode(struct {
			State    ota.State  `json:"state"`
			ABData   ota.ABData `json:"ab_data"`
			Active   ota.Slot   `json:"active_slot"`
			ActiveOK bool       `json:"active_slot_ok"`
		}{State: state, ABData: ab, Active: active, ActiveOK: activeOK})
	case "verify-manifest":
		if len(positional) != 1 {
			return usage()
		}
		manifestPath := positional[0]
		manifest, err := updater.VerifyManifestFile(manifestPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "manifest ok: version=%s channel=%s build_time=%s\n", manifest.Version, manifest.Channel, manifest.BuildTime)
		return nil
	default:
		return usage()
	}
}

func rebootForConfig(config ota.UpdaterConfig) func() error {
	if config.DryRun {
		return func() error { return nil }
	}
	return platformReboot
}

func platformReboot() error {
	for _, path := range []string{"/sbin/reboot", "/usr/sbin/reboot"} {
		if _, err := os.Stat(path); err == nil {
			return exec.Command(path).Run()
		}
	}
	return fmt.Errorf("reboot binary not found in trusted paths")
}

func splitCommandAndFlags(args []string) (string, []string) {
	commands := map[string]bool{
		"health":          true,
		"update":          true,
		"check-now":       true,
		"status":          true,
		"verify-manifest": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) >= 2 && arg[0] == '-' {
			name := flagName(arg)
			if flagTakesValue(name) && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if commands[arg] {
			rest := make([]string, 0, len(args)-1)
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+1:]...)
			return arg, rest
		}
		return "health", args
	}
	return "health", args
}

func parseConfigFlags(args []string) (ota.UpdaterConfig, error) {
	fs := flag.NewFlagSet("ota", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", ota.DefaultOTAConfigPath, "config JSON path")
	stateDir := fs.String("state-dir", "", "OTA state directory")
	miscPath := fs.String("misc", "", "misc partition path")
	blockDir := fs.String("block-dir", "", "block device by-name directory")
	manifestURL := fs.String("manifest-url", "", "direct manifest URL (skips release API)")
	publicKeyPath := fs.String("public-key", "", "Ed25519 public key PEM path")
	dryRun := fs.Bool("dry-run", false, "download and verify without switching misc or rebooting")
	testMode := fs.Bool("test", false, "use test-friendly short health timeout")
	healthTimeout := fs.Duration("health-timeout", 0, "pending health timeout")
	httpTimeout := fs.Duration("http-timeout", 0, "HTTP request timeout")
	switchTries := fs.Uint("switch-tries", 0, "tries remaining when switching slots")
	targetSlot := fs.String("target-slot", "", "test override for target slot: a or b")
	flags, _ := partitionFlagArgs(args)
	if err := fs.Parse(flags); err != nil {
		return ota.UpdaterConfig{}, err
	}
	config, err := ota.LoadUpdaterConfig(*configPath)
	if err != nil {
		return ota.UpdaterConfig{}, err
	}
	if *stateDir != "" {
		config.StateDir = *stateDir
		config.DownloadDir = ""
		config.UpdateLockPath = ""
	}
	if *miscPath != "" {
		config.MiscPath = *miscPath
	}
	if *blockDir != "" {
		config.BlockDir = *blockDir
	}
	if *manifestURL != "" {
		config.ManifestURL = *manifestURL
	}
	if *publicKeyPath != "" {
		config.PublicKeyPath = *publicKeyPath
	}
	if *healthTimeout != 0 {
		config.HealthTimeout = *healthTimeout
	}
	if *httpTimeout != 0 {
		config.HTTPTimeout = *httpTimeout
	}
	if *switchTries != 0 {
		if *switchTries > uint(ota.MaxTries) {
			return ota.UpdaterConfig{}, fmt.Errorf("switch-tries %d exceeds %d", *switchTries, ota.MaxTries)
		}
		config.SwitchTries = uint8(*switchTries)
	}
	if *targetSlot != "" {
		config.TargetSlotOverride = *targetSlot
	}
	config.DryRun = config.DryRun || *dryRun
	if *testMode {
		config.HealthTimeout = time.Second
	}
	// With the SD card mounted, cache downloads there instead of eMMC. An
	// explicit download_dir in the config JSON or a -state-dir sandbox
	// (tests, acceptance runs) keeps its own layout.
	if !config.DownloadDirConfigured && *stateDir == "" {
		emmcDownloadDir := config.DownloadDir
		state := readSDStorageState(storageStatePath)
		if dir := sdOTACacheDir(storageStatePath, config.ReserveSizeBytes); dir != "" {
			config.DownloadDir = dir
			config.AlternateDownloadDirs = []string{emmcDownloadDir}
		} else if state.mounted && state.mountPoint != "" {
			config.AlternateDownloadDirs = []string{filepath.Join(state.mountPoint, "aiden", "ota-cache")}
		}
	}
	config.Logger = log.New(os.Stderr, "ota: ", log.LstdFlags)
	return config, nil
}

func flagArgs(args []string) []string {
	_, positional := partitionFlagArgs(args)
	return positional
}

func partitionFlagArgs(args []string) ([]string, []string) {
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		name := flagName(arg)
		if flagIsBool(name) || strings.Contains(arg, "=") {
			flags = append(flags, arg)
			continue
		}
		if flagTakesValue(name) && i+1 < len(args) {
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		flags = append(flags, arg)
	}
	return flags, positional
}

func flagName(arg string) string {
	name := arg
	for len(name) > 0 && name[0] == '-' {
		name = name[1:]
	}
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	return name
}

func flagIsBool(name string) bool {
	return name == "dry-run" || name == "test"
}

func flagTakesValue(name string) bool {
	switch name {
	case "config", "state-dir", "misc", "block-dir", "manifest-url", "public-key", "health-timeout", "target-slot", "http-timeout", "switch-tries":
		return true
	default:
		return false
	}
}

func usage() error {
	return fmt.Errorf("usage: ota [flags] [health|update|check-now|status|verify-manifest <manifest>]")
}
