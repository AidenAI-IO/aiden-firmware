package ota

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type SelfCheckConfig struct {
	RequiredWiFi        bool
	RequiredPhoneBridge bool
	RequiredHDMI        bool
	RequiredUDC         bool
	CommandTimeout      time.Duration
	FrameCLI            string
	AudioCLI            string
	Curl                string
}

type SelfCheckItem struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
type SelfCheckReport struct {
	StartedAt  time.Time                `json:"started_at"`
	FinishedAt time.Time                `json:"finished_at"`
	Items      map[string]SelfCheckItem `json:"items"`
	Passed     int                      `json:"passed"`
	Warnings   int                      `json:"warnings"`
	Failures   int                      `json:"failures"`
}

func (r SelfCheckReport) Fatal() bool { return r.Failures > 0 }
func DefaultSelfCheckConfig() SelfCheckConfig {
	return SelfCheckConfig{CommandTimeout: 4 * time.Second}
}

// RunSelfCheck only performs read-only probes. External conditions such as a
// missing HDMI source, phone, Wi-Fi AP, or USB host are warnings by default.
func RunSelfCheck(ctx context.Context, cfg SelfCheckConfig) SelfCheckReport {
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 4 * time.Second
	}
	r := SelfCheckReport{StartedAt: time.Now().UTC(), Items: map[string]SelfCheckItem{}}
	add := func(name, status, detail string) {
		r.Items[name] = SelfCheckItem{status, detail}
		switch status {
		case "pass":
			r.Passed++
		case "warn":
			r.Warnings++
		default:
			r.Failures++
		}
	}
	pathCheck := func(name, path string, required bool) {
		if _, e := os.Stat(path); e == nil {
			add(name, "pass", path)
		} else if required {
			add(name, "fail", e.Error())
		} else {
			add(name, "warn", "not present")
		}
	}
	pathCheck("ota_storage", "/userdata/ota", true)
	pathCheck("agent_config", "/userdata/agent/agent.toml", true)
	pathCheck("hid_keyboard", "/dev/hidg0", true)
	pathCheck("hid_pointer", "/dev/hidg1", true)
	pathCheck("hid_consumer", "/dev/hidg2", false)
	command := func(name string, required bool, bin string, args ...string) {
		c, cancel := context.WithTimeout(ctx, cfg.CommandTimeout)
		defer cancel()
		out, e := exec.CommandContext(c, bin, args...).CombinedOutput()
		if e == nil {
			add(name, "pass", strings.TrimSpace(string(out)))
			return
		}
		st := "warn"
		if required {
			st = "fail"
		}
		d := strings.TrimSpace(string(out))
		if d == "" {
			d = e.Error()
		}
		add(name, st, d)
	}
	frameCLI := cfg.FrameCLI
	if frameCLI == "" {
		frameCLI = "/oem/usr/bin/frame_service_cli"
	}
	audioCLI := cfg.AudioCLI
	if audioCLI == "" {
		audioCLI = "/oem/usr/bin/audio_service_cli"
	}
	curlBin := cfg.Curl
	if curlBin == "" {
		curlBin = "curl"
	}
	command("frame_service", true, frameCLI, "--socket", "/run/frame_service/frame_service.sock", "health")
	command("audio_service", true, audioCLI, "--socket", "/run/audio_service/audio_service.sock", "health")
	command("agent_http", true, curlBin, "--fail", "--silent", "--max-time", "3", "http://127.0.0.1:8080/health")
	// Config Web is started after S54ota on the production image. It is
	// therefore diagnostic-only here; requiring it would make a healthy Agent
	// fail its own boot confirmation solely because rcS has not reached S56.
	command("config_web", false, "curl", "--fail", "--silent", "--max-time", "3", "http://127.0.0.1/api/status")
	command("ble_service", false, "/oem/usr/bin/ble_service_cli", "status")
	if _, e := os.Stat("/sys/class/net/usb0"); e == nil {
		command("usb_ecm", cfg.RequiredUDC, "sh", "-c", "ip addr show usb0 | grep -q '192.168.42.1'")
	} else if cfg.RequiredUDC {
		add("usb_ecm", "fail", "usb0 not enumerated")
	} else {
		add("usb_ecm", "warn", "usb0 not enumerated")
	}
	if cfg.RequiredWiFi {
		command("wifi_uplink", true, "sh", "-c", "ip route | grep -q default")
	} else {
		add("wifi_uplink", "warn", "optional; not required")
	}
	command("phone_bridge", cfg.RequiredPhoneBridge, curlBin, "--fail", "--silent", "--max-time", "3", "http://127.0.0.1:8080/api/phone-bridge/status")
	if cfg.RequiredHDMI {
		command("hdmi_signal", true, frameCLI, "--socket", "/run/frame_service/frame_service.sock", "latest-frame", "--out", "/tmp/ota-self-check-frame.raw")
	} else {
		add("hdmi_signal", "warn", "optional; HDMI source not required")
	}
	r.FinishedAt = time.Now().UTC()
	return r
}

func SaveSelfCheckReport(path string, report SelfCheckReport) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	b, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, append(b, '\n'), 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
