package ota

import (
	"net/http"
	"strings"
	"testing"
)

func TestOTAProxyFromEnvironmentRejectsDuplicateScheme(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://http://proxy.example:7893")

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/owner/repo/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = otaProxyFromEnvironment(req)
	if err == nil {
		t.Fatal("otaProxyFromEnvironment() error = nil, want duplicate scheme error")
	}
	if !strings.Contains(err.Error(), "HTTPS_PROXY") || !strings.Contains(err.Error(), "duplicate scheme") {
		t.Fatalf("otaProxyFromEnvironment() error = %v, want env name and duplicate scheme", err)
	}
}

func TestOTAProxyFromEnvironmentRejectsMixedAssignments(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:7893\u00a0http_proxy=http://proxy.example:7893")

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/owner/repo/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = otaProxyFromEnvironment(req)
	if err == nil {
		t.Fatal("otaProxyFromEnvironment() error = nil, want whitespace error")
	}
	if !strings.Contains(err.Error(), "HTTPS_PROXY") || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("otaProxyFromEnvironment() error = %v, want env name and whitespace", err)
	}
}

func TestOTAProxyFromEnvironmentAllowsValidProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/owner/repo/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := otaProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("otaProxyFromEnvironment() error = %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("proxyURL = %v, want configured proxy", proxyURL)
	}
}

func TestOTAProxyFromEnvironmentFallsBackToAllProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("ALL_PROXY", "http://127.0.0.1:7891")

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/owner/repo/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := otaProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("otaProxyFromEnvironment() error = %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7891" {
		t.Fatalf("proxyURL = %v, want ALL_PROXY fallback", proxyURL)
	}
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "no_proxy", "NO_PROXY"} {
		t.Setenv(key, "")
	}
}
