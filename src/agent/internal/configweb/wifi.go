package configweb

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"aiden-agent/internal/wifiproxy"
)

const (
	wifiConnectionTaskTimeout = 2 * time.Minute
	wifiCandidateApplyTimeout = 75 * time.Second
)

type wiFiNetwork struct {
	SSID     string
	PSK      string
	Priority int
	ScanSSID bool
	Disabled bool
}

type wiFiConfig struct {
	Country  string
	Networks []wiFiNetwork
}

func (c wiFiConfig) publicValue(proxyConfigs ...wifiproxy.Config) map[string]any {
	proxyConfig := wifiproxy.EmptyConfig()
	if len(proxyConfigs) > 0 {
		proxyConfig = proxyConfigs[0]
	}
	networks := make([]map[string]any, 0, len(c.Networks))
	for _, network := range c.Networks {
		value := map[string]any{
			"ssid": network.SSID, "has_psk": network.PSK != "", "priority": network.Priority,
			"scan_ssid": network.ScanSSID, "disabled": network.Disabled,
			"proxy_mode": string(wifiproxy.ModeSystem), "proxy_url": "", "no_proxy": "",
		}
		if configured, ok := proxyConfig.Networks[network.SSID]; ok {
			value["proxy_mode"] = string(configured.Mode)
			value["proxy_url"] = wifiproxy.RedactedURL(configured.ProxyURL)
			value["no_proxy"] = configured.NoProxy
		}
		networks = append(networks, value)
	}
	return map[string]any{"country": c.Country, "networks": networks}
}

func loadWiFiConfig(path string) (wiFiConfig, error) {
	data, err := readFileLimited(path, maxAgentConfigSize)
	if err != nil {
		return wiFiConfig{Country: "CN", Networks: []wiFiNetwork{}}, err
	}
	config := wiFiConfig{Networks: []wiFiNetwork{}}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var current *wiFiNetwork
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		switch {
		case line == "network={":
			current = &wiFiNetwork{ScanSSID: true}
		case line == "}" && current != nil:
			if current.SSID != "" {
				config.Networks = append(config.Networks, *current)
			}
			current = nil
		default:
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if current == nil {
				if key == "country" {
					config.Country = value
				}
				continue
			}
			switch key {
			case "ssid":
				current.SSID = decodeWiFiValue(value, false)
			case "psk":
				current.PSK = decodeWiFiValue(value, true)
			case "priority":
				current.Priority, _ = strconv.Atoi(value)
			case "scan_ssid":
				current.ScanSSID = value != "0"
			case "disabled":
				current.Disabled = value != "0"
			}
		}
	}
	if current != nil && current.SSID != "" {
		config.Networks = append(config.Networks, *current)
	}
	if err := scanner.Err(); err != nil {
		return wiFiConfig{}, err
	}
	normalizeWiFiPriorities(&config)
	config.Country = normalizeWiFiCountry(config.Country)
	return config, nil
}

func normalizeWiFiCountry(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 2 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
		return "CN"
	}
	return value
}

func decodeWiFiValue(value string, rawPSK bool) string {
	if rawPSK && len(value) == 64 {
		if _, err := hex.DecodeString(value); err == nil {
			return value
		}
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
		value = strings.ReplaceAll(strings.ReplaceAll(value, `\\`, `\`), `\"`, `"`)
		return value
	}
	if decoded, err := hex.DecodeString(value); err == nil {
		return string(decoded)
	}
	return value
}

func renderWiFiConfig(config wiFiConfig) string {
	var output strings.Builder
	output.WriteString("ctrl_interface=/var/run/wpa_supplicant\nupdate_config=1\ncountry=")
	output.WriteString(normalizeWiFiCountry(config.Country))
	output.WriteByte('\n')
	for _, network := range config.Networks {
		if network.SSID == "" {
			continue
		}
		output.WriteString("\nnetwork={\n    ssid=")
		output.WriteString(hex.EncodeToString([]byte(network.SSID)))
		output.WriteByte('\n')
		if network.PSK == "" {
			output.WriteString("    key_mgmt=NONE\n")
		} else if len(network.PSK) == 64 {
			if _, err := hex.DecodeString(network.PSK); err == nil {
				output.WriteString("    psk=" + network.PSK + "\n")
			} else {
				output.WriteString("    psk=" + strconv.Quote(network.PSK) + "\n")
			}
		} else {
			output.WriteString("    psk=" + strconv.Quote(network.PSK) + "\n")
		}
		if network.ScanSSID {
			output.WriteString("    scan_ssid=1\n")
		} else {
			output.WriteString("    scan_ssid=0\n")
		}
		if network.Priority > 0 {
			fmt.Fprintf(&output, "    priority=%d\n", network.Priority)
		}
		if network.Disabled {
			output.WriteString("    disabled=1\n")
		}
		output.WriteString("}\n")
	}
	return output.String()
}

func saveWiFiConfig(path string, config wiFiConfig) error {
	return atomicWriteFile(path, []byte(renderWiFiConfig(config)), 0o600)
}

type fileSnapshot struct {
	path    string
	data    []byte
	mode    os.FileMode
	existed bool
}

func captureFileSnapshot(path string) (fileSnapshot, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileSnapshot{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{path: path, data: data, mode: info.Mode().Perm(), existed: true}, nil
}

func restoreFileSnapshot(snapshot fileSnapshot) error {
	if snapshot.existed {
		return atomicWriteFile(snapshot.path, snapshot.data, snapshot.mode)
	}
	if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func restoreWiFiPersistence(wifiSnapshot, proxySnapshot fileSnapshot) error {
	wifiErr := restoreFileSnapshot(wifiSnapshot)
	proxyErr := restoreFileSnapshot(proxySnapshot)
	if wifiErr != nil {
		wifiErr = fmt.Errorf("restore Wi-Fi config: %w", wifiErr)
	}
	if proxyErr != nil {
		proxyErr = fmt.Errorf("restore Wi-Fi proxy config: %w", proxyErr)
	}
	return errors.Join(wifiErr, proxyErr)
}

func normalizeWiFiPriorities(config *wiFiConfig) {
	next := 1
	for index := range config.Networks {
		if config.Networks[index].Priority <= 0 {
			config.Networks[index].Priority = next
		}
		if config.Networks[index].Priority >= next {
			next = config.Networks[index].Priority + 1
		}
	}
}

func findWiFiNetwork(config wiFiConfig, ssid string) int {
	for index, network := range config.Networks {
		if network.SSID == ssid {
			return index
		}
	}
	return -1
}

func upsertWiFiNetwork(config *wiFiConfig, network wiFiNetwork) {
	index := findWiFiNetwork(*config, network.SSID)
	if index >= 0 {
		if network.Priority <= 0 {
			network.Priority = config.Networks[index].Priority
		}
		config.Networks[index] = network
	} else {
		config.Networks = append(config.Networks, network)
	}
	normalizeWiFiPriorities(config)
}

func promoteWiFiNetwork(config *wiFiConfig, ssid string) {
	index := findWiFiNetwork(*config, ssid)
	if index < 0 {
		return
	}
	target := config.Networks[index]
	config.Networks = append(config.Networks[:index], config.Networks[index+1:]...)
	config.Networks = append(config.Networks, target)
	for index := range config.Networks {
		config.Networks[index].Priority = index + 1
	}
}

func (s *Server) queryWiFiStatus() map[string]any {
	return s.queryWiFiStatusContext(context.Background())
}

func (s *Server) queryWiFiStatusContext(ctx context.Context) map[string]any {
	status := map[string]any{"connected": false, "ssid": "", "ip_address": "", "state": "DISCONNECTED", "detail": ""}
	if commandExists("wpa_cli") {
		result := runCommandContext(ctx, 5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "status")
		if result.ExitCode == 0 {
			detail := strings.TrimRight(string(result.Output), "\r\n")
			status["detail"] = detail
			values := keyValueLines(detail)
			status["state"], status["ssid"], status["ip_address"] = values["wpa_state"], values["ssid"], values["ip_address"]
			status["connected"] = values["wpa_state"] == "COMPLETED" && values["ssid"] != ""
			if status["connected"] == true && status["ip_address"] == "" {
				status["ip_address"] = interfaceIPv4Context(ctx, s.options.WiFiInterface)
			}
			if values["wpa_state"] != "" || values["ssid"] != "" {
				return status
			}
		}
	}
	if commandExists("iw") {
		result := runCommandContext(ctx, 5*time.Second, nil, nil, "iw", "dev", s.options.WiFiInterface, "link")
		detail := strings.TrimRight(string(result.Output), "\r\n")
		status["detail"] = detail
		for _, line := range strings.Split(detail, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "SSID:") {
				status["ssid"] = strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
				status["connected"], status["state"] = true, "COMPLETED"
				status["ip_address"] = interfaceIPv4Context(ctx, s.options.WiFiInterface)
			}
		}
	}
	return status
}

func interfaceIPv4(interfaceName string) string {
	return interfaceIPv4Context(context.Background(), interfaceName)
}

func interfaceIPv4Context(ctx context.Context, interfaceName string) string {
	if !commandExists("ifconfig") {
		return ""
	}
	result := runCommandContext(ctx, 5*time.Second, nil, nil, "ifconfig", interfaceName)
	if result.ExitCode != 0 {
		return ""
	}
	fields := strings.Fields(string(result.Output))
	for index, field := range fields {
		candidates := []string{}
		if field == "inet" && index+1 < len(fields) {
			candidates = append(candidates, fields[index+1])
		}
		if strings.HasPrefix(field, "addr:") {
			candidates = append(candidates, strings.TrimPrefix(field, "addr:"))
		}
		for _, candidate := range candidates {
			candidate = strings.Trim(candidate, "[](),")
			if ip := net.ParseIP(candidate); ip != nil && ip.To4() != nil {
				return ip.To4().String()
			}
		}
	}
	return ""
}

func keyValueLines(text string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			result[key] = value
		}
	}
	return result
}

func parseWiFiScanOutput(text string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		name := ""
		if strings.HasPrefix(line, "SSID:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
		}
		if position := strings.Index(line, `ESSID:"`); position >= 0 {
			value := line[position+len(`ESSID:"`):]
			if end := strings.IndexByte(value, '"'); end >= 0 {
				name = value[:end]
			}
		}
		if name != "" && !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	return result
}

func (s *Server) handleWiFiScan(w http.ResponseWriter, _ *http.Request) {
	var output strings.Builder
	result := commandResult{ExitCode: 127}
	if s.options.WiFiBackend == "systemd-networkd" {
		if commandExists("ip") {
			up := runCommand(10*time.Second, nil, nil, "ip", "link", "set", "dev", s.options.WiFiInterface, "up")
			if len(up.Output) > 0 {
				fmt.Fprintf(&output, "$ ip link set dev %s up\n%s\n", s.options.WiFiInterface, up.Output)
			}
		}
	} else if commandExists("ifconfig") {
		up := runCommand(10*time.Second, nil, nil, "ifconfig", s.options.WiFiInterface, "up")
		if len(up.Output) > 0 {
			fmt.Fprintf(&output, "$ ifconfig %s up\n%s\n", s.options.WiFiInterface, up.Output)
		}
	}
	if commandExists("iw") {
		result = runCommand(30*time.Second, nil, nil, "iw", "dev", s.options.WiFiInterface, "scan")
		fmt.Fprintf(&output, "$ iw dev %s scan\n%s", s.options.WiFiInterface, result.Output)
	}
	if result.ExitCode != 0 && commandExists("iwlist") {
		result = runCommand(30*time.Second, nil, nil, "iwlist", s.options.WiFiInterface, "scan")
		fmt.Fprintf(&output, "$ iwlist %s scan\n%s", s.options.WiFiInterface, result.Output)
	}
	if !commandExists("iw") && !commandExists("iwlist") {
		output.WriteString("No supported scan command found (need iw or iwlist).\n")
	}
	text := strings.TrimRight(output.String(), "\r\n")
	writeJSON(w, 200, map[string]any{"ok": result.ExitCode == 0, "exit_code": result.ExitCode, "output": text, "networks": parseWiFiScanOutput(text)})
}

type wifiConnectionRequest struct {
	SSID      string  `json:"ssid"`
	PSK       *string `json:"psk"`
	Country   string  `json:"country"`
	ProxyMode string  `json:"proxy_mode"`
	ProxyURL  *string `json:"proxy_url"`
	NoProxy   *string `json:"no_proxy"`
}

type wifiConnectionJob struct {
	TaskID     string
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time
	Result     map[string]any
}

func (s *Server) handleWiFiConnect(w http.ResponseWriter, r *http.Request) {
	var request wifiConnectionRequest
	if !readJSONBody(w, r, &request) {
		return
	}
	if err := wifiproxy.ValidateSSID(request.SSID); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if err := validateWiFiProxyRequest(request); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if !s.wifiOpMu.TryLock() {
		response := map[string]any{
			"ok": false, "error": "another Wi-Fi configuration operation is already running",
			"status": "running",
		}
		s.wifiMu.Lock()
		if s.wifiJob != nil && s.wifiJob.Status == "running" {
			response["task_id"] = s.wifiJob.TaskID
		}
		s.wifiMu.Unlock()
		writeJSON(w, http.StatusConflict, response)
		return
	}
	s.wifiMu.Lock()
	job := &wifiConnectionJob{
		TaskID:    fmt.Sprintf("wifi-%d", time.Now().UnixNano()),
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	s.wifiJob = job
	s.wifiMu.Unlock()

	go func() {
		defer s.wifiOpMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), wifiConnectionTaskTimeout)
		defer cancel()
		result := s.runWiFiConnection(ctx, request)
		status := "failed"
		if result["ok"] == true {
			status = "succeeded"
		}
		s.wifiMu.Lock()
		if s.wifiJob == job {
			job.Status = status
			job.FinishedAt = time.Now().UTC()
			job.Result = result
		}
		s.wifiMu.Unlock()
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "task_id": job.TaskID, "status": "running",
		"deadline_seconds": int(wifiConnectionTaskTimeout / time.Second),
	})
}

func (s *Server) handleWiFiConnectStatus(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	s.wifiMu.Lock()
	job := s.wifiJob
	if job == nil || (taskID != "" && taskID != job.TaskID) {
		s.wifiMu.Unlock()
		writeJSONError(w, http.StatusNotFound, "Wi-Fi connection task not found")
		return
	}
	response := map[string]any{
		"ok":         true,
		"task_id":    job.TaskID,
		"status":     job.Status,
		"started_at": job.StartedAt,
	}
	if !job.FinishedAt.IsZero() {
		response["finished_at"] = job.FinishedAt
	}
	for key, value := range job.Result {
		response[key] = value
	}
	s.wifiMu.Unlock()
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) runWiFiConnection(ctx context.Context, request wifiConnectionRequest) map[string]any {
	wifiSnapshot, err := captureFileSnapshot(s.options.WiFiConfigPath)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	original, loadErr := loadWiFiConfig(s.options.WiFiConfigPath)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return map[string]any{"ok": false, "error": loadErr.Error()}
	}
	proxySnapshot, err := captureFileSnapshot(s.options.WiFiProxyConfigPath)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	proxyConfig, err := wifiproxy.Load(s.options.WiFiProxyConfigPath)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if err := applyWiFiProxyRequest(&proxyConfig, request); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	attempt := original
	if strings.TrimSpace(request.Country) != "" {
		attempt.Country = normalizeWiFiCountry(request.Country)
	} else {
		attempt.Country = normalizeWiFiCountry(attempt.Country)
	}
	index := findWiFiNetwork(attempt, request.SSID)
	network := wiFiNetwork{SSID: request.SSID, ScanSSID: true}
	if index >= 0 {
		network = attempt.Networks[index]
		network.SSID = request.SSID
	}
	if request.PSK != nil || index < 0 {
		if request.PSK != nil {
			network.PSK = *request.PSK
		}
	}
	upsertWiFiNetwork(&attempt, network)
	promoteWiFiNetwork(&attempt, request.SSID)
	candidate := s.options.WiFiConfigPath + ".candidate"
	if err := saveWiFiConfig(candidate, attempt); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	removeCandidate := true
	defer func() {
		if removeCandidate {
			_ = os.Remove(candidate)
		}
	}()
	candidateCtx, cancelCandidate := context.WithTimeout(ctx, wifiCandidateApplyTimeout)
	apply := s.applyWiFi(candidateCtx, candidate, true)
	cancelCandidate()
	status := s.queryWiFiStatusContext(ctx)
	connected := apply.ExitCode == 0 && status["connected"] == true && status["ssid"] == request.SSID && status["ip_address"] != ""
	responseConfig := original
	persistError := ""
	diskRestoreNeeded := false
	var diskRestoreErr error
	if connected {
		if err := saveWiFiConfig(s.options.WiFiConfigPath, attempt); err != nil {
			persistError = "save Wi-Fi config: " + err.Error()
			connected = false
		} else {
			diskRestoreNeeded = true
			if err := wifiproxy.Save(s.options.WiFiProxyConfigPath, proxyConfig); err != nil {
				persistError = "save Wi-Fi proxy config: " + err.Error()
				connected = false
			} else {
				diskRestoreNeeded = false
				// The running wpa_supplicant retains the path supplied with -c for
				// future reconfigure/save operations. Keep the verified candidate file
				// until the next connection attempt or service restart.
				removeCandidate = false
				responseConfig = attempt
			}
		}
	}
	rollback := commandResult{ExitCode: 0}
	if !connected {
		if diskRestoreNeeded {
			diskRestoreErr = restoreWiFiPersistence(wifiSnapshot, proxySnapshot)
		}
		if len(original.Networks) > 0 {
			rollback = s.applyWiFi(ctx, s.options.WiFiConfigPath, true)
		} else if commandExists("wpa_cli") {
			rollback = runCommandContext(ctx, 5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "disconnect")
		}
		if apply.ExitCode == 0 {
			apply.ExitCode = 1
		}
		status = s.queryWiFiStatusContext(ctx)
	}
	message := "wifi connected and saved"
	if !connected {
		message = "wifi connect failed; config restored"
		if diskRestoreErr != nil {
			message = "wifi connect failed; config recovery incomplete"
		} else if rollback.ExitCode != 0 {
			message = "wifi connect failed; config restored on disk but runtime rollback failed"
		}
	}
	applyValue := map[string]any{"ok": connected, "exit_code": apply.ExitCode, "output": strings.TrimRight(string(apply.Output), "\r\n")}
	if !connected {
		applyValue["error"] = "failed to apply wifi config"
	}
	responseProxyConfig := proxyConfig
	if !connected {
		responseProxyConfig, _ = wifiproxy.Load(s.options.WiFiProxyConfigPath)
	}
	response := map[string]any{"ok": connected, "wifi": responseConfig.publicValue(responseProxyConfig), "wifi_status": status, "message": message, "wifi_apply": applyValue}
	if !connected {
		rollbackValue := map[string]any{
			"ok": rollback.ExitCode == 0 && diskRestoreErr == nil, "exit_code": rollback.ExitCode,
			"disk_restored": diskRestoreErr == nil,
			"timed_out":     rollback.TimedOut, "output": strings.TrimRight(string(rollback.Output), "\r\n"),
		}
		if diskRestoreErr != nil {
			rollbackValue["disk_error"] = diskRestoreErr.Error()
		}
		response["wifi_rollback"] = rollbackValue
	}
	responseErrors := []string{}
	if persistError != "" {
		responseErrors = append(responseErrors, "persist configuration: "+persistError)
	}
	if diskRestoreErr != nil {
		responseErrors = append(responseErrors, "restore persisted config: "+diskRestoreErr.Error())
	}
	if ctx.Err() != nil {
		responseErrors = append(responseErrors, "Wi-Fi connection task exceeded its deadline")
	}
	if len(responseErrors) > 0 {
		response["error"] = strings.Join(responseErrors, "; ")
	}
	return response
}

func validateWiFiProxyRequest(request wifiConnectionRequest) error {
	if err := wifiproxy.ValidateSSID(request.SSID); err != nil {
		return fmt.Errorf("ssid: %w", err)
	}
	mode := wifiproxy.Mode(strings.ToLower(strings.TrimSpace(request.ProxyMode)))
	if mode == "" || mode == wifiproxy.ModeSystem {
		if request.ProxyURL != nil && strings.TrimSpace(*request.ProxyURL) != "" {
			return errors.New("proxy_url is only valid with proxy_mode=proxy")
		}
		if request.NoProxy != nil && strings.TrimSpace(*request.NoProxy) != "" {
			return errors.New("no_proxy is only valid with proxy_mode=proxy")
		}
		return nil
	}
	if mode == wifiproxy.ModeDirect {
		if request.ProxyURL != nil && strings.TrimSpace(*request.ProxyURL) != "" {
			return errors.New("proxy_url is only valid with proxy_mode=proxy")
		}
		if request.NoProxy != nil && strings.TrimSpace(*request.NoProxy) != "" {
			return errors.New("no_proxy is not used with proxy_mode=direct")
		}
		return nil
	}
	if mode != wifiproxy.ModeProxy {
		return fmt.Errorf("unsupported proxy_mode %q", request.ProxyMode)
	}
	noProxy := ""
	if request.NoProxy != nil {
		noProxy = *request.NoProxy
	}
	if _, err := wifiproxy.NormalizeNoProxy(noProxy); err != nil {
		return fmt.Errorf("no_proxy: %w", err)
	}
	if request.ProxyURL == nil || strings.TrimSpace(*request.ProxyURL) == "" {
		return nil
	}
	if _, err := wifiproxy.NormalizeNetwork(wifiproxy.Network{Mode: mode, ProxyURL: *request.ProxyURL, NoProxy: noProxy}); err != nil {
		return fmt.Errorf("proxy_url: %w", err)
	}
	return nil
}

func applyWiFiProxyRequest(config *wifiproxy.Config, request wifiConnectionRequest) error {
	if err := wifiproxy.ValidateSSID(request.SSID); err != nil {
		return fmt.Errorf("ssid: %w", err)
	}
	mode := wifiproxy.Mode(strings.ToLower(strings.TrimSpace(request.ProxyMode)))
	if mode == "" {
		return nil
	}
	switch mode {
	case wifiproxy.ModeSystem:
		delete(config.Networks, request.SSID)
	case wifiproxy.ModeDirect:
		config.Networks[request.SSID] = wifiproxy.Network{Mode: mode}
	case wifiproxy.ModeProxy:
		existing, hasExisting := config.Networks[request.SSID]
		network := wifiproxy.Network{Mode: mode}
		if request.ProxyURL == nil || strings.TrimSpace(*request.ProxyURL) == "" {
			if hasExisting && existing.Mode == wifiproxy.ModeProxy {
				network.ProxyURL = existing.ProxyURL
			} else {
				return errors.New("proxy_url is required when proxy_mode is proxy")
			}
		} else {
			network.ProxyURL = strings.TrimSpace(*request.ProxyURL)
		}
		if request.NoProxy != nil {
			network.NoProxy = *request.NoProxy
			network.NoProxySet = true
		} else if hasExisting && existing.Mode == wifiproxy.ModeProxy {
			network.NoProxy = existing.NoProxy
			network.NoProxySet = existing.NoProxySet
		}
		network, err := wifiproxy.NormalizeNetwork(network)
		if err != nil {
			return err
		}
		config.Networks[request.SSID] = network
	default:
		return fmt.Errorf("unsupported proxy_mode %q", request.ProxyMode)
	}
	return nil
}

func (s *Server) handleWiFiForget(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SSID string `json:"ssid"`
	}
	request.SSID = r.URL.Query().Get("ssid")
	if request.SSID == "" && r.Body != nil {
		if !readJSONBody(w, r, &request) {
			return
		}
	}
	if err := wifiproxy.ValidateSSID(request.SSID); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	if !s.wifiOpMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error": "another Wi-Fi configuration operation is already running",
		})
		return
	}
	defer s.wifiOpMu.Unlock()
	config, err := loadWiFiConfig(s.options.WiFiConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, 404, "wifi network not found")
			return
		}
		writeJSONError(w, 500, err.Error())
		return
	}
	index := findWiFiNetwork(config, request.SSID)
	if index < 0 {
		writeJSONError(w, 404, "wifi network not found")
		return
	}
	config.Networks = append(config.Networks[:index], config.Networks[index+1:]...)
	normalizeWiFiPriorities(&config)
	originalWiFiData, err := readFileLimited(s.options.WiFiConfigPath, maxAgentConfigSize)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	proxyConfig, err := wifiproxy.Load(s.options.WiFiProxyConfigPath)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	delete(proxyConfig.Networks, request.SSID)
	if err := saveWiFiConfig(s.options.WiFiConfigPath, config); err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	if err := wifiproxy.Save(s.options.WiFiProxyConfigPath, proxyConfig); err != nil {
		if rollbackErr := atomicWriteFile(s.options.WiFiConfigPath, originalWiFiData, 0o600); rollbackErr != nil {
			writeJSONError(w, 500, fmt.Sprintf("remove Wi-Fi proxy: %v; restore Wi-Fi config: %v", err, rollbackErr))
			return
		}
		writeJSONError(w, 500, err.Error())
		return
	}
	apply := commandResult{ExitCode: 0}
	if len(config.Networks) > 0 {
		apply = s.applyWiFi(r.Context(), s.options.WiFiConfigPath, false)
	} else if commandExists("wpa_cli") {
		apply = runCommand(5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "disconnect")
	}
	applied := apply.ExitCode == 0
	message := "wifi network forgotten"
	if !applied {
		message = "wifi network forgotten but failed to apply runtime changes"
	}
	applyValue := map[string]any{"ok": applied, "exit_code": apply.ExitCode, "output": strings.TrimRight(string(apply.Output), "\r\n")}
	if !applied {
		applyValue["error"] = "failed to apply wifi config"
	}
	writeJSON(w, 200, map[string]any{"ok": applied, "wifi": config.publicValue(proxyConfig), "wifi_status": s.queryWiFiStatus(), "message": message, "wifi_apply": applyValue})
}

func (s *Server) applyWiFi(ctx context.Context, configPath string, force bool) commandResult {
	var output strings.Builder
	associated := false
	result := commandResult{ExitCode: 0}
	if s.options.WiFiBackend == "systemd-networkd" {
		if commandExists("ip") {
			up := runCommandContext(ctx, 10*time.Second, nil, nil, "ip", "link", "set", "dev", s.options.WiFiInterface, "up")
			fmt.Fprintf(&output, "$ ip link set dev %s up\n%s", s.options.WiFiInterface, up.Output)
			if up.ExitCode != 0 {
				result.ExitCode = up.ExitCode
			}
		} else {
			result.ExitCode = 127
			output.WriteString("ip command is required by the systemd-networkd Wi-Fi backend.\n")
		}
	} else if commandExists("ifconfig") {
		up := runCommandContext(ctx, 10*time.Second, nil, nil, "ifconfig", s.options.WiFiInterface, "up")
		fmt.Fprintf(&output, "$ ifconfig %s up\n%s", s.options.WiFiInterface, up.Output)
		if up.ExitCode != 0 {
			result.ExitCode = up.ExitCode
		}
	}
	if !force && commandExists("wpa_cli") {
		ping := runCommandContext(ctx, 5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "ping")
		if ping.ExitCode == 0 && strings.Contains(string(ping.Output), "PONG") {
			reconfigure := runCommandContext(ctx, 5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "reconfigure")
			if reconfigure.ExitCode == 0 && !strings.Contains(string(reconfigure.Output), "FAIL") {
				associated = s.waitForWiFiState(ctx, &output, 8)
			}
		}
	}
	if !associated && s.options.WiFiBackend == "systemd-networkd" {
		unit := "wpa_supplicant@" + s.options.WiFiInterface + ".service"
		restart := runCommandContext(ctx, 10*time.Second, nil, nil, "systemctl", "restart", unit)
		fmt.Fprintf(&output, "$ systemctl restart %s\n%s", unit, restart.Output)
		if restart.ExitCode == 0 {
			associated = s.waitForWiFiState(ctx, &output, 10)
		} else {
			result.ExitCode = restart.ExitCode
		}
	} else if !associated && commandExists("wpa_supplicant") {
		if commandExists("killall") {
			_ = runCommandContext(ctx, 5*time.Second, nil, nil, "killall", "wpa_supplicant")
		}
		if !waitContext(ctx, time.Second) {
			return commandResult{Output: []byte(output.String()), ExitCode: -1, TimedOut: true}
		}
		start := runCommandContext(ctx, 10*time.Second, nil, nil, "wpa_supplicant", "-B", "-i", s.options.WiFiInterface, "-c", configPath)
		fmt.Fprintf(&output, "$ wpa_supplicant -B -i %s -c %s\n%s", s.options.WiFiInterface, configPath, start.Output)
		associated = s.waitForWiFiState(ctx, &output, 10)
	}
	dhcpOK := false
	if associated && s.options.WiFiBackend == "systemd-networkd" {
		reconfigure := runCommandContext(ctx, 10*time.Second, nil, nil, "networkctl", "reconfigure", s.options.WiFiInterface)
		fmt.Fprintf(&output, "$ networkctl reconfigure %s\n%s", s.options.WiFiInterface, reconfigure.Output)
		dhcpOK = reconfigure.ExitCode == 0 && s.waitForWiFiIP(ctx, &output, 20)
		if !dhcpOK {
			result.ExitCode = reconfigure.ExitCode
			if result.ExitCode == 0 {
				result.ExitCode = 1
			}
		}
	} else if associated && commandExists("dhcpcd") {
		dhcp := runCommandContext(ctx, 10*time.Second, nil, nil, "dhcpcd", "-n", s.options.WiFiInterface)
		dhcpOK = dhcp.ExitCode == 0 && s.waitForWiFiIP(ctx, &output, 15)
		if !dhcpOK {
			result.ExitCode = 1
		}
	} else if associated && commandExists("dhclient") {
		dhcp := runCommandContext(ctx, 30*time.Second, nil, nil, "dhclient", s.options.WiFiInterface)
		dhcpOK = dhcp.ExitCode == 0 && s.waitForWiFiIP(ctx, &output, 15)
		if !dhcpOK {
			result.ExitCode = 1
		}
	} else if associated {
		result.ExitCode = 127
		output.WriteString("No supported DHCP client found (need dhcpcd or dhclient).\n")
	} else {
		result.ExitCode = 1
		output.WriteString("wpa_supplicant never reached COMPLETED; skipping DHCP.\n")
	}
	if associated && dhcpOK {
		result.ExitCode = 0
	}
	if ctx.Err() != nil {
		result.ExitCode = -1
		result.TimedOut = true
	}
	result.Output = []byte(output.String())
	return result
}

func (s *Server) waitForWiFiState(ctx context.Context, output *strings.Builder, seconds int) bool {
	for index := 0; index < seconds; index++ {
		if !waitContext(ctx, time.Second) {
			return false
		}
		status := runCommandContext(ctx, 5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "status")
		if status.ExitCode == 0 && strings.Contains(string(status.Output), "wpa_state=COMPLETED") {
			fmt.Fprintf(output, "$ wpa_cli status -> COMPLETED after %ds\n", index+1)
			return true
		}
	}
	return false
}
func (s *Server) waitForWiFiIP(ctx context.Context, output *strings.Builder, seconds int) bool {
	for index := 0; index < seconds; index++ {
		status := runCommandContext(ctx, 5*time.Second, nil, nil, "wpa_cli", "-i", s.options.WiFiInterface, "status")
		if keyValueLines(string(status.Output))["ip_address"] != "" || interfaceIPv4Context(ctx, s.options.WiFiInterface) != "" {
			return true
		}
		if !waitContext(ctx, time.Second) {
			return false
		}
	}
	fmt.Fprintf(output, "$ no IPv4 address obtained within %ds\n", seconds)
	return false
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
