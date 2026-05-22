package ota

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdaterHappyPathDownloadsWritesSwitchesAndReboots(t *testing.T) {
	env := newUpdaterTestEnv(t)
	manifest := env.signedManifest(map[string][]byte{
		"boot_a.img": []byte("boot-a-v2"),
		"boot_b.img": []byte("boot-b-v2"),
		"oem_a.img":  []byte("oem-a-v2"),
		"oem_b.img":  []byte("oem-b-v2"),
		"rootfs.img": []byte("rootfs-v2"),
	}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{
		"boot_b.img": []byte("boot-b-v2"),
		"oem_b.img":  []byte("oem-b-v2"),
		"rootfs.img": []byte("rootfs-v2"),
	})
	env.config.APIBase = server.URL

	result, err := env.updater().CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !result.Updated || result.TargetSlot != SlotB || result.Version != env.version {
		t.Fatalf("CheckOnce() = %+v", result)
	}
	if env.reboots != 1 {
		t.Fatalf("reboots = %d, want 1", env.reboots)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_b"), "boot-b-v2")
	assertFileContent(t, filepath.Join(env.blockDir, "oem_b"), "oem-b-v2")
	assertFileContent(t, filepath.Join(env.blockDir, "rootfs_b"), "rootfs-v2")
	assertFileContent(t, filepath.Join(env.blockDir, "boot_a"), "old-boot-a")

	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	active, ok := ab.ActiveSlot()
	if !ok || active != SlotB || ab.Slots[SlotB].TriesRemaining != env.config.SwitchTries {
		t.Fatalf("misc after update active=%v ok=%v slotB=%+v", active, ok, ab.Slots[SlotB])
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "health.ok")); !os.IsNotExist(err) {
		t.Fatalf("stale health marker still exists: %v", err)
	}
	pendingBytes, err := os.ReadFile(filepath.Join(env.stateDir, "pending_boot.json"))
	if err != nil {
		t.Fatalf("pending_boot.json missing: %v", err)
	}
	var pending PendingBoot
	if err := json.Unmarshal(pendingBytes, &pending); err != nil {
		t.Fatalf("pending boot JSON error = %v", err)
	}
	if pending.TargetSlot != "b" || pending.TargetVersion != env.version || pending.Nonce == "" {
		t.Fatalf("pending boot = %+v", pending)
	}
}

func TestUpdaterNoUpdateReturnsNoop(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.state.LastCommittedVersion = env.version
	env.state.LastCommittedBuildTime = env.buildTime
	env.saveState(t)
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	result, err := env.updater().CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !result.NoUpdate || result.Updated {
		t.Fatalf("CheckOnce() = %+v, want no update", result)
	}
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

func TestUpdaterInitializesMissingStateFromFactoryConfig(t *testing.T) {
	env := newUpdaterTestEnv(t)
	statePath := filepath.Join(env.stateDir, "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("Remove(state.json) error = %v", err)
	}
	env.config.FactoryVersion = "factory-1"
	env.config.FactoryBuildTime = "2026-05-21T10:00:00Z"
	env.config.FactoryPartitionHash = testHashA

	state, err := env.updater().loadState()
	if err != nil {
		t.Fatalf("loadState() error = %v", err)
	}
	if state.LastCommittedVersion != env.config.FactoryVersion || state.LastCommittedBuildTime != env.config.FactoryBuildTime {
		t.Fatalf("factory state version/build_time = %q/%q", state.LastCommittedVersion, state.LastCommittedBuildTime)
	}
	for _, slot := range []string{"a", "b"} {
		for _, part := range []string{"boot", "oem", "rootfs"} {
			got := state.Slots[slot].Partitions[part]
			if got.Version != env.config.FactoryVersion || got.Hash != env.config.FactoryPartitionHash {
				t.Fatalf("state.Slots[%s].Partitions[%s] = %+v", slot, part, got)
			}
		}
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("factory state was not persisted: %v", err)
	}
}

func TestUpdaterRejectsDowngradeOnFreshDeviceWithFactoryConfig(t *testing.T) {
	env := newUpdaterTestEnv(t)
	if err := os.Remove(filepath.Join(env.stateDir, "state.json")); err != nil {
		t.Fatalf("Remove(state.json) error = %v", err)
	}
	env.config.FactoryVersion = "factory-2"
	env.config.FactoryBuildTime = "2026-05-21T12:00:00Z"
	env.config.FactoryPartitionHash = testHashA
	manifest := env.signedManifest(map[string][]byte{
		"boot_a.img": []byte("boot-a-v1"),
		"boot_b.img": []byte("boot-b-v1"),
		"oem_a.img":  []byte("oem-a-v1"),
		"oem_b.img":  []byte("oem-b-v1"),
		"rootfs.img": []byte("rootfs-v1"),
	}, func(m *Manifest) {
		m.Version = "factory-1"
		m.BuildTime = "2026-05-21T11:00:00Z"
	})
	server := env.releaseServer(t, manifest, map[string][]byte{
		"boot_b.img": []byte("boot-b-v1"),
		"oem_b.img":  []byte("oem-b-v1"),
		"rootfs.img": []byte("rootfs-v1"),
	})
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reject downgrade") {
		t.Fatalf("CheckOnce() error = %v, want reject downgrade", err)
	}
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

func TestNewUpdaterRejectsInvalidConfig(t *testing.T) {
	updater, err := NewUpdater(UpdaterConfig{SwitchTries: MaxTries + 1}, nil)
	if err == nil {
		t.Fatalf("NewUpdater() error = nil, want switch_tries validation error")
	}
	if updater != nil {
		t.Fatalf("NewUpdater() updater = %#v, want nil", updater)
	}
}

func TestUpdaterRejectsOversizedRemoteManifest(t *testing.T) {
	env := newUpdaterTestEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxRemoteManifestBytes+1)))
	}))
	t.Cleanup(server.Close)

	_, err := env.updater().VerifyManifestFile(server.URL)
	if err == nil || !strings.Contains(err.Error(), "manifest too large") {
		t.Fatalf("VerifyManifestFile() error = %v, want manifest too large", err)
	}
}

func TestUpdaterReleaseURLUsesLatestForStableLatestAndEmptyChannel(t *testing.T) {
	for _, channel := range []string{"", "latest", "stable"} {
		t.Run(channel, func(t *testing.T) {
			updater, err := NewUpdater(UpdaterConfig{Repo: "owner/repo", Channel: channel, APIBase: "https://api.example.test/"}, nil)
			if err != nil {
				t.Fatalf("NewUpdater() error = %v", err)
			}
			got, err := updater.releaseURL()
			if err != nil {
				t.Fatalf("releaseURL() error = %v", err)
			}
			want := "https://api.example.test/repos/owner/repo/releases/latest"
			if got != want {
				t.Fatalf("releaseURL() = %q, want %q", got, want)
			}
		})
	}
}

func TestUpdaterReleaseURLUsesExplicitTagPrefixForTagLookup(t *testing.T) {
	updater, err := NewUpdater(UpdaterConfig{Repo: "owner/repo", Channel: "tag:v1.2.3", APIBase: "https://api.example.test"}, nil)
	if err != nil {
		t.Fatalf("NewUpdater() error = %v", err)
	}
	got, err := updater.releaseURL()
	if err != nil {
		t.Fatalf("releaseURL() error = %v", err)
	}
	want := "https://api.example.test/repos/owner/repo/releases/tags/v1.2.3"
	if got != want {
		t.Fatalf("releaseURL() = %q, want %q", got, want)
	}
}

func TestCurrentSlotFromCmdlineReadsAidenSlotSuffix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmdline string
		want    Slot
		wantOK  bool
		wantErr string
	}{
		{name: "slot a", cmdline: "console=ttyFIQ0 aiden.slot_suffix=_a root=/dev/mmcblk0", want: SlotA, wantOK: true},
		{name: "slot b", cmdline: "rootwait aiden.slot_suffix=_b", want: SlotB, wantOK: true},
		{name: "missing", cmdline: "console=ttyFIQ0", wantOK: false},
		{name: "invalid", cmdline: "aiden.slot_suffix=_c", wantErr: "invalid slot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := currentSlotFromCmdline(tc.cmdline)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("currentSlotFromCmdline() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("currentSlotFromCmdline() error = %v", err)
			}
			if ok != tc.wantOK || (ok && got != tc.want) {
				t.Fatalf("currentSlotFromCmdline() = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestUpdaterRejectsOversizedAssetWithDefaultPartitionSizes(t *testing.T) {
	env := newUpdaterTestEnv(t)
	bootBody := []byte("boot-b-v2")
	assets := map[string][]byte{
		"boot_a.img": []byte("boot-a-v2"),
		"boot_b.img": bootBody,
		"oem_a.img":  []byte("oem-a-v2"),
		"oem_b.img":  []byte("oem-b-v2"),
		"rootfs.img": []byte("rootfs-v2"),
	}
	manifest := env.signedManifest(assets, func(m *Manifest) {
		m.Parts[0].AssetB.Size = DefaultBootPartitionSize + 1
	})
	assetDownloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos/owner/repo/releases/latest") {
			release := struct {
				Assets []githubAsset `json:"assets"`
			}{Assets: []githubAsset{
				{Name: "manifest.json", BrowserDownloadURL: "http://" + r.Host + "/assets/manifest.json"},
				{Name: "boot_b.img", BrowserDownloadURL: "http://" + r.Host + "/assets/boot_b.img"},
			}}
			_ = json.NewEncoder(w).Encode(release)
			return
		}
		if r.URL.Path == "/assets/manifest.json" {
			_, _ = w.Write(manifest)
			return
		}
		if r.URL.Path == "/assets/boot_b.img" {
			assetDownloads++
			_, _ = w.Write(bootBody)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "larger than partition") {
		t.Fatalf("CheckOnce() error = %v, want default partition size rejection", err)
	}
	if assetDownloads != 0 {
		t.Fatalf("assetDownloads = %d, want 0", assetDownloads)
	}
	if _, err := os.Stat(filepath.Join(env.downloadDir, "boot_b.img")); !os.IsNotExist(err) {
		t.Fatalf("download file exists after pre-download rejection: %v", err)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_b"), "old-boot-b")
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

func TestUpdaterLatestRejectsNonStableManifestChannel(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.Channel = "latest"
	env.config.DryRun = true
	assets := map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}
	manifest := env.signedManifest(assets, func(m *Manifest) {
		m.Channel = "beta"
	})
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), `manifest channel "beta", want "stable"`) {
		t.Fatalf("CheckOnce() error = %v, want stable channel rejection", err)
	}
}

func TestUpdaterStableAcceptsStableManifestChannel(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.Channel = "stable"
	env.config.DryRun = true
	assets := map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}
	manifest := env.signedManifest(assets, func(m *Manifest) {
		m.Channel = "stable"
	})
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	result, err := env.updater().CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !result.Updated || result.TargetSlot != SlotB {
		t.Fatalf("CheckOnce() = %+v, want dry-run update to slot B", result)
	}
}

func TestUpdaterRejectsBadSignature(t *testing.T) {
	env := newUpdaterTestEnv(t)
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	manifest = []byte(strings.Replace(string(manifest), env.version, "20260521-130000-tamper", 1))
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("CheckOnce() error = %v, want signature failure", err)
	}
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

func TestUpdaterRejectsHashMismatchBeforeWriting(t *testing.T) {
	env := newUpdaterTestEnv(t)
	assets := map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}
	manifest := env.signedManifest(assets, func(m *Manifest) { m.Parts[0].AssetB.SHA256 = testHashA })
	server := env.releaseServer(t, manifest, assets)
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("CheckOnce() error = %v, want hash failure", err)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_b"), "old-boot-b")
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

func TestUpdaterUsesGitHubTokenForManifestAndImageDownloads(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.GitHubToken = "secret-token"
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.authReleaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, "Bearer secret-token")
	env.config.APIBase = server.URL

	result, err := env.updater().CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !result.Updated {
		t.Fatalf("CheckOnce() = %+v, want update", result)
	}
}

func TestUpdaterRejectsStaleTargetSlotForSelectiveUpdate(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.state.Slots["b"] = SlotPartitionInfo{Partitions: map[string]PartitionVersion{
		"boot":   {Version: "factory", Hash: testHashA},
		"oem":    {Version: "stale", Hash: testHashB},
		"rootfs": {Version: "factory", Hash: testHashA},
	}}
	env.saveState(t)
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2")}, func(m *Manifest) {
		m.Parts = []ManifestPart{m.Parts[0]}
		m.Parts[0].RequiresPartitions = []string{"oem=factory:" + testHashA, "rootfs=factory:" + testHashA}
	})
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2")})
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "selective update") {
		t.Fatalf("CheckOnce() error = %v, want selective update failure", err)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_b"), "old-boot-b")
}

func TestUpdaterInvalidatesTargetSlotMetadataBeforePartialWriteFailure(t *testing.T) {
	env := newUpdaterTestEnv(t)
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL
	if err := os.Remove(filepath.Join(env.blockDir, "oem_b")); err != nil {
		t.Fatalf("Remove(oem_b) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(env.blockDir, "oem_b"), 0o755); err != nil {
		t.Fatalf("Mkdir(oem_b) error = %v", err)
	}

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil {
		t.Fatalf("CheckOnce() error = nil, want write failure")
	}
	state, err := LoadState(filepath.Join(env.stateDir, "state.json"))
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	selective := env.manifestValue(map[string][]byte{"boot_a.img": []byte("boot-a-v3"), "boot_b.img": []byte("boot-b-v3")}, func(m *Manifest) {
		m.Version = "20260521-130000-abcdef1"
		m.BuildTime = "2026-05-21T13:00:00Z"
		m.Parts = []ManifestPart{m.Parts[0]}
		m.Parts[0].RequiresPartitions = []string{"oem=factory:" + testHashA, "rootfs=factory:" + testHashA}
	})
	if err := state.ValidateSelectiveUpdate(selective, SlotB); err == nil {
		t.Fatalf("ValidateSelectiveUpdate() error = nil, want invalidated target slot metadata")
	}
}

func TestUpdaterRejectsActiveSlotWrite(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.TargetSlotOverride = "a"
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "oem_a.img": []byte("oem-a-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "active slot") {
		t.Fatalf("CheckOnce() error = %v, want active slot write rejection", err)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_a"), "old-boot-a")
}

func TestUpdaterUsesRunningSlotToProtectWritesWhenMiscPrefersOtherSlot(t *testing.T) {
	env := newUpdaterTestEnv(t)
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if err := ab.SetActive(SlotB, 2, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := writeMiscFile(env.miscPath, ab); err != nil {
		t.Fatalf("writeMiscFile() error = %v", err)
	}
	env.config.TargetSlotOverride = "a"
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "oem_a.img": []byte("oem-a-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotA, true, nil }

	_, err = updater.CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "active slot") {
		t.Fatalf("CheckOnce() error = %v, want running active slot write rejection", err)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_a"), "old-boot-a")
}

func TestUpdaterDryRunDoesNotWritePartitionsOrSwitchMisc(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.config.DryRun = true
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	result, err := env.updater().CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !result.Updated || result.TargetSlot != SlotB {
		t.Fatalf("CheckOnce() = %+v", result)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_b"), "old-boot-b")
	assertFileContent(t, filepath.Join(env.blockDir, "oem_b"), "old-oem-b")
	assertFileContent(t, filepath.Join(env.blockDir, "rootfs_b"), "old-rootfs-b")
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	active, ok := ab.ActiveSlot()
	if !ok || active != SlotA {
		t.Fatalf("active slot after dry-run = %v ok=%v, want A", active, ok)
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); !os.IsNotExist(err) {
		t.Fatalf("pending_boot.json exists after dry-run: %v", err)
	}
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

func TestUpdaterRejectsSameBuildTimeDifferentVersion(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.state.LastCommittedVersion = "20260521-110000-older"
	env.state.LastCommittedBuildTime = env.buildTime
	env.saveState(t)
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL

	_, err := env.updater().CheckOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("CheckOnce() error = %v, want downgrade rejection", err)
	}
	assertFileContent(t, filepath.Join(env.blockDir, "boot_b"), "old-boot-b")
}

func TestUpdaterProcessesPendingHealthTimeoutAndReboots(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "nonce-1"}
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if err := ab.SetActive(SlotB, 2, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := writeMiscFile(env.miscPath, ab); err != nil {
		t.Fatalf("writeMiscFile() error = %v", err)
	}
	env.config.HealthTimeout = time.Millisecond
	env.config.HealthPollInterval = time.Millisecond

	err = env.updater().ProcessPendingHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "health timeout") {
		t.Fatalf("ProcessPendingHealth() error = %v, want timeout", err)
	}
	if env.reboots != 1 {
		t.Fatalf("reboots = %d, want 1", env.reboots)
	}
}

func TestUpdaterDoesNotClearPendingHealthWhenRunningSlotUnknown(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "nonce-1"}
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	env.config.HealthTimeout = time.Millisecond
	env.config.HealthPollInterval = time.Millisecond
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotA, false, nil }

	err := updater.ProcessPendingHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "health timeout") {
		t.Fatalf("ProcessPendingHealth() error = %v, want timeout", err)
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); err != nil {
		t.Fatalf("pending_boot.json missing after unknown running slot: %v", err)
	}
}

func TestUpdaterCommitsPendingHealthWhenMiscPrefersOldSlotButRunningTarget(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "nonce-1"}
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	// Simulate the final try after SPL has decremented B to zero: misc now prefers
	// old slot A, but Linux is still running the just-booted target slot B.
	ab.Slots[SlotA] = SlotData{Priority: MaxPriority - 1, SuccessfulBoot: true}
	ab.Slots[SlotB] = SlotData{Priority: MaxPriority}
	if err := writeMiscFile(env.miscPath, ab); err != nil {
		t.Fatalf("writeMiscFile() error = %v", err)
	}
	env.state.TargetVersion = env.version
	env.state.TargetBuildTime = env.buildTime
	env.state.TargetSlot = SlotB
	env.state.DownloadedHashes = map[string]string{"boot": testHashB}
	env.saveState(t)
	marker := HealthMarker{Slot: "b", Version: env.version, BuildTime: env.buildTime, Nonce: "nonce-1", BootID: currentBootID()}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("Marshal(HealthMarker) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.stateDir, "health.ok"), data, 0o644); err != nil {
		t.Fatalf("WriteFile(health.ok) error = %v", err)
	}
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotB, true, nil }

	if err := updater.ProcessPendingHealth(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealth() error = %v", err)
	}
	ab, err = readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if !ab.Slots[SlotB].SuccessfulBoot || ab.Slots[SlotB].TriesRemaining != 0 {
		t.Fatalf("slot B after health = %+v, want successful with zero tries", ab.Slots[SlotB])
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); !os.IsNotExist(err) {
		t.Fatalf("pending_boot.json still exists after commit: %v", err)
	}
}

func TestUpdaterClearsPendingHealthAfterRollbackToOldSlot(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "nonce-1"}
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	env.config.HealthTimeout = time.Millisecond
	env.config.HealthPollInterval = time.Millisecond
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotA, true, nil }

	if err := updater.ProcessPendingHealth(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealth() error = %v", err)
	}
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); !os.IsNotExist(err) {
		t.Fatalf("pending_boot.json still exists after rollback: %v", err)
	}
}

func TestUpdaterDoesNotClearPendingWhenMiscStillPrefersTargetButRunningOldSlot(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "nonce-1"}
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if err := ab.SetActive(SlotB, 2, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := writeMiscFile(env.miscPath, ab); err != nil {
		t.Fatalf("writeMiscFile() error = %v", err)
	}
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotA, true, nil }

	err = updater.ProcessPendingHealth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pending target slot") {
		t.Fatalf("ProcessPendingHealth() error = %v, want pending target mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); err != nil {
		t.Fatalf("pending_boot.json missing after reboot mismatch: %v", err)
	}
}

func TestUpdaterSelectiveOEMCommitPreservesCompatibleOmittedTargetPartitions(t *testing.T) {
	env := newUpdaterTestEnv(t)
	env.state.Slots["b"] = SlotPartitionInfo{Partitions: map[string]PartitionVersion{
		"boot":   {Version: "factory", Hash: testHashA},
		"oem":    {Version: "factory", Hash: testHashA},
		"rootfs": {Version: "factory", Hash: testHashA},
	}}
	env.saveState(t)
	manifest := env.signedManifest(map[string][]byte{"oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2")}, func(m *Manifest) {
		m.Parts = []ManifestPart{m.Parts[0]}
		m.Parts[0].RequiresPartitions = []string{"boot=factory:" + testHashA, "rootfs=factory:" + testHashA}
	})
	server := env.releaseServer(t, manifest, map[string][]byte{"oem_b.img": []byte("oem-b-v2")})
	env.config.APIBase = server.URL

	result, err := env.updater().CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce() error = %v", err)
	}
	if !result.Updated || result.TargetSlot != SlotB {
		t.Fatalf("CheckOnce() = %+v, want update to slot B", result)
	}
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if err := ab.SetActive(SlotB, 1, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := writeMiscFile(env.miscPath, ab); err != nil {
		t.Fatalf("writeMiscFile() error = %v", err)
	}
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime}
	pendingBytes, err := os.ReadFile(filepath.Join(env.stateDir, "pending_boot.json"))
	if err != nil {
		t.Fatalf("ReadFile(pending_boot.json) error = %v", err)
	}
	if err := json.Unmarshal(pendingBytes, &pending); err != nil {
		t.Fatalf("Unmarshal(pending) error = %v", err)
	}
	marker := HealthMarker{Slot: "b", Version: env.version, BuildTime: env.buildTime, Nonce: pending.Nonce, BootID: currentBootID()}
	markerBytes, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("Marshal(HealthMarker) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.stateDir, "health.ok"), markerBytes, 0o644); err != nil {
		t.Fatalf("WriteFile(health.ok) error = %v", err)
	}
	updater := env.updater()
	updater.currentSlot = func() (Slot, bool, error) { return SlotB, true, nil }

	if err := updater.ProcessPendingHealth(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealth() error = %v", err)
	}
	state, err := LoadState(filepath.Join(env.stateDir, "state.json"))
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	parts := state.Slots["b"].Partitions
	if got := parts["boot"]; got.Version != "factory" || got.Hash != testHashA {
		t.Fatalf("boot partition = %+v, want preserved factory", got)
	}
	if got := parts["rootfs"]; got.Version != "factory" || got.Hash != testHashA {
		t.Fatalf("rootfs partition = %+v, want preserved factory", got)
	}
	oemHash := sha256.Sum256([]byte("oem-b-v2"))
	if got := parts["oem"]; got.Version != env.version || got.Hash != hex.EncodeToString(oemHash[:]) {
		t.Fatalf("oem partition = %+v, want committed update", got)
	}
}

func TestUpdaterProcessesPendingHealthSuccessDuringWindow(t *testing.T) {
	env := newUpdaterTestEnv(t)
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "nonce-1"}
	if err := WritePendingBoot(filepath.Join(env.stateDir, "pending_boot.json"), pending); err != nil {
		t.Fatalf("WritePendingBoot() error = %v", err)
	}
	ab, err := readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if err := ab.SetActive(SlotB, 2, false); err != nil {
		t.Fatalf("SetActive() error = %v", err)
	}
	if err := writeMiscFile(env.miscPath, ab); err != nil {
		t.Fatalf("writeMiscFile() error = %v", err)
	}
	env.state.TargetVersion = env.version
	env.state.TargetBuildTime = env.buildTime
	env.state.TargetSlot = SlotB
	env.state.DownloadedHashes = map[string]string{"boot": testHashA, "oem": testHashB}
	env.saveState(t)
	env.config.HealthTimeout = 200 * time.Millisecond
	env.config.HealthPollInterval = time.Millisecond

	go func() {
		time.Sleep(10 * time.Millisecond)
		marker := HealthMarker{Slot: "b", Version: env.version, BuildTime: env.buildTime, Nonce: "nonce-1", BootID: currentBootID()}
		data, _ := json.Marshal(marker)
		_ = os.WriteFile(filepath.Join(env.stateDir, "health.ok"), data, 0o644)
	}()

	if err := env.updater().ProcessPendingHealth(context.Background()); err != nil {
		t.Fatalf("ProcessPendingHealth() error = %v", err)
	}
	ab, err = readMiscFile(env.miscPath)
	if err != nil {
		t.Fatalf("readMiscFile() error = %v", err)
	}
	if !ab.Slots[SlotB].SuccessfulBoot || ab.Slots[SlotB].TriesRemaining != 0 {
		t.Fatalf("slot B after health = %+v, want successful with zero tries", ab.Slots[SlotB])
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); !os.IsNotExist(err) {
		t.Fatalf("pending_boot.json still exists: %v", err)
	}
	state, err := LoadState(filepath.Join(env.stateDir, "state.json"))
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if state.LastCommittedVersion != env.version || state.Slots["b"].Partitions["boot"].Hash != testHashA {
		t.Fatalf("state after health = %+v", state)
	}
}

func TestUpdaterCleansPendingBootWhenMiscSwitchFails(t *testing.T) {
	env := newUpdaterTestEnv(t)
	manifest := env.signedManifest(map[string][]byte{"boot_a.img": []byte("boot-a-v2"), "boot_b.img": []byte("boot-b-v2"), "oem_a.img": []byte("oem-a-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")}, nil)
	server := env.releaseServer(t, manifest, map[string][]byte{"boot_b.img": []byte("boot-b-v2"), "oem_b.img": []byte("oem-b-v2"), "rootfs.img": []byte("rootfs-v2")})
	env.config.APIBase = server.URL
	updater := env.updater()
	updater.writeABData = func(ABData) error { return fmt.Errorf("forced misc write failure") }

	_, err := updater.CheckOnce(context.Background())
	if err == nil {
		t.Fatalf("CheckOnce() error = nil, want misc write failure")
	}
	if _, err := os.Stat(filepath.Join(env.stateDir, "pending_boot.json")); !os.IsNotExist(err) {
		t.Fatalf("pending_boot.json still exists after misc failure: %v", err)
	}
	if env.reboots != 0 {
		t.Fatalf("reboots = %d, want 0", env.reboots)
	}
}

type updaterTestEnv struct {
	t           *testing.T
	stateDir    string
	downloadDir string
	blockDir    string
	miscPath    string
	pub         ed25519.PublicKey
	priv        ed25519.PrivateKey
	config      UpdaterConfig
	state       State
	version     string
	buildTime   string
	reboots     int
}

func newUpdaterTestEnv(t *testing.T) *updaterTestEnv {
	t.Helper()
	tmp := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	env := &updaterTestEnv{
		t:           t,
		stateDir:    filepath.Join(tmp, "state"),
		downloadDir: filepath.Join(tmp, "downloads"),
		blockDir:    filepath.Join(tmp, "block"),
		miscPath:    filepath.Join(tmp, "misc.img"),
		pub:         pub,
		priv:        priv,
		version:     "20260521-120000-abcdef0",
		buildTime:   "2026-05-21T12:00:00Z",
	}
	if err := os.MkdirAll(env.blockDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(blockDir) error = %v", err)
	}
	for name, content := range map[string]string{
		"boot_a":   "old-boot-a",
		"boot_b":   "old-boot-b",
		"oem_a":    "old-oem-a",
		"oem_b":    "old-oem-b",
		"rootfs_a": "old-rootfs-a",
		"rootfs_b": "old-rootfs-b",
	} {
		if err := os.WriteFile(filepath.Join(env.blockDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	if err := CreateMiscImage(env.miscPath, DefaultMiscSize); err != nil {
		t.Fatalf("CreateMiscImage() error = %v", err)
	}
	env.state = NewFactoryState("factory", "2026-05-21T10:00:00Z", testHashA)
	env.state.ActiveSlot = SlotA
	env.saveState(t)
	if err := os.WriteFile(filepath.Join(env.stateDir, "health.ok"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(health.ok) error = %v", err)
	}
	env.config = UpdaterConfig{
		StateDir:           env.stateDir,
		DownloadDir:        env.downloadDir,
		MiscPath:           env.miscPath,
		BlockDir:           env.blockDir,
		Repo:               "owner/repo",
		Channel:            "stable",
		PublicKey:          env.pub,
		SwitchTries:        3,
		HealthTimeout:      10 * time.Millisecond,
		HealthPollInterval: time.Millisecond,
	}
	return env
}

func (e *updaterTestEnv) saveState(t *testing.T) {
	t.Helper()
	if err := SaveState(filepath.Join(e.stateDir, "state.json"), e.state); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}
}

func (e *updaterTestEnv) updater() *Updater {
	updater, err := NewUpdater(e.config, func() error {
		e.reboots++
		return nil
	})
	if err != nil {
		e.t.Fatalf("NewUpdater() error = %v", err)
	}
	return updater
}

func (e *updaterTestEnv) signedManifest(assetBytes map[string][]byte, mutate func(*Manifest)) []byte {
	e.t.Helper()
	manifest := e.manifestValue(assetBytes, mutate)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		e.t.Fatalf("Marshal(manifest) error = %v", err)
	}
	return encoded
}

func (e *updaterTestEnv) manifestValue(assetBytes map[string][]byte, mutate func(*Manifest)) Manifest {
	e.t.Helper()
	asset := func(name string) *ManifestAsset {
		b, ok := assetBytes[name]
		if !ok {
			return nil
		}
		sum := sha256.Sum256(b)
		return &ManifestAsset{Name: name, Size: int64(len(b)), SHA256: hex.EncodeToString(sum[:])}
	}
	manifest := Manifest{
		SchemaVersion: 1,
		Channel:       "stable",
		Version:       e.version,
		BuildTime:     e.buildTime,
		Parts: []ManifestPart{
			{Name: "boot", AssetA: asset("boot_a.img"), AssetB: asset("boot_b.img")},
			{Name: "oem", AssetA: asset("oem_a.img"), AssetB: asset("oem_b.img")},
			{Name: "rootfs", Asset: asset("rootfs.img")},
		},
		Signature: ManifestSignature{Algorithm: "ed25519"},
	}
	filtered := manifest.Parts[:0]
	for _, part := range manifest.Parts {
		if part.Name == "boot" && (part.AssetA != nil || part.AssetB != nil) || part.Name != "boot" && (part.Asset != nil || part.AssetA != nil || part.AssetB != nil) {
			filtered = append(filtered, part)
		}
	}
	manifest.Parts = filtered
	if mutate != nil {
		mutate(&manifest)
	}
	canonical, err := CanonicalManifestJSON(manifest)
	if err != nil {
		e.t.Fatalf("CanonicalManifestJSON() error = %v", err)
	}
	manifest.Signature.Value = hex.EncodeToString(ed25519.Sign(e.priv, canonical))
	return manifest
}

func (e *updaterTestEnv) releaseServer(t *testing.T, manifest []byte, assets map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repos/owner/repo/releases/latest") {
			var release struct {
				Assets []githubAsset `json:"assets"`
			}
			release.Assets = append(release.Assets, githubAsset{Name: "manifest.json", BrowserDownloadURL: "http://" + r.Host + "/assets/manifest.json"})
			for name := range assets {
				release.Assets = append(release.Assets, githubAsset{Name: name, BrowserDownloadURL: "http://" + r.Host + "/assets/" + name})
			}
			_ = json.NewEncoder(w).Encode(release)
			return
		}
		if r.URL.Path == "/assets/manifest.json" {
			_, _ = w.Write(manifest)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if b, ok := assets[name]; ok {
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func (e *updaterTestEnv) authReleaseServer(t *testing.T, manifest []byte, assets map[string][]byte, wantAuth string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/repos/owner/repo/releases/latest") {
			var release struct {
				Assets []githubAsset `json:"assets"`
			}
			release.Assets = append(release.Assets, githubAsset{Name: "manifest.json", BrowserDownloadURL: "http://" + r.Host + "/assets/manifest.json"})
			for name := range assets {
				release.Assets = append(release.Assets, githubAsset{Name: name, BrowserDownloadURL: "http://" + r.Host + "/assets/" + name})
			}
			_ = json.NewEncoder(w).Encode(release)
			return
		}
		if r.URL.Path == "/assets/manifest.json" {
			_, _ = w.Write(manifest)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if b, ok := assets[name]; ok {
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("ReadFile(%s) = %q, want %q", path, got, want)
	}
}

func readMiscFile(path string) (ABData, error) {
	f, err := os.Open(path)
	if err != nil {
		return ABData{}, err
	}
	defer f.Close()
	return ReadABData(f)
}

func writeMiscFile(path string, ab ABData) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteABDataAt(f, ab)
}
