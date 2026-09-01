package configweb

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(filepath.Join(webRoot, "assets", "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webRoot, "assets", "js", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Options{
		BindAddress:      "127.0.0.1",
		Port:             8081,
		AgentConfigPath:  filepath.Join(root, "agent.toml"),
		WiFiConfigPath:   filepath.Join(root, "wpa_supplicant.conf"),
		WiFiInterface:    "wlan0",
		OTAStatePath:     filepath.Join(root, "ota-state.json"),
		CmdlinePath:      filepath.Join(root, "cmdline"),
		SystemEnvPath:    filepath.Join(root, "system.env"),
		StorageStatePath: filepath.Join(root, "storage.state"),
		WebRoot:          webRoot,
		AgentBinary:      "/bin/true",
		AgentHTTPBaseURL: "http://127.0.0.1:1",
		AgentInitScript:  filepath.Join(root, "missing-init"),
	}
}

func TestServerServesStaticAssetsAndRejectsTraversal(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || resp.Body.String() != "index" {
		t.Fatalf("root response: status=%d body=%q", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("root content type=%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/assets/js/app.js", nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || resp.Body.String() != "console.log(1)" {
		t.Fatalf("asset response: status=%d body=%q", resp.Code, resp.Body.String())
	}

	for _, path := range []string{"/assets/../index.html", "/assets/%2e%2e/index.html", "/assets/js/%2fetc%2fpasswd"} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		resp = httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Errorf("path %q returned status %d", path, resp.Code)
		}
	}

	link := filepath.Join(options.WebRoot, "assets", "js", "link.js")
	if err := os.Symlink("app.js", link); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/assets/js/link.js", nil)
	resp = httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("symlink asset returned status %d", resp.Code)
	}
}

func TestProxyForwardsQueryAndResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"target": r.URL.RequestURI()})
	}))
	defer upstream.Close()

	options := testOptions(t)
	options.AgentHTTPBaseURL = upstream.URL
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=openai&locale=zh-CN", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("X-Upstream") != "yes" || !strings.Contains(resp.Body.String(), "/api/models?provider=openai\\u0026locale=zh-CN") {
		t.Fatalf("proxy did not preserve upstream response: headers=%v body=%s", resp.Header(), resp.Body.String())
	}
}

func TestProxyRejectsOversizedRequestBody(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/storage/format", strings.NewReader(strings.Repeat("x", maxRequestBodySize+1)))
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusRequestEntityTooLarge || !strings.Contains(resp.Body.String(), "request body too large") {
		t.Fatalf("oversized proxy body: status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestStorageProxyNormalizesEmptyBody(t *testing.T) {
	var received string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	options := testOptions(t)
	options.AgentHTTPBaseURL = upstream.URL
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/storage/eject", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || received != "{}" {
		t.Fatalf("storage proxy: status=%d received=%q", resp.Code, received)
	}
}

func TestReadFileLimitedRejectsOversizedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileLimited(path, 4); err == nil {
		t.Fatal("oversized file was accepted")
	}
	data, err := readFileLimited(path, 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("exact-sized file read: data=%q err=%v", data, err)
	}
}

func TestSystemEnvParserAndWiFiConfigDefaults(t *testing.T) {
	assignments, err := parseSystemEnv("export HTTP_PROXY='http://proxy.example:8080'\nAIDEN_TEST=ok\n")
	if err != nil || len(assignments) != 2 || assignments[0].Value != "http://proxy.example:8080" {
		t.Fatalf("parsed assignments=%#v err=%v", assignments, err)
	}
	if _, err := parseSystemEnv("BROKEN='unterminated\n"); err == nil {
		t.Fatal("unterminated env value was accepted")
	}

	path := filepath.Join(t.TempDir(), "wpa.conf")
	contents := "country=US\nnetwork={\n ssid=\"demo\"\n psk=\"secret\"\n}\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadWiFiConfig(path)
	if err != nil || len(config.Networks) != 1 || config.Networks[0].SSID != "demo" || !config.Networks[0].ScanSSID {
		t.Fatalf("wifi config=%#v err=%v", config, err)
	}
}
