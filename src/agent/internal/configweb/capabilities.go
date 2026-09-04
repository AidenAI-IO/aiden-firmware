package configweb

import (
	"net/http"
	"strings"

	"aiden-agent/internal/agent"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		writeJSONError(w, http.StatusBadRequest, "provider parameter is required")
		return
	}
	locale := strings.TrimSpace(r.URL.Query().Get("locale"))
	if locale == "" {
		locale = "en-US"
		if cfg, err := agent.LoadRuntimeConfig(s.options.AgentConfigPath); err == nil {
			locale = cfg.LocaleOrDefault()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": provider,
		"models":   agent.GetLocalizedModelsForProvider(provider, locale),
	})
}
