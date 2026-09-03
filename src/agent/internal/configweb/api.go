package configweb

import (
	"net/http"
	"strings"
)

// Config Web deliberately uses the same /api namespace as the Agent runtime.
// Resource nouns provide the boundary; the API does not add a version prefix.
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
	apiSTTTestStart
	apiSTTTestStop
	apiModels
	apiWiFiScan
	apiWiFiConnect
	apiWiFiForget
	apiSystemEnvironmentGet
	apiSystemEnvironmentPut
	apiDeviceStatus
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

type routeVariant struct{ method, path string }

type apiRoute struct {
	endpoint  apiEndpoint
	canonical routeVariant
}

var apiRoutes = []apiRoute{
	{apiDeviceSnapshot, routeVariant{http.MethodGet, apiPrefix + "/device/snapshot"}},
	{apiConfigGet, routeVariant{http.MethodGet, apiPrefix + "/config"}},
	{apiConfigSchema, routeVariant{http.MethodGet, apiPrefix + "/config/schema"}},
	{apiConfigUpdate, routeVariant{http.MethodPatch, apiPrefix + "/config"}},
	{apiConfigLocale, routeVariant{http.MethodPut, apiPrefix + "/config/locale"}},
	{apiConfigTest, routeVariant{http.MethodPost, apiPrefix + "/config/test"}},
	{apiSTTTestStart, routeVariant{http.MethodPost, apiPrefix + "/config/test/stt/start"}},
	{apiSTTTestStop, routeVariant{http.MethodPost, apiPrefix + "/config/test/stt/stop"}},
	{apiModels, routeVariant{http.MethodGet, apiPrefix + "/models"}},
	{apiWiFiScan, routeVariant{http.MethodPost, apiPrefix + "/network/wifi/scan"}},
	{apiWiFiConnect, routeVariant{http.MethodPut, apiPrefix + "/network/wifi/connection"}},
	{apiWiFiForget, routeVariant{http.MethodDelete, apiPrefix + "/network/wifi/connection"}},
	{apiSystemEnvironmentGet, routeVariant{http.MethodGet, apiPrefix + "/system/environment"}},
	{apiSystemEnvironmentPut, routeVariant{http.MethodPut, apiPrefix + "/system/environment"}},
	{apiDeviceStatus, routeVariant{http.MethodGet, apiPrefix + "/device/status"}},
	{apiAgentLogs, routeVariant{http.MethodGet, apiPrefix + "/logs/agent"}},
	{apiStorageStatus, routeVariant{http.MethodGet, apiPrefix + "/storage/status"}},
	{apiStorageFormat, routeVariant{http.MethodPost, apiPrefix + "/storage/format"}},
	{apiStorageEject, routeVariant{http.MethodPost, apiPrefix + "/storage/eject"}},
	{apiOTAStatus, routeVariant{http.MethodGet, apiPrefix + "/ota/status"}},
	{apiOTAUpdate, routeVariant{http.MethodPost, apiPrefix + "/ota/updates"}},
	{apiDeviceReboot, routeVariant{http.MethodPost, apiPrefix + "/device/reboot"}},
	{apiUSBReenumerate, routeVariant{http.MethodPost, apiPrefix + "/device/usb/reenumerate"}},
	{apiSupportArchive, routeVariant{http.MethodGet, apiPrefix + "/logs/support"}},
	{apiLLMLogs, routeVariant{http.MethodGet, apiPrefix + "/logs/llm"}},
}

type apiMatch struct {
	endpoint apiEndpoint
	suffix   string
}

func (s *Server) APIHandler() http.Handler { return http.HandlerFunc(s.serveAPI) }

func matchAPIRequest(r *http.Request) apiMatch {
	for _, route := range apiRoutes {
		if r.Method == route.canonical.method && r.URL.Path == route.canonical.path {
			return apiMatch{endpoint: route.endpoint}
		}
	}
	escapedPath := r.URL.EscapedPath()
	const canonicalLogPrefix = apiPrefix + "/logs/llm/"
	if strings.HasPrefix(escapedPath, canonicalLogPrefix) {
		suffix := strings.TrimPrefix(escapedPath, canonicalLogPrefix)
		switch r.Method {
		case http.MethodGet:
			return apiMatch{endpoint: apiLLMLogExport, suffix: suffix}
		case http.MethodPut:
			return apiMatch{endpoint: apiLLMLogImport, suffix: suffix}
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
	case apiSTTTestStart:
		s.handleSTTTestStart(w, r)
	case apiSTTTestStop:
		s.handleSTTTestStop(w, r)
	case apiModels:
		target := apiPrefix + "/models"
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
	case apiDeviceStatus:
		s.handleAgentStatus(w, r)
	case apiAgentLogs:
		s.handleAgentLogs(w, r)
	case apiStorageStatus:
		s.proxyAgent(w, r, apiPrefix+"/storage/status")
	case apiStorageFormat:
		s.proxyAgent(w, r, apiPrefix+"/storage/format")
	case apiStorageEject:
		s.proxyAgent(w, r, apiPrefix+"/storage/eject")
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
