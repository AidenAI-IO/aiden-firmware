package configweb

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"aiden-agent/internal/wifiproxy"
)

const (
	defaultAgentConfigPath           = "/userdata/agent/agent.toml"
	defaultWiFiConfigPath            = "/userdata/wpa_supplicant.conf"
	defaultWiFiConfigEnvironmentPath = "/run/aiden/wpa_supplicant-config.env"
	defaultSystemEnvPath             = "/userdata/system/env"
	defaultWebRoot                   = "/oem/usr/share/aiden/config-web"
)

// Options contains the filesystem and process integration points used by the
// configuration portal. Paths are flags so host tests can run without device
// files, while production keeps the established locations.
type Options struct {
	BindAddress               string
	Port                      int
	AgentConfigPath           string
	WiFiConfigPath            string
	WiFiConfigEnvironmentPath string
	WiFiInterface             string
	WiFiBackend               string
	OTAStatePath              string
	CmdlinePath               string
	SystemEnvPath             string
	WiFiProxyConfigPath       string
	LocalProxyAddress         string
	LocalProxyEnvironmentPath string
	StorageStatePath          string
	WebRoot                   string

	AgentBinary            string
	AgentInitScript        string
	FrameServiceInitScript string
	WiFiProxyInitScript    string
	AgentHTTPBaseURL       string
	OTABinary              string
	EnvRunBinary           string
	OTAUpdateLockPath      string
	OTAUpdateLogPath       string
	OTAHealthLogPath       string
}

func DefaultOptions() Options {
	agentBinary := strings.TrimSpace(os.Getenv("AIDEN_AGENT_BIN"))
	if agentBinary == "" {
		if executable, err := os.Executable(); err == nil {
			agentBinary = executable
		} else {
			agentBinary = "/oem/usr/bin/agent"
		}
	}
	return Options{
		BindAddress:               "0.0.0.0",
		Port:                      80,
		AgentConfigPath:           defaultAgentConfigPath,
		WiFiConfigPath:            defaultWiFiConfigPath,
		WiFiConfigEnvironmentPath: defaultWiFiConfigEnvironmentPath,
		WiFiInterface:             "wlan0",
		WiFiBackend:               "legacy",
		OTAStatePath:              "/userdata/ota/state.json",
		CmdlinePath:               "/proc/cmdline",
		SystemEnvPath:             defaultSystemEnvPath,
		WiFiProxyConfigPath:       wifiproxy.DefaultConfigPath,
		LocalProxyAddress:         wifiproxy.DefaultListenAddress,
		LocalProxyEnvironmentPath: envOrDefault("AIDEN_WIFI_PROXY_ENVIRONMENT", wifiproxy.DefaultEnvironmentPath),
		StorageStatePath:          "/run/aiden/storage.state",
		WebRoot:                   defaultWebRoot,
		AgentBinary:               agentBinary,
		AgentInitScript:           envOrDefault("AIDEN_AGENT_INIT_SCRIPT", "/etc/init.d/S53agent"),
		FrameServiceInitScript:    envOrDefault("AIDEN_FRAME_SERVICE_INIT_SCRIPT", "/etc/init.d/S52frame_service"),
		WiFiProxyInitScript:       envOrDefault("AIDEN_WIFI_PROXY_INIT_SCRIPT", "/etc/init.d/S51wifi_proxy"),
		// Leave the Agent HTTP target empty by default so the portal follows the
		// address reported by S53agent. AIDEN_AGENT_HTTP_BASE_URL remains an
		// explicit override for development and tests.
		AgentHTTPBaseURL:  strings.TrimSpace(os.Getenv("AIDEN_AGENT_HTTP_BASE_URL")),
		OTABinary:         envOrDefault("AIDEN_OTA_BIN", "/oem/usr/bin/ota"),
		EnvRunBinary:      envOrDefault("AIDEN_ENV_RUN_BIN", "/oem/usr/bin/aiden-env-run"),
		OTAUpdateLockPath: envOrDefault("AIDEN_CONFIG_WEB_OTA_UPDATE_LOCK", "/tmp/config_web_ota_update.lock"),
		OTAUpdateLogPath:  envOrDefault("AIDEN_CONFIG_WEB_OTA_UPDATE_LOG", "/userdata/ota/config_web_ota_update.log"),
		OTAHealthLogPath:  envOrDefault("AIDEN_CONFIG_WEB_OTA_HEALTH_LOG", "/var/log/ota/ota.log"),
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func (o Options) Addr() string {
	return net.JoinHostPort(o.BindAddress, strconv.Itoa(o.Port))
}

func (o Options) Validate() error {
	switch o.BindAddress {
	case "0.0.0.0", "127.0.0.1", "192.168.42.1":
	default:
		return fmt.Errorf("unsupported bind address %q", o.BindAddress)
	}
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if o.WiFiBackend != "legacy" && o.WiFiBackend != "systemd-networkd" {
		return fmt.Errorf("unsupported Wi-Fi backend %q", o.WiFiBackend)
	}
	for name, value := range map[string]string{
		"config":                  o.AgentConfigPath,
		"wifi-config":             o.WiFiConfigPath,
		"wifi-config-environment": o.WiFiConfigEnvironmentPath,
		"wifi-interface":          o.WiFiInterface,
		"ota-state":               o.OTAStatePath,
		"cmdline":                 o.CmdlinePath,
		"system-env":              o.SystemEnvPath,
		"wifi-proxy-config":       o.WiFiProxyConfigPath,
		"local-proxy-environment": o.LocalProxyEnvironmentPath,
		"storage-state":           o.StorageStatePath,
		"web-root":                o.WebRoot,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--%s must not be empty", name)
		}
	}
	if o.LocalProxyAddress != "" {
		host, portText, err := net.SplitHostPort(o.LocalProxyAddress)
		if err != nil {
			return fmt.Errorf("invalid --local-proxy-address: %w", err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid --local-proxy-address port %q", portText)
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("--local-proxy-address must use a loopback host")
		}
	}
	return nil
}

func (o Options) ConfigDir() string {
	return filepath.Dir(o.AgentConfigPath)
}
