package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"aiden-agent/internal/agent/model"
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
	modelName := strings.TrimSpace(r.URL.Query().Get("model"))

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"provider": provider,
		"models":   models,
	}
	if modelName != "" {
		var spec model.ModelSpec
		if s.runtime != nil {
			if manager, ok := s.runtime.models.(*ModelManager); ok {
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				spec = manager.SpecForModel(ctx, provider, modelName)
				cancel()
			} else {
				spec, _ = LookupModelSpec(provider, modelName)
			}
		} else {
			spec, _ = LookupModelSpec(provider, modelName)
		}
		if spec.Provider != "" || spec.Name != "" || spec.Thinking != nil || spec.ContextWindow > 0 || spec.MaxOutput > 0 {
			response["spec"] = spec
		}
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("failed to encode models response: %v", err)
	}
}
