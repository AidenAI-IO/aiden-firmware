package ota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubLatestReleaseAssetsUsesTokenAndMapsAssets(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"assets":[{"name":"manifest.json","browser_download_url":"https://example.test/manifest.json"},{"name":"boot_b.img","browser_download_url":"https://example.test/boot_b.img"}]}`))
	}))
	defer server.Close()

	assets, err := FetchLatestReleaseAssets(context.Background(), server.URL, "secret-token")
	if err != nil {
		t.Fatalf("FetchLatestReleaseAssets() error = %v", err)
	}
	if auth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", auth)
	}
	if assets["manifest.json"] != "https://example.test/manifest.json" || assets["boot_b.img"] != "https://example.test/boot_b.img" {
		t.Fatalf("assets = %#v", assets)
	}
}

func TestGitHubLatestReleaseAssetsReturnsClearErrors(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		}))
		defer server.Close()
		_, err := FetchLatestReleaseAssets(context.Background(), server.URL, "")
		if err == nil || !strings.Contains(err.Error(), "GitHub release status 429") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}))
		defer server.Close()
		_, err := FetchLatestReleaseAssets(context.Background(), server.URL, "")
		if err == nil || !strings.Contains(err.Error(), "decode GitHub release JSON") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing assets", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"assets":[]}`))
		}))
		defer server.Close()
		_, err := FetchLatestReleaseAssets(context.Background(), server.URL, "")
		if err == nil || !strings.Contains(err.Error(), "no release assets") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestGitHubRequireReleaseAssetsReturnsURLsAndNamesMissingAssets(t *testing.T) {
	assets := map[string]string{
		"manifest.json": "https://example.test/manifest.json",
		"boot_b.img":    "https://example.test/boot_b.img",
	}
	urls, err := RequireReleaseAssets(assets, "manifest.json", "boot_b.img")
	if err != nil {
		t.Fatalf("RequireReleaseAssets() error = %v", err)
	}
	if urls["manifest.json"] != assets["manifest.json"] || urls["boot_b.img"] != assets["boot_b.img"] {
		t.Fatalf("urls = %#v", urls)
	}

	_, err = RequireReleaseAssets(assets, "manifest.json", "rootfs_b.img")
	if err == nil || !strings.Contains(err.Error(), "missing required release asset rootfs_b.img") {
		t.Fatalf("missing required asset error = %v", err)
	}
}

func TestGitHubRequireReleaseAssetsRejectsMissingManifest(t *testing.T) {
	_, err := RequireReleaseAssets(map[string]string{"boot_b.img": "https://example.test/boot_b.img"}, "manifest.json", "boot_b.img")
	if err == nil || !strings.Contains(err.Error(), "missing required release asset manifest.json") {
		t.Fatalf("missing manifest error = %v", err)
	}
}
