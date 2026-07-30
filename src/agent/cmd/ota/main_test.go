package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aiden-agent/internal/ota"
)

func TestSplitCommandAndFlagsAllowsGlobalFlagsBeforeCommand(t *testing.T) {
	command, rest := splitCommandAndFlags([]string{"--config", "test.json", "verify-manifest", "manifest.json", "--state-dir", "state"})
	if command != "verify-manifest" {
		t.Fatalf("command = %q, want verify-manifest", command)
	}
	want := []string{"--config", "test.json", "manifest.json", "--state-dir", "state"}
	if len(rest) != len(want) {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Fatalf("rest = %#v, want %#v", rest, want)
		}
	}
}

func TestSplitCommandAndFlagsDefaultsToHealth(t *testing.T) {
	command, rest := splitCommandAndFlags([]string{"--config", "test.json"})
	if command != "health" {
		t.Fatalf("command = %q, want health", command)
	}
	if len(rest) != 2 || rest[0] != "--config" || rest[1] != "test.json" {
		t.Fatalf("rest = %#v", rest)
	}
}

func TestSplitCommandAndFlagsConsumesFlagValuesBeforeCommand(t *testing.T) {
	command, rest := splitCommandAndFlags([]string{"--manifest-url", "https://example.com/manifest.json", "update"})
	if command != "update" {
		t.Fatalf("command = %q, want update", command)
	}
	want := []string{"--manifest-url", "https://example.com/manifest.json"}
	if len(rest) != len(want) || rest[0] != want[0] || rest[1] != want[1] {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
	config, err := parseConfigFlags(rest)
	if err != nil {
		t.Fatalf("parseConfigFlags() error = %v", err)
	}
	if config.ManifestURL != "https://example.com/manifest.json" {
		t.Fatalf("manifest_url = %q, want https://example.com/manifest.json", config.ManifestURL)
	}
}

func TestParseConfigFlagsUsesSDCacheWhenDownloadDirIsNotConfigured(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	statePath := filepath.Join(tmp, "storage.state")
	if err := os.WriteFile(statePath, []byte("SD_MOUNTED=1\nSD_MOUNTPOINT=/mnt/sdcard\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(storage state) error = %v", err)
	}
	previousStatePath := storageStatePath
	storageStatePath = statePath
	t.Cleanup(func() { storageStatePath = previousStatePath })

	config, err := parseConfigFlags([]string{"-config", configPath})
	if err != nil {
		t.Fatalf("parseConfigFlags() error = %v", err)
	}
	want := filepath.Join("/mnt/sdcard", "aiden", "ota-cache")
	if config.DownloadDir != want {
		t.Fatalf("DownloadDir = %q, want %q", config.DownloadDir, want)
	}
}

func TestParseConfigFlagsPreservesExplicitDownloadDir(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"download_dir":"/custom/ota-cache"}`), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	statePath := filepath.Join(tmp, "storage.state")
	if err := os.WriteFile(statePath, []byte("SD_MOUNTED=1\nSD_MOUNTPOINT=/mnt/sdcard\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(storage state) error = %v", err)
	}
	previousStatePath := storageStatePath
	storageStatePath = statePath
	t.Cleanup(func() { storageStatePath = previousStatePath })

	config, err := parseConfigFlags([]string{"-config", configPath})
	if err != nil {
		t.Fatalf("parseConfigFlags() error = %v", err)
	}
	if config.DownloadDir != "/custom/ota-cache" {
		t.Fatalf("DownloadDir = %q, want explicit config path", config.DownloadDir)
	}
}

func TestSplitCommandAndFlagsKeepsCheckNowAlias(t *testing.T) {
	command, rest := splitCommandAndFlags([]string{"--manifest-url", "https://example.com/manifest.json", "check-now"})
	if command != "check-now" {
		t.Fatalf("command = %q, want check-now", command)
	}
	want := []string{"--manifest-url", "https://example.com/manifest.json"}
	if len(rest) != len(want) || rest[0] != want[0] || rest[1] != want[1] {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
}

func TestSplitCommandAndFlagsSupportsHealthCommand(t *testing.T) {
	command, rest := splitCommandAndFlags([]string{"--state-dir", "state", "health"})
	if command != "health" {
		t.Fatalf("command = %q, want health", command)
	}
	want := []string{"--state-dir", "state"}
	if len(rest) != len(want) || rest[0] != want[0] || rest[1] != want[1] {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
}

func TestRunRejectsExtraPositionalsForNonManifestCommands(t *testing.T) {
	for _, args := range [][]string{
		{"health", "extra"},
		{"update", "extra", "--dry-run"},
		{"check-now", "extra", "--dry-run"},
		{"status", "extra"},
	} {
		err := run(args, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("run(%#v) error = %v, want usage", args, err)
		}
	}
}

func TestDefaultRebootIsRealUnlessDryRun(t *testing.T) {
	reboot := rebootForConfig(ota.UpdaterConfig{})
	if reboot == nil {
		t.Fatalf("rebootForConfig(default) = nil, want real reboot function")
	}
	dryRunReboot := rebootForConfig(ota.UpdaterConfig{DryRun: true})
	if dryRunReboot == nil {
		t.Fatalf("rebootForConfig(dry-run) = nil")
	}
	if err := dryRunReboot(); err != nil {
		t.Fatalf("dry-run reboot error = %v", err)
	}
}

func TestUpdateRunsManualCheckWhenNoUpdate(t *testing.T) {
	fixture := newNoUpdateFixture(t)

	var out bytes.Buffer
	err := run([]string{
		"update",
		"--state-dir", fixture.stateDir,
		"--misc", fixture.miscPath,
		"--manifest-url", fixture.manifestURL,
		"--public-key", fixture.keyPath,
	}, &out)
	if err != nil {
		t.Fatalf("run(update) error = %v", err)
	}
	if !strings.Contains(out.String(), `"NoUpdate":true`) {
		t.Fatalf("output = %q, want no update", out.String())
	}
}

func TestUpdateReturnsManualCheckFailure(t *testing.T) {
	fixture := newNoUpdateFixture(t)
	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a manifest"))
	}))
	t.Cleanup(badServer.Close)

	err := run([]string{
		"update",
		"--state-dir", fixture.stateDir,
		"--misc", fixture.miscPath,
		"--manifest-url", badServer.URL + "/manifest.json",
		"--public-key", fixture.keyPath,
	}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("run(update) error = nil, want manifest failure")
	}
}

type noUpdateFixture struct {
	stateDir    string
	miscPath    string
	keyPath     string
	manifestURL string
}

func newNoUpdateFixture(t *testing.T) noUpdateFixture {
	t.Helper()

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	miscPath := filepath.Join(tmp, "misc.img")
	keyPath := filepath.Join(tmp, "ota_pubkey.pem")
	version := "20260521-120000-abcdef0"
	buildTime := "2026-05-21T12:00:00Z"

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatalf("WriteFile(pubkey) error = %v", err)
	}
	if err := ota.CreateMiscImage(miscPath, ota.DefaultMiscSize); err != nil {
		t.Fatalf("CreateMiscImage() error = %v", err)
	}
	state := ota.State{
		LastCommittedVersion:   version,
		LastCommittedBuildTime: buildTime,
		Slots:                  map[string]ota.SlotPartitionInfo{},
	}
	if err := ota.SaveState(filepath.Join(stateDir, "state.json"), state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	manifest := ota.Manifest{
		SchemaVersion: 1,
		Channel:       "stable",
		Version:       version,
		BuildTime:     buildTime,
		Parts: []ota.ManifestPart{{
			Name:   "boot",
			AssetA: &ota.ManifestAsset{Name: "boot_a.img", Size: 1, SHA256: strings.Repeat("a", 64)},
			AssetB: &ota.ManifestAsset{Name: "boot_b.img", Size: 1, SHA256: strings.Repeat("b", 64)},
		}},
		Signature: ota.ManifestSignature{Algorithm: "ed25519"},
	}
	canonical, err := ota.CanonicalManifestJSON(manifest)
	if err != nil {
		t.Fatalf("CanonicalManifestJSON() error = %v", err)
	}
	manifest.Signature.Value = hex.EncodeToString(ed25519.Sign(priv, canonical))
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		_, _ = w.Write(manifestBytes)
	}))
	t.Cleanup(server.Close)

	return noUpdateFixture{
		stateDir:    stateDir,
		miscPath:    miscPath,
		keyPath:     keyPath,
		manifestURL: server.URL + "/manifest.json",
	}
}

func TestParseConfigFlagsRejectsInvalidUpdaterConfig(t *testing.T) {
	err := run([]string{"status", "--config", filepath.Join(t.TempDir(), "missing.json"), "--switch-tries", "16"}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("run(status) error = nil, want validation error")
	}
}

func TestParseConfigFlagsRejectsSwitchTriesOverflow(t *testing.T) {
	_, err := parseConfigFlags([]string{"--config", filepath.Join(t.TempDir(), "missing.json"), "--switch-tries", "256"})
	if err == nil || !strings.Contains(err.Error(), "switch-tries") {
		t.Fatalf("parseConfigFlags() error = %v, want switch-tries rejection", err)
	}
}

func TestVerifyManifestSupportsRemoteURL(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	manifest := ota.Manifest{
		SchemaVersion: 1,
		Channel:       "stable",
		Version:       "20260521-120000-abcdef0",
		BuildTime:     "2026-05-21T12:00:00Z",
		Parts: []ota.ManifestPart{{
			Name:   "boot",
			AssetA: &ota.ManifestAsset{Name: "boot_a.img", Size: 1, SHA256: strings.Repeat("a", 64)},
			AssetB: &ota.ManifestAsset{Name: "boot_b.img", Size: 1, SHA256: strings.Repeat("b", 64)},
		}},
		Signature: ota.ManifestSignature{Algorithm: "ed25519"},
	}
	canonical, err := ota.CanonicalManifestJSON(manifest)
	if err != nil {
		t.Fatalf("CanonicalManifestJSON() error = %v", err)
	}
	manifest.Signature.Value = hex.EncodeToString(ed25519.Sign(priv, canonical))
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(manifestBytes)
	}))
	t.Cleanup(server.Close)
	keyPath := filepath.Join(t.TempDir(), "ota_pubkey.pem")
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatalf("WriteFile(pubkey) error = %v", err)
	}

	var out bytes.Buffer
	if err := run([]string{"verify-manifest", server.URL, "--public-key", keyPath}, &out); err != nil {
		t.Fatalf("run(verify-manifest URL) error = %v", err)
	}
	if !strings.Contains(out.String(), "manifest ok") {
		t.Fatalf("output = %q, want manifest ok", out.String())
	}
}
