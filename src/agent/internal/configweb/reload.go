package configweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Server) agentBaseURL() (*url.URL, error) {
	baseURL := strings.TrimSpace(s.options.AgentHTTPBaseURL)
	if baseURL == "" {
		status := s.queryAgentStatus()
		host, _ := status["port_host"].(string)
		port, _ := status["port"].(int)
		if host == "" {
			host = "127.0.0.1"
		}
		if port < 1 || port > 65535 {
			port = 8080
		}
		baseURL = fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("agent HTTP endpoint is invalid")
	}
	return base, nil
}

// reloadAgentConfig asks the independent Agent process to re-read its config.
// Config Web owns persistence; Agent owns runtime application.
func (s *Server) reloadAgentConfig(ctx context.Context, revision uint64) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{"revision": revision})
	if err != nil {
		return nil, err
	}
	base, err := s.agentBaseURL()
	if err != nil {
		return nil, err
	}
	target, _ := url.Parse("/api/internal/config/reload")
	base.Path = strings.TrimRight(base.Path, "/") + target.Path
	base.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent config reload request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("agent config reload response failed: %w", err)
	}
	var payload map[string]any
	if len(data) > maxRequestBodySize || json.Unmarshal(data, &payload) != nil || payload == nil {
		return nil, fmt.Errorf("agent config reload returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload["ok"] != true || payload["applied"] == false {
		message, _ := payload["error"].(string)
		if message == "" {
			message = fmt.Sprintf("agent config reload failed (HTTP %d)", response.StatusCode)
		}
		return payload, errors.New(message)
	}
	return payload, nil
}
