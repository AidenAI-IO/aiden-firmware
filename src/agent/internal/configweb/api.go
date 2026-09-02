package configweb

import (
	"fmt"
	"net/http"
	"strings"
)

// Config Web deliberately uses the same /api namespace as the Agent runtime.
// Resource nouns provide the boundary; the API does not add a version prefix.
// Legacy aliases are kept only where they do not collide with new semantics.
const apiPrefix = "/api"

type apiEndpoint int

const (
	apiUnknown apiEndpoint = iota
	apiDeviceSnapshot
	apiConfigGet
	apiConfigSchema
	apiConfigUpdate
	apiConfigLocale
	apiConfigTest
	apiWiFiScan
	apiWiFiConnect
	apiWiFiForget
	apiSystemEnvironmentGet
	apiSystemEnvironmentPut
	apiDeviceStatus
	apiAgentLogs
	apiOTAStatus
	apiOTAUpdate
	apiDeviceReboot
	apiUSBReenumerate
	apiSupportArchive
	apiLLMLogs
	apiLLMLogExport
	apiLLMLogImport
)

type routeVariant struct{ method, path string }

type apiRoute struct {
	endpoint  apiEndpoint
	canonical routeVariant
	legacy    []routeVariant
}

var apiRoutes = []apiRoute{
	{apiDeviceSnapshot, routeVariant{http.MethodGet, apiPrefix + "/device/snapshot"}, nil},
	{apiConfigGet, routeVariant{http.MethodGet, apiPrefix + "/config"}, nil},
	{apiConfigSchema, routeVariant{http.MethodGet, apiPrefix + "/config/schema"}, nil},
	{apiConfigUpdate, routeVariant{http.MethodPatch, apiPrefix + "/config"}, []routeVariant{{http.MethodPost, "/api/config"}}},
	{apiConfigLocale, routeVariant{http.MethodPut, apiPrefix + "/config/locale"}, nil},
	{apiConfigTest, routeVariant{http.MethodPost, apiPrefix + "/config/test"}, nil},
	{apiWiFiScan, routeVariant{http.MethodPost, apiPrefix + "/network/wifi/scan"}, []routeVariant{{http.MethodPost, "/api/wifi/scan"}}},
	{apiWiFiConnect, routeVariant{http.MethodPut, apiPrefix + "/network/wifi/connection"}, []routeVariant{{http.MethodPost, "/api/wifi/connect"}}},
	{apiWiFiForget, routeVariant{http.MethodDelete, apiPrefix + "/network/wifi/connection"}, []routeVariant{{http.MethodPost, "/api/wifi/forget"}}},
	{apiSystemEnvironmentGet, routeVariant{http.MethodGet, apiPrefix + "/system/environment"}, nil},
	{apiSystemEnvironmentPut, routeVariant{http.MethodPut, apiPrefix + "/system/environment"}, []routeVariant{{http.MethodPost, "/api/system/env"}}},
	{apiDeviceStatus, routeVariant{http.MethodGet, apiPrefix + "/device/status"}, []routeVariant{{http.MethodGet, "/api/agent/status"}}},
	{apiAgentLogs, routeVariant{http.MethodGet, apiPrefix + "/logs/agent"}, []routeVariant{{http.MethodGet, "/api/agent/logs"}}},
	{apiOTAStatus, routeVariant{http.MethodGet, apiPrefix + "/ota/status"}, []routeVariant{{http.MethodGet, "/api/ota/logs"}}},
	{apiOTAUpdate, routeVariant{http.MethodPost, apiPrefix + "/ota/updates"}, []routeVariant{{http.MethodPost, "/api/ota/update"}, {http.MethodPost, "/api/ota/check-now"}}},
	{apiDeviceReboot, routeVariant{http.MethodPost, apiPrefix + "/device/reboot"}, []routeVariant{{http.MethodPost, "/api/reboot"}}},
	{apiUSBReenumerate, routeVariant{http.MethodPost, apiPrefix + "/device/usb/reenumerate"}, []routeVariant{{http.MethodPost, "/api/hid/usb-reenumerate"}}},
	{apiSupportArchive, routeVariant{http.MethodGet, apiPrefix + "/logs/support"}, []routeVariant{{http.MethodGet, "/api/logs/export"}}},
	{apiLLMLogs, routeVariant{http.MethodGet, apiPrefix + "/logs/llm"}, []routeVariant{{http.MethodGet, "/api/llm-logs"}}},
}

type apiMatch struct {
	endpoint      apiEndpoint
	canonicalPath string
	legacy        bool
	suffix        string
}

func (s *Server) APIHandler() http.Handler { return http.HandlerFunc(s.serveAPI) }

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
	const canonicalLogPrefix = apiPrefix + "/logs/llm/"
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
		method, prefix string
		endpoint       apiEndpoint
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
		s.handleGetDeviceSnapshot(w, r)
	case apiConfigGet:
		s.handleGetConfig(w, r)
	case apiConfigSchema:
		s.handleConfigMeta(w, r)
	case apiConfigUpdate:
		s.handlePostConfig(w, r)
	case apiConfigLocale:
		s.handlePutLocale(w, r)
	case apiConfigTest:
		s.handleConfigTest(w, r)
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
	case apiDeviceStatus:
		s.handleAgentStatus(w, r)
	case apiAgentLogs:
		s.handleAgentLogs(w, r)
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
	}
}
