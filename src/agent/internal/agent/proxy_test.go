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

func TestProxyConfigValidateRejectsRelativeURL(t *testing.T) {
	if err := (ProxyConfig{HTTPProxy: "127.0.0.1:7890"}).Validate(); err == nil {
		t.Fatal("expected invalid relative proxy URL")
	}
}
