package netproxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestParseRejectsDuplicateScheme(t *testing.T) {
	_, err := Parse("http://http://proxy.example:7893", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate scheme error")
	}
	if !strings.Contains(err.Error(), "duplicate scheme") {
		t.Fatalf("Parse() error = %v, want duplicate scheme", err)
	}
}

func TestParseRejectsEmbeddedProxyAssignmentWithNonBreakingSpace(t *testing.T) {
	_, err := Parse("http://proxy.example:7893\u00a0http_proxy=http://proxy.example:7893", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want whitespace error")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("Parse() error = %v, want whitespace", err)
	}
}

func TestParseRejectsQuotedMultipleAssignments(t *testing.T) {
	_, err := Parse("http://proxy.example:7893 http_proxy=http://proxy.example:7893", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want whitespace error")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("Parse() error = %v, want whitespace", err)
	}
}

func TestParseRejectsMissingHost(t *testing.T) {
	_, err := Parse("http://", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want missing host error")
	}
	if !strings.Contains(err.Error(), "expected absolute proxy URL") {
		t.Fatalf("Parse() error = %v, want absolute URL guidance", err)
	}
}

func TestProxyFromEnvironmentBypassesLoopbackByDefault(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTP_PROXY", "http://proxy.example:7890")

	for _, target := range []string{
		"http://localhost:8080/status",
		"http://127.0.0.1:8080/status",
		"http://[::1]:8080/status",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		proxyURL, err := ProxyFromEnvironment(req, "http", "https", "socks5")
		if err != nil {
			t.Fatalf("ProxyFromEnvironment(%s) error = %v", target, err)
		}
		if proxyURL != nil {
			t.Fatalf("ProxyFromEnvironment(%s) = %v, want loopback bypass", target, proxyURL)
		}
	}
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "no_proxy", "NO_PROXY"} {
		t.Setenv(key, "")
	}
}
