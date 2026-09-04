package configweb

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
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

func (c wiFiConfig) publicValue() map[string]any {
	networks := make([]map[string]any, 0, len(c.Networks))
	for _, network := range c.Networks {
		networks = append(networks, map[string]any{
			"ssid": network.SSID, "has_psk": network.PSK != "", "priority": network.Priority,
			"scan_ssid": network.ScanSSID, "disabled": network.Disabled,
		})
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
	if commandExists("ifconfig") {
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
	SSID    string  `json:"ssid"`
	PSK     *string `json:"psk"`
	Country string  `json:"country"`
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
	if strings.TrimSpace(request.SSID) == "" {
		writeJSONError(w, 400, "ssid is required")
		return
	}
	s.wifiMu.Lock()
	if s.wifiJob != nil && s.wifiJob.Status == "running" {
		taskID := s.wifiJob.TaskID
		s.wifiMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "error": "a Wi-Fi connection task is already running",
			"task_id": taskID, "status": "running",
		})
		return
	}
	job := &wifiConnectionJob{
		TaskID:    fmt.Sprintf("wifi-%d", time.Now().UnixNano()),
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	s.wifiJob = job
	s.wifiMu.Unlock()

	go func() {
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
		"ok": true, "task_id": job.TaskID, "status": job.Status,
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
	originalData, originalErr := readFileLimited(s.options.WiFiConfigPath, maxAgentConfigSize)
	original, loadErr := loadWiFiConfig(s.options.WiFiConfigPath)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return map[string]any{"ok": false, "error": loadErr.Error()}
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
	if connected {
		if err := saveWiFiConfig(s.options.WiFiConfigPath, attempt); err != nil {
			persistError = err.Error()
			connected = false
		} else {
			// The running wpa_supplicant retains the path supplied with -c for
			// future reconfigure/save operations. Keep the verified candidate file
			// until the next connection attempt or service restart.
			removeCandidate = false
			responseConfig = attempt
		}
	}
	rollback := commandResult{ExitCode: 0}
	if !connected {
		if originalErr == nil {
			_ = atomicWriteFile(s.options.WiFiConfigPath, originalData, 0o600)
		} else {
			_ = os.Remove(s.options.WiFiConfigPath)
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
		if rollback.ExitCode != 0 {
			message = "wifi connect failed; config restored on disk but runtime rollback failed"
		}
	}
	applyValue := map[string]any{"ok": connected, "exit_code": apply.ExitCode, "output": strings.TrimRight(string(apply.Output), "\r\n")}
	if !connected {
		applyValue["error"] = "failed to apply wifi config"
	}
	response := map[string]any{"ok": connected, "wifi": responseConfig.publicValue(), "wifi_status": status, "message": message, "wifi_apply": applyValue}
	if persistError != "" {
		response["error"] = "persist Wi-Fi config: " + persistError
	}
	if !connected {
		response["wifi_rollback"] = map[string]any{
			"ok": rollback.ExitCode == 0, "exit_code": rollback.ExitCode,
			"timed_out": rollback.TimedOut, "output": strings.TrimRight(string(rollback.Output), "\r\n"),
		}
	}
	if ctx.Err() != nil {
		response["error"] = "Wi-Fi connection task exceeded its deadline"
	}
	return response
}

func (s *Server) handleWiFiForget(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SSID string `json:"ssid"`
	}
	request.SSID = strings.TrimSpace(r.URL.Query().Get("ssid"))
	if request.SSID == "" && r.Body != nil {
		if !readJSONBody(w, r, &request) {
			return
		}
	}
	if strings.TrimSpace(request.SSID) == "" {
		writeJSONError(w, 400, "ssid is required")
		return
	}
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
	if err := saveWiFiConfig(s.options.WiFiConfigPath, config); err != nil {
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
	writeJSON(w, 200, map[string]any{"ok": applied, "wifi": config.publicValue(), "wifi_status": s.queryWiFiStatus(), "message": message, "wifi_apply": applyValue})
}

func (s *Server) applyWiFi(ctx context.Context, configPath string, force bool) commandResult {
	var output strings.Builder
	associated := false
	result := commandResult{ExitCode: 0}
	if commandExists("ifconfig") {
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
	if !associated && commandExists("wpa_supplicant") {
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
	if associated && commandExists("dhcpcd") {
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
