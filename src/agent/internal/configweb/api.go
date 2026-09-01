package configweb

import (
	"fmt"
	"net/http"
	"strings"
)

const apiV1Prefix = "/api/v1"

type apiEndpoint int

const (
	apiUnknown apiEndpoint = iota
	apiDeviceSnapshot
	apiConfigSchema
	apiConfigUpdate
	apiConfigLocale
	apiConfigTest
	apiSTTTestStart
	apiSTTTestStop
	apiModels
	apiWiFiScan
	apiWiFiConnect
	apiWiFiForget
	apiSystemEnvironmentGet
	apiSystemEnvironmentPut
	apiAgentStatus
	apiAgentLogs
	apiStorageStatus
	apiStorageFormat
	apiStorageEject
	apiOTAStatus
	apiOTAUpdate
	apiDeviceReboot
	apiUSBReenumerate
	apiSupportArchive
	apiLLMLogs
	apiLLMLogExport
	apiLLMLogImport
)

type routeVariant struct {
	method string
	path   string
}

type apiRoute struct {
	endpoint  apiEndpoint
	canonical routeVariant
	legacy    []routeVariant
}

var apiRoutes = []apiRoute{
	{apiDeviceSnapshot, routeVariant{http.MethodGet, apiV1Prefix + "/device/snapshot"}, []routeVariant{{http.MethodGet, "/api/config"}}},
	{apiConfigSchema, routeVariant{http.MethodGet, apiV1Prefix + "/config/schema"}, []routeVariant{{http.MethodGet, "/api/config-meta"}, {http.MethodGet, "/api/config/meta"}}},
	{apiConfigUpdate, routeVariant{http.MethodPatch, apiV1Prefix + "/config"}, []routeVariant{{http.MethodPost, "/api/config"}}},
	{apiConfigLocale, routeVariant{http.MethodPut, apiV1Prefix + "/config/locale"}, []routeVariant{{http.MethodPut, "/api/config/locale"}}},
	{apiConfigTest, routeVariant{http.MethodPost, apiV1Prefix + "/config/tests"}, []routeVariant{{http.MethodPost, "/api/config/test"}}},
	{apiSTTTestStart, routeVariant{http.MethodPost, apiV1Prefix + "/config/tests/stt-session"}, []routeVariant{{http.MethodPost, "/api/config/test/stt/start"}}},
	{apiSTTTestStop, routeVariant{http.MethodDelete, apiV1Prefix + "/config/tests/stt-session"}, []routeVariant{{http.MethodPost, "/api/config/test/stt/stop"}}},
	{apiModels, routeVariant{http.MethodGet, apiV1Prefix + "/models"}, []routeVariant{{http.MethodGet, "/api/models"}}},
	{apiWiFiScan, routeVariant{http.MethodPost, apiV1Prefix + "/wifi/scans"}, []routeVariant{{http.MethodPost, "/api/wifi/scan"}}},
	{apiWiFiConnect, routeVariant{http.MethodPut, apiV1Prefix + "/wifi/connection"}, []routeVariant{{http.MethodPost, "/api/wifi/connect"}}},
	{apiWiFiForget, routeVariant{http.MethodDelete, apiV1Prefix + "/wifi/connection"}, []routeVariant{{http.MethodPost, "/api/wifi/forget"}}},
	{apiSystemEnvironmentGet, routeVariant{http.MethodGet, apiV1Prefix + "/system/environment"}, nil},
	{apiSystemEnvironmentPut, routeVariant{http.MethodPut, apiV1Prefix + "/system/environment"}, []routeVariant{{http.MethodPost, "/api/system/env"}}},
	{apiAgentStatus, routeVariant{http.MethodGet, apiV1Prefix + "/agent/status"}, []routeVariant{{http.MethodGet, "/api/agent/status"}}},
	{apiAgentLogs, routeVariant{http.MethodGet, apiV1Prefix + "/agent/logs"}, []routeVariant{{http.MethodGet, "/api/agent/logs"}}},
	{apiStorageStatus, routeVariant{http.MethodGet, apiV1Prefix + "/storage/status"}, []routeVariant{{http.MethodGet, "/api/storage/status"}}},
	{apiStorageFormat, routeVariant{http.MethodPost, apiV1Prefix + "/storage/format"}, []routeVariant{{http.MethodPost, "/api/storage/format"}}},
	{apiStorageEject, routeVariant{http.MethodPost, apiV1Prefix + "/storage/eject"}, []routeVariant{{http.MethodPost, "/api/storage/eject"}}},
	{apiOTAStatus, routeVariant{http.MethodGet, apiV1Prefix + "/ota/status"}, []routeVariant{{http.MethodGet, "/api/ota/logs"}}},
	{apiOTAUpdate, routeVariant{http.MethodPost, apiV1Prefix + "/ota/updates"}, []routeVariant{{http.MethodPost, "/api/ota/update"}, {http.MethodPost, "/api/ota/check-now"}}},
	{apiDeviceReboot, routeVariant{http.MethodPost, apiV1Prefix + "/device/reboot"}, []routeVariant{{http.MethodPost, "/api/reboot"}}},
	{apiUSBReenumerate, routeVariant{http.MethodPost, apiV1Prefix + "/hid/usb-reenumeration"}, []routeVariant{{http.MethodPost, "/api/hid/usb-reenumerate"}}},
	{apiSupportArchive, routeVariant{http.MethodGet, apiV1Prefix + "/support/archive"}, []routeVariant{{http.MethodGet, "/api/logs/export"}}},
	{apiLLMLogs, routeVariant{http.MethodGet, apiV1Prefix + "/logs/llm"}, []routeVariant{{http.MethodGet, "/api/llm-logs"}}},
}

type apiMatch struct {
	endpoint      apiEndpoint
	canonicalPath string
	legacy        bool
	suffix        string
}

// APIHandler returns the device-management API without the Config Web static
// pages. It can be mounted independently when the portal is replaced by
// another client.
func (s *Server) APIHandler() http.Handler {
	return http.HandlerFunc(s.serveAPI)
}

func matchAPIRequest(r *http.Request) apiMatch {
	for _, route := range apiRoutes {
		if r.Method == route.canonical.method && r.URL.Path == route.canonical.path {
			return apiMatch{endpoint: route.endpoint, canonicalPath: route.canonical.path}
		}
		for _, legacy := range route.legacy {
			if r.Method == legacy.method && r.URL.Path == legacy.path {
				return apiMatch{endpoint: route.endpoint, canonicalPath: route.canonical.path, legacy: true}
			}
		}
	}

	escapedPath := r.URL.EscapedPath()
	const canonicalLogPrefix = apiV1Prefix + "/logs/llm/"
	if strings.HasPrefix(escapedPath, canonicalLogPrefix) {
		suffix := strings.TrimPrefix(escapedPath, canonicalLogPrefix)
		switch r.Method {
		case http.MethodGet:
			return apiMatch{endpoint: apiLLMLogExport, canonicalPath: canonicalLogPrefix + suffix, suffix: suffix}
		case http.MethodPut:
			return apiMatch{endpoint: apiLLMLogImport, canonicalPath: canonicalLogPrefix + suffix, suffix: suffix}
		}
	}
	for _, legacy := range []struct {
		method   string
		prefix   string
		endpoint apiEndpoint
	}{
		{http.MethodGet, "/api/llm-logs/export/", apiLLMLogExport},
		{http.MethodPost, "/api/llm-logs/import/", apiLLMLogImport},
	} {
		if r.Method == legacy.method && strings.HasPrefix(escapedPath, legacy.prefix) {
			suffix := strings.TrimPrefix(escapedPath, legacy.prefix)
			return apiMatch{endpoint: legacy.endpoint, canonicalPath: canonicalLogPrefix + suffix, legacy: true, suffix: suffix}
		}
	}
	return apiMatch{}
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request) {
	s.startDeferredRestartIfIdle()
	match := matchAPIRequest(r)
	if match.endpoint == apiUnknown {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("X-Aiden-API-Version", "1")
	if match.legacy {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"successor-version\"", match.canonicalPath))
	}

	switch match.endpoint {
	case apiDeviceSnapshot:
		s.handleGetConfig(w, r)
	case apiConfigSchema:
		s.handleConfigMeta(w, r)
	case apiConfigUpdate:
		s.handlePostConfig(w, r)
	case apiConfigLocale:
		s.handlePutLocale(w, r)
	case apiConfigTest:
		s.handleConfigTest(w, r)
	case apiSTTTestStart:
		s.handleSTTTestStart(w, r)
	case apiSTTTestStop:
		s.handleSTTTestStop(w, r)
	case apiModels:
		target := "/api/models"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		s.proxyAgent(w, r, target)
	case apiWiFiScan:
		s.handleWiFiScan(w, r)
	case apiWiFiConnect:
		s.handleWiFiConnect(w, r)
	case apiWiFiForget:
		s.handleWiFiForget(w, r)
	case apiSystemEnvironmentGet:
		s.handleGetSystemEnv(w, r)
	case apiSystemEnvironmentPut:
		s.handleSystemEnv(w, r)
	case apiAgentStatus:
		s.handleAgentStatus(w, r)
	case apiAgentLogs:
		s.handleAgentLogs(w, r)
	case apiStorageStatus:
		s.handleStorageStatus(w, r)
	case apiStorageFormat:
		s.proxyAgent(w, r, "/api/storage/format")
	case apiStorageEject:
		s.proxyAgent(w, r, "/api/storage/eject")
	case apiOTAStatus:
		s.handleOTALogs(w, r)
	case apiOTAUpdate:
		s.handleOTAUpdate(w, r)
	case apiDeviceReboot:
		s.handleReboot(w, r)
	case apiUSBReenumerate:
		s.handleUSBReenumerate(w, r)
	case apiSupportArchive:
		s.handleSupportLogsExport(w, r)
	case apiLLMLogs:
		s.handleLLMLogs(w, r)
	case apiLLMLogExport:
		s.handleLLMLogExportName(w, match.suffix)
	case apiLLMLogImport:
		s.handleLLMLogImportName(w, r, match.suffix)
	default:
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}
