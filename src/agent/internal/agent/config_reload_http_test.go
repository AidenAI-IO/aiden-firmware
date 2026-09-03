package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInternalConfigReloadAppliesLoopbackRevision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.toml"), []byte("[agent]\nlocale=\"en-US\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{config: Config{ConfigDir: dir}}
	server := &Server{runtime: runtime}
	req := httptest.NewRequest(http.MethodPost, "/api/internal/config/reload", strings.NewReader(`{"revision":0}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	server.handleInternalConfigReload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload["applied"] != true {
		t.Fatalf("payload=%s", rec.Body.String())
	}
	if runtime.config.LocaleOrDefault() != "en-US" {
		t.Fatalf("runtime locale=%q", runtime.config.LocaleOrDefault())
	}
}

func TestRuntimeConfigSnapshotIsSafeDuringReload(t *testing.T) {
	runtime := &Runtime{config: Config{ConfigDir: t.TempDir()}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				cfg := runtime.ConfigSnapshot()
				cfg.Locale = []string{"en-US", "zh-CN"}[index%2]
				_ = runtime.ApplyConfigSnapshot(cfg)
			}
		}(i)
	}
	wg.Wait()
}

func TestInternalConfigReloadRejectsRemoteRequest(t *testing.T) {
	server := &Server{runtime: &Runtime{config: Config{ConfigDir: t.TempDir()}}}
	req := httptest.NewRequest(http.MethodPost, "/api/internal/config/reload", strings.NewReader(`{}`))
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Aiden-Internal", "config-web")
	rec := httptest.NewRecorder()
	server.handleInternalConfigReload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInternalConfigReloadRejectsTruncatedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.toml"), []byte("[agent]\nlocale=\"en-US\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	server := &Server{runtime: &Runtime{config: Config{ConfigDir: dir}}}
	req := httptest.NewRequest(http.MethodPost, "/api/internal/config/reload", strings.NewReader(`{"revision":1`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	server.handleInternalConfigReload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentHandlerAllowsExactConfigWebOrigin(t *testing.T) {
	t.Setenv("AIDEN_CONFIG_WEB_ORIGINS", "http://example.invalid")
	server := &Server{}
	h := server.Handler()
	req := httptest.NewRequest(http.MethodOptions, "/api/models", nil)
	req.Host = "example.invalid:8080"
	req.Header.Set("Origin", "http://example.invalid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "http://example.invalid" {
		t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
	}
}
