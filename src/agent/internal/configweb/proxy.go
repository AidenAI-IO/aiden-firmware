package configweb

import (
	"bytes"
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

type proxyResponse struct {
	status int
	header http.Header
	body   []byte
}

func (s *Server) proxyAgent(w http.ResponseWriter, r *http.Request, targetPath string) {
	body, err := readRequestBody(w, r)
	if err != nil {
		return
	}
	if len(body) == 0 && strings.HasPrefix(targetPath, "/api/storage/") {
		body = []byte("{}")
	}
	_ = s.proxyAgentBytes(w, r, targetPath, body)
}

func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	if err == nil {
		return body, nil
	}
	status := http.StatusBadRequest
	message := "failed to read request body"
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		status = http.StatusRequestEntityTooLarge
		message = "request body too large"
	}
	writeJSONError(w, status, message)
	return nil, err
}

func (s *Server) proxyAgentBytes(w http.ResponseWriter, r *http.Request, targetPath string, body []byte) int {
	response, err := s.doProxyAgent(r, targetPath, body)
	if err != nil {
		status := http.StatusServiceUnavailable
		if proxyErr, ok := err.(*proxyError); ok && proxyErr.status != 0 {
			status = proxyErr.status
		}
		writeJSONError(w, status, err.Error())
		return status
	}
	writeProxyResponse(w, response)
	return response.status
}

func (s *Server) doProxyAgent(r *http.Request, targetPath string, body []byte) (proxyResponse, error) {
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
		return proxyResponse{}, &proxyError{status: http.StatusServiceUnavailable, message: "agent HTTP endpoint is invalid"}
	}
	target, err := url.Parse(targetPath)
	if err != nil || target.Path == "" || !strings.HasPrefix(target.Path, "/api/") {
		return proxyResponse{}, &proxyError{status: http.StatusBadRequest, message: "invalid request target"}
	}
	base.Path = strings.TrimRight(base.Path, "/") + target.Path
	base.RawQuery = target.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, base.String(), bytes.NewReader(body))
	if err != nil {
		return proxyResponse{}, &proxyError{status: http.StatusInternalServerError, message: err.Error()}
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return proxyResponse{}, &proxyError{status: http.StatusServiceUnavailable, message: "agent HTTP request failed: " + err.Error(), retryable: true}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBodySize+1))
	if err != nil {
		return proxyResponse{}, &proxyError{status: http.StatusServiceUnavailable, message: "agent HTTP response failed: " + err.Error(), retryable: true}
	}
	if len(data) > maxRequestBodySize {
		return proxyResponse{}, &proxyError{status: http.StatusServiceUnavailable, message: "agent HTTP response body too large"}
	}
	return proxyResponse{status: response.StatusCode, header: response.Header.Clone(), body: data}, nil
}

type proxyError struct {
	status    int
	message   string
	retryable bool
}

func (e *proxyError) Error() string { return e.message }

func writeProxyResponse(w http.ResponseWriter, response proxyResponse) {
	for key, values := range response.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}

func (s *Server) proxyAgentBytesRetry(w http.ResponseWriter, r *http.Request, targetPath string, body []byte, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		response, err := s.doProxyAgent(r, targetPath, body)
		if err == nil {
			s.clearAgentRestartReadiness()
			writeProxyResponse(w, response)
			return response.status
		}
		lastErr = err
		proxyErr, typed := err.(*proxyError)
		if !typed || !proxyErr.retryable || time.Now().After(deadline) {
			break
		}
		select {
		case <-r.Context().Done():
			lastErr = r.Context().Err()
		case <-time.After(100 * time.Millisecond):
		}
		if r.Context().Err() != nil || time.Now().After(deadline) {
			break
		}
	}
	message := "agent HTTP request failed"
	if lastErr != nil {
		message = lastErr.Error()
	}
	writeJSONError(w, http.StatusServiceUnavailable, message)
	return http.StatusServiceUnavailable
}
