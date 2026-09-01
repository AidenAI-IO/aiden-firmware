package configweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxRequestBodySize = 64 * 1024
	maxSystemEnvSize   = 64 * 1024
	maxAgentConfigSize = 1024 * 1024
)

type Server struct {
	options Options
	http    *http.Server

	restartMu               sync.Mutex
	restartCommand          *exec.Cmd
	restartDone             chan error
	restartDeferred         bool
	restartReadinessPending bool
	sttTestActive           bool
}

func NewServer(options Options) (*Server, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	s := &Server{options: options}
	s.http = &http.Server{
		Addr:              options.Addr(),
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       65 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

func (s *Server) ListenAndServe() error {
	log.Printf("[config_web] listening on %s", s.options.Addr())
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.startDeferredRestartIfIdle()

	if s.serveStatic(w, r) {
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/llm-logs":
		s.handleLLMLogs(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/llm-logs/export/"):
		s.handleLLMLogExport(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/llm-logs/import/"):
		s.handleLLMLogImport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/storage/status":
		s.handleStorageStatus(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == "/api/storage/format" || r.URL.Path == "/api/storage/eject"):
		s.proxyAgent(w, r, r.URL.RequestURI())
	case r.Method == http.MethodGet && r.URL.Path == "/api/agent/status":
		s.handleAgentStatus(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/agent/logs":
		s.handleAgentLogs(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/export":
		s.handleSupportLogsExport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/ota/logs":
		s.handleOTALogs(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == "/api/ota/update" || r.URL.Path == "/api/ota/check-now"):
		s.handleOTAUpdate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/config":
		s.handleGetConfig(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/models":
		s.proxyAgent(w, r, r.URL.RequestURI())
	case r.Method == http.MethodGet && (r.URL.Path == "/api/config-meta" || r.URL.Path == "/api/config/meta"):
		s.handleConfigMeta(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/config":
		s.handlePostConfig(w, r)
	case r.Method == http.MethodPut && r.URL.Path == "/api/config/locale":
		s.handlePutLocale(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/system/env":
		s.handleSystemEnv(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/reboot":
		s.handleReboot(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/wifi/scan":
		s.handleWiFiScan(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/wifi/connect":
		s.handleWiFiConnect(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/wifi/forget":
		s.handleWiFiForget(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/config/test/stt/start":
		s.handleSTTTestStart(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/config/test/stt/stop":
		s.handleSTTTestStop(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/hid/usb-reenumerate":
		s.handleUSBReenumerate(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/config/test":
		s.handleConfigTest(w, r)
	default:
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) bool {
	relative := ""
	entry := false
	switch r.URL.Path {
	case "/":
		relative, entry = "index.html", true
	case "/llm-logs":
		relative, entry = "llm-logs.html", true
	default:
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			decoded, ok := safeAssetPath(strings.TrimPrefix(r.URL.EscapedPath(), "/assets/"))
			if !ok {
				http.Error(w, "Not Found", http.StatusNotFound)
				return true
			}
			relative = filepath.Join("assets", decoded)
		} else {
			return false
		}
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return true
	}

	file, info, err := openStaticFileNoSymlinks(s.options.WebRoot, relative)
	if err != nil {
		if entry {
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		} else {
			http.Error(w, "Not Found", http.StatusNotFound)
		}
		return true
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "no-cache")
	contentType := staticContentType(relative)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	_, _ = io.Copy(w, file)
	return true
}

func staticContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

func safeAssetPath(escaped string) (string, bool) {
	if escaped == "" {
		return "", false
	}
	parts := strings.Split(escaped, "/")
	decoded := make([]string, 0, len(parts))
	for _, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "" || value == "." || value == ".." ||
			strings.ContainsAny(value, "/\\\x00") {
			return "", false
		}
		decoded = append(decoded, value)
	}
	return filepath.Join(decoded...), true
}

func openStaticFileNoSymlinks(root, relative string) (*os.File, os.FileInfo, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		if err == nil {
			err = fmt.Errorf("static root is not a directory")
		}
		return nil, nil, err
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("symlink asset rejected")
		}
	}
	file, err := os.Open(current)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err == nil {
			err = fmt.Errorf("asset is not a regular file")
		}
		return nil, nil, err
	}
	return file, info, nil
}

func readJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSONError(w, status, "invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}
