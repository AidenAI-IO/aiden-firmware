package agent

import (
	"net/http"
	"testing"
)

func TestProxyFuncUsesConfiguredHTTPSProxy(t *testing.T) {
	fn := proxyFunc(ProxyConfig{
		HTTPSProxy: "http://127.0.0.1:7890",
		NoProxy:    "localhost,127.0.0.1",
	})

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := fn(req)
	if err != nil {
		t.Fatalf("proxyFunc() error = %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("proxyURL = %v, want configured proxy", proxyURL)
	}
}

func TestProxyFuncHonorsNoProxy(t *testing.T) {
	fn := proxyFunc(ProxyConfig{
		AllProxy: "http://127.0.0.1:7890",
		NoProxy:  "localhost,*.invalid,api.openai.com,192.168.0.0/16",
	})

	for _, target := range []string{
		"https://api.openai.com/v1/models",
		"https://api.invalid/v1/models",
		"http://192.168.1.10/status",
	} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		proxyURL, err := fn(req)
		if err != nil {
			t.Fatalf("proxyFunc(%s) error = %v", target, err)
		}
		if proxyURL != nil {
			t.Fatalf("proxyFunc(%s) = %v, want bypass", target, proxyURL)
		}
	}
}

func TestProxyFuncHonorsIPv6NoProxy(t *testing.T) {
	fn := proxyFunc(ProxyConfig{
		AllProxy: "http://127.0.0.1:7890",
		NoProxy:  "::1",
	})
	req, err := http.NewRequest(http.MethodGet, "http://[::1]:8080/status", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := fn(req)
	if err != nil {
		t.Fatalf("proxyFunc() error = %v", err)
	}
	if proxyURL != nil {
		t.Fatalf("proxyFunc() = %v, want IPv6 no_proxy bypass", proxyURL)
	}
}

func TestProxyFuncAppliesDefaultNoProxy(t *testing.T) {
	fn := proxyFunc(ProxyConfig{
		AllProxy: "http://127.0.0.1:7890",
	})
	req, err := http.NewRequest(http.MethodGet, "http://192.168.42.1/status", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := fn(req)
	if err != nil {
		t.Fatalf("proxyFunc() error = %v", err)
	}
	if proxyURL != nil {
		t.Fatalf("proxyFunc() = %v, want default no_proxy bypass", proxyURL)
	}
}

func TestProxyFuncUsesEnvironmentProxyWithDefaultNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example:18443")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")

	fn := proxyFunc(ProxyConfig{})
	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := fn(req)
	if err != nil {
		t.Fatalf("proxyFunc() error = %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://env-proxy.example:18443" {
		t.Fatalf("proxyFunc() = %v, want environment proxy", proxyURL)
	}

	localReq, err := http.NewRequest(http.MethodGet, "https://192.168.42.1/status", nil)
	if err != nil {
		t.Fatalf("NewRequest local: %v", err)
	}
	proxyURL, err = fn(localReq)
	if err != nil {
		t.Fatalf("proxyFunc(local) error = %v", err)
	}
	if proxyURL != nil {
		t.Fatalf("proxyFunc(local) = %v, want default no_proxy bypass", proxyURL)
	}
}

func TestProxyConfigValidateRejectsRelativeURL(t *testing.T) {
	if err := (ProxyConfig{HTTPProxy: "127.0.0.1:7890"}).Validate(); err == nil {
		t.Fatal("expected invalid relative proxy URL")
	}
}
