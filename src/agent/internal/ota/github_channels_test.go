package ota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func channelFixture(tag string, firmware bool) githubRelease {
	release := githubRelease{TagName: tag}
	if firmware {
		for _, name := range []string{"manifest.json", "boot_a.img.tar.gz", "boot_b.img.tar.gz", "rootfs.img.tar.gz"} {
			release.Assets = append(release.Assets, githubAsset{Name: name, BrowserDownloadURL: "https://example.test/" + tag + "/" + name})
		}
	}
	return release
}

func TestChannelDiscoverySkipsBusinessOtherChannelsAndDraftsAcrossPages(t *testing.T) {
	first := make([]githubRelease, 100)
	for i := range first {
		first[i] = channelFixture("dev-v0.0.20", false)
	}
	first[0] = channelFixture("prod-v9.0.0", true)
	first[1] = channelFixture("dev-v0.0.11", true)
	first[1].Draft = true
	first[2] = channelFixture("dev-v0.0.9", true)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(first)
		case "2":
			_ = json.NewEncoder(w).Encode([]githubRelease{channelFixture("dev-v0.0.10", true)})
		default:
			t.Errorf("unexpected page: %s", r.URL)
		}
	}))
	defer server.Close()
	assets, err := FetchChannelReleaseAssets(context.Background(), server.URL, "dev", "token", "")
	if err != nil || !strings.Contains(assets["manifest.json"], "dev-v0.0.10/") || requests != 2 {
		t.Fatalf("assets=%v requests=%d err=%v", assets, requests, err)
	}
}

func TestChannelDiscoveryFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"no channel OTA", 200, `[{"tag_name":"prod-v0.0.2","assets":[]}]`},
		{"incomplete OTA", 200, `[{"tag_name":"dev-v0.0.2","assets":[{"name":"manifest.json","browser_download_url":"https://example.test/m"}]}]`},
		{"API error", 403, `{}`},
		{"invalid JSON", 200, `broken`},
		{"oversized", 200, strings.Repeat(" ", (4<<20)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			if _, err := FetchChannelReleaseAssets(context.Background(), server.URL, "dev", "", ""); err == nil {
				t.Fatal("expected channel discovery error")
			}
		})
	}
}

func TestReleaseEndpointAndManifestChannel(t *testing.T) {
	endpoint, err := releaseEndpoint(UpdaterConfig{})
	if err != nil || endpoint != DefaultReleaseURL {
		t.Fatalf("legacy endpoint=%s err=%v", endpoint, err)
	}
	for _, channel := range []string{"dev", "staging", "prod"} {
		endpoint, err := releaseEndpoint(UpdaterConfig{Repo: "example/firmware", Channel: channel})
		if err != nil || endpoint != "https://api.github.com/repos/example/firmware/releases" {
			t.Fatalf("channel endpoint=%s err=%v", endpoint, err)
		}
		if err := requireManifestChannel(channel, Manifest{Channel: channel, Version: channel + "-v0.0.2"}); err != nil {
			t.Fatal(err)
		}
		for _, manifest := range []Manifest{{Channel: "other", Version: channel + "-v0.0.2"}, {Channel: channel, Version: "wrong-tag"}} {
			if requireManifestChannel(channel, manifest) == nil {
				t.Fatal("accepted a manifest from another channel/tag")
			}
		}
	}
	if _, err := releaseEndpoint(UpdaterConfig{Channel: "prdo"}); err == nil {
		t.Fatal("unknown channel silently fell back to prod")
	}
	if _, err := releaseEndpoint(UpdaterConfig{Repo: "../other"}); err == nil {
		t.Fatal("invalid repository was accepted")
	}
	if err := requireManifestChannel("", Manifest{Channel: "dev-legacy"}); err != nil {
		t.Fatal("legacy direct manifest override broke")
	}
}

func TestUpdaterChannelSelectionAndSignedChannelCheck(t *testing.T) {
	for _, signedChannel := range []string{"dev", "prod"} {
		t.Run(signedChannel, func(t *testing.T) {
			env := newUpdaterTestEnv(t)
			env.version = signedChannel + "-v0.0.2"
			images := map[string][]byte{"boot_a.img": []byte("new-a"), "boot_b.img": []byte("new-b"), "rootfs.img": []byte("new-root")}
			archives := make(map[string][]byte)
			for name, data := range images {
				archives[name+".tar.gz"] = testTarGzImage(t, name, data)
			}
			manifest := env.signedManifest(images, func(m *Manifest) {
				m.Channel = signedChannel
				m.Parts[0].AssetA = testCompressedManifestAsset("boot_a.img.tar.gz", archives["boot_a.img.tar.gz"], images["boot_a.img"])
				m.Parts[0].AssetB = testCompressedManifestAsset("boot_b.img.tar.gz", archives["boot_b.img.tar.gz"], images["boot_b.img"])
				m.Parts[1].Asset = testCompressedManifestAsset("rootfs.img.tar.gz", archives["rootfs.img.tar.gz"], images["rootfs.img"])
			})
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/releases" {
					firmware := channelFixture("dev-v0.0.2", true)
					for i := range firmware.Assets {
						firmware.Assets[i].BrowserDownloadURL = server.URL + "/" + firmware.Assets[i].Name
					}
					_ = json.NewEncoder(w).Encode([]githubRelease{channelFixture("dev-v0.0.3", false), firmware})
				} else if r.URL.Path == "/manifest.json" {
					_, _ = w.Write(manifest)
				} else if data, ok := archives[strings.TrimPrefix(r.URL.Path, "/")]; ok {
					_, _ = w.Write(data)
				} else {
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			env.config.Channel = "dev"
			env.config.ReleaseURL = server.URL + "/releases"
			result, err := env.updater().CheckOnce(context.Background())
			if signedChannel == "dev" {
				if err != nil || !result.Updated {
					t.Fatalf("update=%+v err=%v", result, err)
				}
				assertFileContent(t, filepath.Join(env.blockDir, "rootfs_b"), "new-root")
			} else {
				if err == nil || !strings.Contains(err.Error(), "configured channel") || env.reboots != 0 {
					t.Fatalf("cross-channel result=%+v err=%v reboots=%d", result, err, env.reboots)
				}
				assertFileContent(t, filepath.Join(env.blockDir, "boot_a"), "old-boot-a")
			}
		})
	}
}

func TestLoadUpdaterConfigPreservesReleaseChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"repo":"example/firmware","channel":"staging"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadUpdaterConfig(path)
	if err != nil || config.Repo != "example/firmware" || config.Channel != "staging" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}
