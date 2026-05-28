package minimax

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"

	"aiden-agent/internal/agent/tts"
)

func httpClientForConfig(cfg commonConfig) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxyFunc(cfg.proxy)
	return &http.Client{Timeout: 60 * time.Second, Transport: transport}
}

func websocketDialerForConfig(cfg commonConfig) (websocket.Dialer, error) {
	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{},
		HandshakeTimeout: wsConnTimeout,
	}
	if err := configureWebSocketProxy(&dialer, cfg.proxy); err != nil {
		return dialer, err
	}
	return dialer, nil
}

func configureWebSocketProxy(dialer *websocket.Dialer, cfg tts.ProxyConfig) error {
	proxyURL := strings.TrimSpace(cfg.AllProxy)
	if strings.TrimSpace(cfg.HTTPSProxy) != "" {
		proxyURL = cfg.HTTPSProxy
	}
	if strings.TrimSpace(proxyURL) == "" {
		return nil
	}
	u, err := parseProxyURL(proxyURL)
	if err != nil {
		return err
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		d, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return fmt.Errorf("socks5 proxy: %w", err)
		}
		ctxDialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return fmt.Errorf("socks5 proxy does not support context dialing")
		}
		dialer.NetDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ctxDialer.DialContext(ctx, network, addr)
		}
	case "http", "https":
		dialer.Proxy = func(req *http.Request) (*url.URL, error) {
			if req != nil && req.URL != nil && bypassProxy(req.URL.Hostname(), req.URL.Port(), cfg.NoProxy) {
				return nil, nil
			}
			return u, nil
		}
	default:
		return fmt.Errorf("unsupported proxy URL scheme %q", u.Scheme)
	}
	return nil
}

func proxyFunc(cfg tts.ProxyConfig) func(*http.Request) (*url.URL, error) {
	if proxyConfigIsZero(cfg) {
		return http.ProxyFromEnvironment
	}
	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		if bypassProxy(req.URL.Hostname(), req.URL.Port(), cfg.NoProxy) {
			return nil, nil
		}
		proxyURL := strings.TrimSpace(cfg.AllProxy)
		switch strings.ToLower(req.URL.Scheme) {
		case "http":
			if strings.TrimSpace(cfg.HTTPProxy) != "" {
				proxyURL = cfg.HTTPProxy
			}
		case "https":
			if strings.TrimSpace(cfg.HTTPSProxy) != "" {
				proxyURL = cfg.HTTPSProxy
			}
		}
		if strings.TrimSpace(proxyURL) == "" {
			return nil, nil
		}
		return parseProxyURL(proxyURL)
	}
}

func proxyConfigIsZero(cfg tts.ProxyConfig) bool {
	return strings.TrimSpace(cfg.HTTPProxy) == "" &&
		strings.TrimSpace(cfg.HTTPSProxy) == "" &&
		strings.TrimSpace(cfg.AllProxy) == "" &&
		strings.TrimSpace(cfg.NoProxy) == ""
}

func parseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("expected absolute proxy URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	default:
		return nil, fmt.Errorf("unsupported proxy URL scheme %q", u.Scheme)
	}
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
