package configweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aiden-agent/internal/agent"
)

type fakeStorageController struct {
	status           agent.StorageStatus
	reconfigured     agent.StorageConfig
	reconfigureCalls int
	reconfigureError error
}

func (f *fakeStorageController) Status() agent.StorageStatus { return f.status }
func (f *fakeStorageController) Reconfigure(cfg agent.StorageConfig) error {
	f.reconfigured = cfg
	f.reconfigureCalls++
	return f.reconfigureError
}
func (f *fakeStorageController) SafeEject() error {
	f.status.Card.Mounted = false
	return nil
}
func (f *fakeStorageController) StartFormat(fs, confirm string) error {
	if confirm != agent.StorageFormatConfirmToken {
		return os.ErrPermission
	}
	f.status.FormatJob = agent.StorageFormatJob{Status: agent.StorageFormatRunning, FS: fs}
	return nil
}
func (f *fakeStorageController) Stop() {}

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

func TestConfigTestRejectsInvalidTelemetryURL(t *testing.T) {
	server, err := NewServer(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	request := `{"section":"telemetry","values":{"enabled":true,"base_url":"--output=/tmp/leak","public_key":"pk","secret_key":"sk","upload_timeout_sec":1,"max_retry":1}}`
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/config/test", strings.NewReader(request)))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "base_url must be an http:// or https:// URL with a host") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestModelsSTTAndStorageAreOwnedByConfigWeb(t *testing.T) {
	server, err := NewServer(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	server.storage = &fakeStorageController{status: agent.StorageStatus{
		EffectiveMode: agent.StorageModeDual,
		Card:          agent.StorageCardStatus{Present: true, Mounted: true},
		MountPoint:    "/mnt/sdcard",
		FormatJob:     agent.StorageFormatJob{Status: agent.StorageFormatIdle},
		Migration:     agent.StorageMigrationJob{Status: agent.StorageFormatIdle},
	}}
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/models?provider=openai&locale=zh-CN"},
		{http.MethodPost, "/api/config-test/stt/start"},
		{http.MethodPost, "/api/config-test/stt/stop"},
		{http.MethodGet, "/api/storage/status"},
		{http.MethodPost, "/api/storage/format"},
		{http.MethodPost, "/api/storage/eject"},
	} {
		resp := httptest.NewRecorder()
		body := `{}`
		if test.path == "/api/storage/format" {
			body = `{"fs":"ext4","confirm":"format-sd-card"}`
		}
		server.ServeHTTP(resp, httptest.NewRequest(test.method, test.path, strings.NewReader(body)))
		if resp.Code == http.StatusNotFound {
			t.Errorf("Config Web route %s %s returned 404", test.method, test.path)
		}
	}
}

func TestModelsEndpointReturnsLocalizedCatalog(t *testing.T) {
	server, err := NewServer(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(locale string) []agent.LocalizedModelInfo {
		t.Helper()
		resp := httptest.NewRecorder()
		path := "/api/models?provider=openai&locale=" + locale
		server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
		}
		var payload struct {
			Models []agent.LocalizedModelInfo `json:"models"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Models
	}
	zh := fetch("zh-CN")
	en := fetch("en-US")
	if len(zh) == 0 || len(en) == 0 || zh[0].ID != en[0].ID || zh[0].Description == en[0].Description {
		t.Fatalf("zh=%+v en=%+v", zh, en)
	}
	if zh[0].Spec == nil || zh[0].Spec.Reasoning == nil {
		t.Fatalf("catalog model is missing capability metadata: %+v", zh[0])
	}
}

func TestModelsEndpointReturnsRequestedModelSpec(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(options.AgentConfigPath, []byte("[model]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-6\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet,
		"/api/models?provider=anthropic&model=claude-sonnet-4-6&locale=en-US", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload struct {
		Spec *struct {
			Provider  string `json:"provider"`
			Name      string `json:"name"`
			APIShape  string `json:"api_shape"`
			Reasoning *struct {
				Supported bool     `json:"supported"`
				Mode      string   `json:"mode"`
				Efforts   []string `json:"efforts"`
			} `json:"reasoning"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Spec == nil || payload.Spec.Provider != "anthropic" || payload.Spec.Name != "claude-sonnet-4-6" || payload.Spec.APIShape != "messages" {
		t.Fatalf("spec=%+v", payload.Spec)
	}
	if payload.Spec.Reasoning == nil || !payload.Spec.Reasoning.Supported || payload.Spec.Reasoning.Mode != "effort" || len(payload.Spec.Reasoning.Efforts) == 0 {
		t.Fatalf("reasoning=%+v", payload.Spec.Reasoning)
	}
}

func TestWiFiConnectionRunsAsBoundedBackgroundTask(t *testing.T) {
	options := testOptions(t)
	binDir := t.TempDir()
	ifconfig := filepath.Join(binDir, "ifconfig")
	if err := os.WriteFile(ifconfig, []byte("#!/bin/sh\n/bin/sleep 1\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPut, "/api/network/wifi/connection", strings.NewReader(`{"ssid":"test-network","psk":"secret"}`)))
	if resp.Code != http.StatusAccepted || time.Since(startedAt) > 500*time.Millisecond {
		t.Fatalf("status=%d elapsed=%s body=%s", resp.Code, time.Since(startedAt), resp.Body.String())
	}
	var started map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	taskID, _ := started["task_id"].(string)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPut, "/api/network/wifi/connection", strings.NewReader(`{"ssid":"another-network","psk":"secret"}`)),
		httptest.NewRequest(http.MethodDelete, "/api/network/wifi/connection?ssid=test-network", nil),
	} {
		conflict := httptest.NewRecorder()
		server.APIHandler().ServeHTTP(conflict, request)
		if conflict.Code != http.StatusConflict {
			t.Fatalf("concurrent Wi-Fi operation: status=%d body=%s", conflict.Code, conflict.Body.String())
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		statusResp := httptest.NewRecorder()
		path := "/api/network/wifi/connection?task_id=" + taskID
		server.APIHandler().ServeHTTP(statusResp, httptest.NewRequest(http.MethodGet, path, nil))
		var status map[string]any
		if err := json.Unmarshal(statusResp.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status["status"] != "running" {
			if status["status"] != "failed" || status["wifi_rollback"] == nil {
				t.Fatalf("status payload=%#v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Wi-Fi task did not finish: %#v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConfigUpdatesAreSerialized(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	guardDir := filepath.Join(t.TempDir(), "active")
	overlapPath := filepath.Join(t.TempDir(), "overlap")
	t.Setenv("AIDEN_TEST_CONFIG_GUARD", guardDir)
	t.Setenv("AIDEN_TEST_CONFIG_OVERLAP", overlapPath)
	script := `#!/bin/sh
cat >/dev/null
if mkdir "$AIDEN_TEST_CONFIG_GUARD" 2>/dev/null; then
  /bin/sleep 0.15
  rmdir "$AIDEN_TEST_CONFIG_GUARD"
else
  : > "$AIDEN_TEST_CONFIG_OVERLAP"
fi
printf '%s\n' '{"ok":true,"config":{},"changed_paths":[],"reboot_required":false,"persisted":true}'
`
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	options.AgentBinary = fakeAgent
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp := httptest.NewRecorder()
			server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"config":{"agent":{"locale":"en-US"}}}`)))
			responses <- resp
		}()
	}
	close(start)
	wg.Wait()
	close(responses)
	for resp := range responses {
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
		}
	}
	if _, err := os.Stat(overlapPath); !os.IsNotExist(err) {
		t.Fatalf("config-update commands overlapped: %v", err)
	}
}

func TestConfigPatchReconfiguresStorageOwner(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"ok\":true,\"config\":{},\"changed_paths\":[\"storage.mount_point\"],\"reboot_required\":false,\"persisted\":true,\"revision\":11}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": true, "revision": 11})
	}))
	defer reload.Close()
	options.AgentBinary = fakeAgent
	options.AgentHTTPBaseURL = reload.URL
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	storage := &fakeStorageController{}
	server.storage = storage
	config := "[storage]\nmount_point = \"/mnt/new-card\"\ndevice = \"mmcblk9\"\nmin_card_free_mb = 128\n"
	if err := os.WriteFile(options.AgentConfigPath, []byte(config), 0o640); err != nil {
		t.Fatal(err)
	}

	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"config":{"storage":{"mount_point":"/mnt/new-card"}}}`)))
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if storage.reconfigureCalls != 1 || storage.reconfigured.MountPointOrDefault() != "/mnt/new-card" || storage.reconfigured.DeviceOrDefault() != "mmcblk9" || storage.reconfigured.MinCardFreeMBOrDefault() != 128 {
		t.Fatalf("storage reconfigure calls=%d config=%+v", storage.reconfigureCalls, storage.reconfigured)
	}
}

func TestOTAUpdateChildKeepsLockAcrossParentDescriptorClose(t *testing.T) {
	options := testOptions(t)
	root := t.TempDir()
	ota := filepath.Join(root, "fake-ota")
	if err := os.WriteFile(ota, []byte("#!/bin/sh\n/bin/sleep 0.4\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	options.OTABinary = ota
	options.EnvRunBinary = filepath.Join(root, "missing-env-run")
	options.OTAUpdateLockPath = filepath.Join(root, "ota.lock")
	options.OTAUpdateLogPath = filepath.Join(root, "ota.log")
	options.OTAHealthLogPath = filepath.Join(root, "health.log")
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}

	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/api/ota/updates", nil))
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if !server.otaUpdateRunning() {
		t.Fatal("OTA lock was released after the parent closed its descriptor")
	}
	deadline := time.Now().Add(3 * time.Second)
	for server.otaUpdateRunning() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if server.otaUpdateRunning() {
		t.Fatal("OTA lock remained held after the supervisor exited")
	}
	logData, err := os.ReadFile(options.OTAUpdateLogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "update_exited exit_code=7") {
		t.Fatalf("OTA log missing exit marker: %q", logData)
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

func TestConfigPatchSchedulesRestartWhenRuntimeReloadRejectsChange(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"ok\":true,\"config\":{\"agent\":{\"locale\":\"zh-CN\"}},\"changed_paths\":[\"agent.locale\"],\"reboot_required\":false,\"persisted\":true,\"revision\":9}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "applied": false, "restart_required": true,
			"error": "configuration changes require an Agent restart",
		})
	}))
	defer reload.Close()
	options.AgentBinary = fakeAgent
	options.AgentHTTPBaseURL = reload.URL
	options.AgentInitScript = "/bin/true"
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"config":{"agent":{"locale":"zh-CN"}}}`)))
	if resp.Code != http.StatusServiceUnavailable ||
		!strings.Contains(resp.Body.String(), `"persisted":true`) ||
		!strings.Contains(resp.Body.String(), `"applied":false`) ||
		!strings.Contains(resp.Body.String(), `"agent_restart_scheduled":true`) {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestConfigPatchUsesUpdateHandler(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"ok\":true,\"config\":{\"agent\":{\"locale\":\"en-US\"}},\"changed_paths\":[],\"reboot_required\":false,\"persisted\":true}'\n"
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

func TestConfigPatchRejectsMissingPersistenceState(t *testing.T) {
	options := testOptions(t)
	fakeAgent := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"ok\":true,\"config\":{},\"changed_paths\":[],\"reboot_required\":false}'\n"
	if err := os.WriteFile(fakeAgent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	options.AgentBinary = fakeAgent
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"config":{}}`)))
	if resp.Code != http.StatusServiceUnavailable || !strings.Contains(resp.Body.String(), "omitted persisted state") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
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

func TestSystemEnvironmentWriteFailureReturnsServerError(t *testing.T) {
	options := testOptions(t)
	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	options.SystemEnvPath = filepath.Join(blockedParent, "system.env")
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPut, "/api/system/environment", strings.NewReader(`{"system_env":"A=1\n"}`)))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSystemEnvironmentReportsRestartLaunchFailure(t *testing.T) {
	options := testOptions(t)
	options.AgentInitScript = ""
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(resp, httptest.NewRequest(http.MethodPut, "/api/system/environment", strings.NewReader(`{"system_env":"A=1\n"}`)))
	if resp.Code != http.StatusServiceUnavailable || !strings.Contains(resp.Body.String(), `"persisted":true`) || !strings.Contains(resp.Body.String(), `"agent_restart_scheduled":false`) {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
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

func TestLLMLogImportRejectsOversizedBody(t *testing.T) {
	options := testOptions(t)
	server, err := NewServer(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/logs/llm/llm-http-oversized.log", strings.NewReader(strings.Repeat("x", maxRequestBodySize+1)))
	server.APIHandler().ServeHTTP(resp, request)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(server.llmLogDir(), "llm-http-oversized.log")); !os.IsNotExist(err) {
		t.Fatalf("oversized target exists or stat failed unexpectedly: %v", err)
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

func TestWiFiPublicValueUsesNetworkCollectionOnly(t *testing.T) {
	payload := (wiFiConfig{Country: "US", Networks: []wiFiNetwork{{SSID: "demo", PSK: "secret", Priority: 1}}}).publicValue()
	if _, exists := payload["ssid"]; exists {
		t.Fatalf("flat ssid field remains: %#v", payload)
	}
	if _, exists := payload["has_psk"]; exists {
		t.Fatalf("flat has_psk field remains: %#v", payload)
	}
	if _, exists := payload["networks"]; !exists {
		t.Fatalf("network collection missing: %#v", payload)
	}
}

func TestWiFiCountryValidation(t *testing.T) {
	for input, want := range map[string]string{
		"us":            "US",
		" CN ":          "CN",
		"USA":           "CN",
		"U1":            "CN",
		"US\nnetwork={": "CN",
	} {
		if got := normalizeWiFiCountry(input); got != want {
			t.Errorf("normalizeWiFiCountry(%q)=%q, want %q", input, got, want)
		}
	}
	if rendered := renderWiFiConfig(wiFiConfig{Country: "US\nnetwork={"}); !strings.Contains(rendered, "country=CN\n") {
		t.Fatalf("rendered invalid country: %q", rendered)
	}
}

func TestEmptyFirmwareInfoMatchesSuccessShape(t *testing.T) {
	info := emptyFirmwareInfo()
	for _, key := range []string{"version", "build_time", "current_version", "current_build_time", "target_version", "target_build_time", "previous_version", "previous_build_time", "phase", "running_slot", "target_slot", "health_status", "health_error"} {
		if value, exists := info[key]; !exists || value != "" {
			t.Errorf("%s=%#v exists=%v", key, value, exists)
		}
	}
}

func TestRunHelpReturnsSuccess(t *testing.T) {
	if code := Run([]string{"--help"}); code != 0 {
		t.Fatalf("Run(--help)=%d, want 0", code)
	}
}

func TestRunRejectsRetiredWiFiIfaceFlag(t *testing.T) {
	if code := Run([]string{"--wifi-iface", "wlan1"}); code != 1 {
		t.Fatalf("Run(--wifi-iface)=%d, want 1", code)
	}
}
