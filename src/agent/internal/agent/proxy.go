package agent

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func newProxyHTTPClient(proxy ProxyConfig) *http.Client {
	return &http.Client{Transport: newProxyTransport(proxy)}
}

func newProxyTransport(proxy ProxyConfig) http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxyFunc(proxy)
	return transport
}

func proxyFunc(proxy ProxyConfig) func(*http.Request) (*url.URL, error) {
	proxy = proxy.WithDefaults()
	if !proxy.HasProxyURL() {
		return http.ProxyFromEnvironment
	}

	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		if bypassProxy(req.URL.Hostname(), req.URL.Port(), proxy.NoProxy) {
			return nil, nil
		}

		proxyURL := strings.TrimSpace(proxy.AllProxy)
		switch strings.ToLower(req.URL.Scheme) {
		case "http":
			if strings.TrimSpace(proxy.HTTPProxy) != "" {
				proxyURL = proxy.HTTPProxy
			}
		case "https":
			if strings.TrimSpace(proxy.HTTPSProxy) != "" {
				proxyURL = proxy.HTTPSProxy
			}
		}
		if strings.TrimSpace(proxyURL) == "" {
			return nil, nil
		}
		return parseProxyURL(proxyURL)
	}
}

func validateProxyURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	_, err := parseProxyURL(raw)
	return err
}

func parseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("expected absolute proxy URL, for example http://127.0.0.1:7890")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" {
		return nil, fmt.Errorf("unsupported proxy URL scheme %q", u.Scheme)
	}
	return u, nil
}

func bypassProxy(host, port, noProxy string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}
	hostPort := host
	if port != "" {
		hostPort = net.JoinHostPort(host, port)
	}

	for _, rawRule := range strings.FieldsFunc(noProxy, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		rule := strings.TrimSpace(strings.ToLower(rawRule))
		if rule == "" {
			continue
		}
		if rule == "*" {
			return true
		}
		if strings.Contains(rule, "/") {
			if ip := net.ParseIP(host); ip != nil {
				if _, network, err := net.ParseCIDR(rule); err == nil && network.Contains(ip) {
					return true
				}
			}
			continue
		}
		if ruleIP := net.ParseIP(rule); ruleIP != nil {
			if hostIP := net.ParseIP(host); hostIP != nil && hostIP.Equal(ruleIP) {
				return true
			}
			continue
		}
		if strings.Contains(rule, ":") && !strings.HasPrefix(rule, ".") {
			if strings.EqualFold(hostPort, rule) {
				return true
			}
			continue
		}
		if strings.HasPrefix(rule, "*.") {
			base := strings.TrimPrefix(rule, "*.")
			if host == base || strings.HasSuffix(host, "."+base) {
				return true
			}
			continue
		}
		rule = strings.TrimPrefix(rule, ".")
		if host == rule || strings.HasSuffix(host, "."+rule) {
			return true
		}
	}
	return false
}
