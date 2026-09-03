package configweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUint64ValuePreservesJSONNumber(t *testing.T) {
	const value = "18446744073709551615"
	if got := uint64Value(json.Number(value)); got != ^uint64(0) {
		t.Fatalf("revision=%d, want %d", got, ^uint64(0))
	}
}

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

func TestStorageStatusUsesAgentResponseContract(t *testing.T) {
	options := testOptions(t)
	if err := os.WriteFile(options.StorageStatePath, []byte("SD_PRESENT=1\nSD_MOUNTED=1\nSD_DEVICE=/dev/mmcblk2p1\nSD_MOUNTPOINT=/mnt/sdcard\nEFFECTIVE_MODE=2\nSD_TOTAL_BYTES=100\nSD_FREE_BYTES=40\nFORMAT_STATUS=running\nFORMAT_FS=ext4\nFORMAT_AUTO=1\nMIGRATE_STATUS=failed\nMIGRATE_ERROR=copy failed\nMIGRATE_MOVED_FILES=2\nMIGRATE_MOVED_BYTES=80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	payload := server.storageStatusValue()
	if _, legacy := payload["sd_present"]; legacy {
		t.Fatalf("storage response still exposes legacy top-level fields: %#v", payload)
	}
	card, ok := payload["card"].(map[string]any)
	if !ok || card["present"] != true || card["mounted"] != true || card["total_bytes"] != int64(100) || card["free_bytes"] != int64(40) {
		t.Fatalf("card=%#v", payload["card"])
	}
	job, ok := payload["format_job"].(map[string]any)
	if !ok || job["status"] != "running" || job["fs"] != "ext4" || job["auto"] != true {
		t.Fatalf("format_job=%#v", payload["format_job"])
	}
	migration, ok := payload["migration"].(map[string]any)
	if !ok || migration["status"] != "failed" || migration["moved_files"] != 2 {
		t.Fatalf("migration=%#v", payload["migration"])
	}
}

func TestConfigTestAcceptsRealtimeInputMode(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	request := `{"section":"agent","values":{"input_mode":"realtime","vad_speech_threshold":0.5,"screen_stable_diff_threshold":6,"silence_ms":550,"min_speech_ms":300,"voice_followup_timeout_ms":1000,"voice_first_turn_timeout_ms":1000,"voice_max_turns":2,"voice_max_response_tokens":100,"screenshot_keep_n":3,"screenshot_prune_interval":2,"screen_stable_timeout_ms":2000,"screen_stable_ms":250,"max_iterations":-1}}`
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/config/test", strings.NewReader(request)))
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		OK      bool `json:"ok"`
		Results []struct {
			Check  string `json:"check"`
			Passed bool   `json:"passed"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, result := range payload.Results {
		if result.Check == "input_mode" {
			if !result.Passed || result.Detail != "got 'realtime', allowed: text/stt/realtime" {
				t.Fatalf("input_mode result=%+v", result)
			}
			return
		}
	}
	t.Fatalf("input_mode result missing: %+v", payload.Results)
}

func TestModelsAreProxiedByConfigWeb(t *testing.T) {
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
	if resp.Code != http.StatusOK || resp.Header().Get("X-Upstream") != "yes" {
		t.Fatalf("models proxy status=%d headers=%v body=%s", resp.Code, resp.Header(), resp.Body.String())
	}
}

func TestSTTTestRoutesAreServedByConfigWeb(t *testing.T) {
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "recording"})
	}))
	defer upstream.Close()
	options := testOptions(t)
	options.AgentHTTPBaseURL = upstream.URL
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	server.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/api/config/test/stt/start", strings.NewReader(`{"stt_values":{},"audio_values":{}}`)))
	if start.Code != http.StatusOK {
		t.Fatalf("STT start status=%d body=%s", start.Code, start.Body.String())
	}
	stop := httptest.NewRecorder()
	server.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/api/config/test/stt/stop", strings.NewReader(`{}`)))
	if stop.Code != http.StatusOK {
		t.Fatalf("STT stop status=%d body=%s", stop.Code, stop.Body.String())
	}
	if strings.Join(requests, ",") != "POST /api/config-test/stt/start,POST /api/config-test/stt/stop" {
		t.Fatalf("upstream requests=%v", requests)
	}
}

func TestAPIRouteHeaders(t *testing.T) {
	server, err := NewServer(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "canonical", path: "/api/device/status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, test.path, nil))
			if resp.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
			if got := resp.Header().Get("X-Aiden-API-Version"); got != "1" {
				t.Fatalf("API version header=%q", got)
			}
		})
	}
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/agent/status"},
		{http.MethodPost, "/api/config"},
		{http.MethodPost, "/api/wifi/scan"},
		{http.MethodPost, "/api/ota/update"},
		{http.MethodGet, "/api/llm-logs"},
	} {
		resp := httptest.NewRecorder()
		server.APIHandler().ServeHTTP(resp, httptest.NewRequest(test.method, test.path, nil))
		if resp.Code != http.StatusNotFound {
			t.Errorf("retired route %s %s returned status=%d", test.method, test.path, resp.Code)
		}
	}
}

func TestConfigPatchReportsPersistedAndAppliedRevision(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"ok\":true,\"config\":{\"agent\":{\"locale\":\"en-US\"}},\"changed_paths\":[\"agent.locale\"],\"reboot_required\":false,\"persisted\":true,\"revision\":7}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/config/reload" || r.Method != http.MethodPost {
			t.Fatalf("reload request=%s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": true, "revision": 7})
	}))
	defer reload.Close()
	options.AgentBinary = fakeAgent
	options.AgentHTTPBaseURL = reload.URL
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"config":{"agent":{"locale":"en-US"}}}`)))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"persisted":true`) || !strings.Contains(resp.Body.String(), `"applied":true`) {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestConfigPatchUsesUpdateHandler(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"ok\":true,\"config\":{\"agent\":{\"locale\":\"en-US\"}},\"changed_paths\":[],\"reboot_required\":false}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	options.AgentBinary = fakeAgent
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}

	body := strings.NewReader(`{"config":{"agent":{"locale":"en-US"}}}`)
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", body))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"locale":"en-US"`) {
		t.Fatalf("config patch: status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSystemEnvironmentGet(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}

	read := func() map[string]any {
		t.Helper()
		resp := httptest.NewRecorder()
		server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/system/environment", nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	if got := read()["system_env"]; got != "" {
		t.Fatalf("missing env content=%q", got)
	}
	if err := os.WriteFile(options.SystemEnvPath, []byte("HTTP_PROXY=http://proxy.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read()["system_env"]; got != "HTTP_PROXY=http://proxy.example\n" {
		t.Fatalf("env content=%q", got)
	}
}

func TestLLMLogRoutesPreserveEncodedName(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(server.llmLogDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "llm-http-a+b.log"
	if err := os.WriteFile(filepath.Join(server.llmLogDir(), name), []byte("entry"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "canonical", path: "/api/logs/llm/llm-http-a%2Bb.log"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, test.path, nil))
			if resp.Code != http.StatusOK || resp.Body.String() != "entry" {
				t.Fatalf("status=%d body=%q", resp.Code, resp.Body.String())
			}
		})
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/llm-logs/export/llm-http-a%2Bb.log", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("legacy LLM log route returned status=%d", resp.Code)
	}

	resp = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/logs/llm/llm-http-upload%2B1.log", strings.NewReader("uploaded"))
	server.APIHandler().ServeHTTP(resp, request)
	if resp.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", resp.Code, resp.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(server.llmLogDir(), "llm-http-upload+1.log"))
	if err != nil || string(data) != "uploaded" {
		t.Fatalf("imported data=%q err=%v", data, err)
	}
}

func TestAPIRouteCatalogHasNoDuplicates(t *testing.T) {
	seen := map[routeVariant]apiEndpoint{}
	for _, route := range apiRoutes {
		if previous, exists := seen[route.canonical]; exists {
			t.Fatalf("duplicate route %s %s for endpoints %d and %d", route.canonical.method, route.canonical.path, previous, route.endpoint)
		}
		seen[route.canonical] = route.endpoint
	}
}

func TestUnknownAPIRouteReturnsNotFound(t *testing.T) {
	server, err := NewServer(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/unknown", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestVersionedAndRetiredSchemaRoutesReturnNotFound(t *testing.T) {
	server, err := NewServer(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/config", "/api/config-meta", "/api/config/meta"} {
		resp := httptest.NewRecorder()
		server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))
		if resp.Code != http.StatusNotFound {
			t.Errorf("%s returned status=%d body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestStorageIsProxiedByConfigWeb(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": r.URL.Path})
	}))
	defer upstream.Close()
	options := testOptions(t)
	options.AgentHTTPBaseURL = upstream.URL
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/storage/status"},
		{http.MethodPost, "/api/storage/format"},
		{http.MethodPost, "/api/storage/eject"},
	} {
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`)))
		if resp.Code != http.StatusOK || resp.Header().Get("X-Upstream") != "yes" {
			t.Errorf("%s %s returned status=%d headers=%v", test.method, test.path, resp.Code, resp.Header())
		}
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
