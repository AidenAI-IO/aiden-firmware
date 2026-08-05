package agent

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleModels returns the list of available models for a given provider.
// GET /api/models?provider=openai&locale=zh-CN
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		http.Error(w, "provider parameter is required", http.StatusBadRequest)
		return
	}

	locale := strings.TrimSpace(r.URL.Query().Get("locale"))
	if locale == "" {
		// Fall back to the configured locale. s.runtime is nil in some server
		// constructions, so guard it rather than risk a nil deref here.
		if s.runtime != nil {
			locale = s.runtime.config.LocaleOrDefault()
		} else {
			locale = defaultLocale
		}
	}

	models := GetLocalizedModelsForProvider(provider, locale)

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"provider": provider,
		"models":   models,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("failed to encode models response: %v", err)
	}
}
