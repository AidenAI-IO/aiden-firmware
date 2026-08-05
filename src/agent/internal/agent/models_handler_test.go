package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleModels(t *testing.T) {
	runtime, err := NewRuntime(Config{
		Model: ModelConfig{
			Provider: "openai",
			Model:    "gpt-4o",
			APIKey:   "test-key",
		},
		Locale: localeSimplifiedChinese,
	})
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	logger, err := NewLogger(t.TempDir(), 7)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	server := &Server{
		runtime: runtime,
		logger:  logger,
	}

	tests := []struct {
		name          string
		queryParams   string
		wantStatus    int
		wantProvider  string
		checkModels   bool
		wantMinModels int
	}{
		{
			name:          "get openai models in Chinese",
			queryParams:   "provider=openai&locale=zh-CN",
			wantStatus:    http.StatusOK,
			wantProvider:  "openai",
			checkModels:   true,
			wantMinModels: 1,
		},
		{
			name:          "get openai models in English",
			queryParams:   "provider=openai&locale=en-US",
			wantStatus:    http.StatusOK,
			wantProvider:  "openai",
			checkModels:   true,
			wantMinModels: 1,
		},
		{
			name:          "get kimi models",
			queryParams:   "provider=kimi&locale=zh-CN",
			wantStatus:    http.StatusOK,
			wantProvider:  "kimi",
			checkModels:   true,
			wantMinModels: 1,
		},
		{
			name:          "locale defaults to config",
			queryParams:   "provider=openai",
			wantStatus:    http.StatusOK,
			wantProvider:  "openai",
			checkModels:   true,
			wantMinModels: 1,
		},
		{
			name:        "missing provider parameter",
			queryParams: "locale=zh-CN",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:          "unknown provider returns empty",
			queryParams:   "provider=unknown-provider&locale=zh-CN",
			wantStatus:    http.StatusOK,
			wantProvider:  "unknown-provider",
			checkModels:   true,
			wantMinModels: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/models?"+tt.queryParams, nil)
			w := httptest.NewRecorder()

			server.handleModels(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
				t.Logf("body: %s", w.Body.String())
				return
			}

			if tt.wantStatus != http.StatusOK {
				return
			}

			var response map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			provider, ok := response["provider"].(string)
			if !ok || provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", provider, tt.wantProvider)
			}

			if tt.checkModels {
				models, ok := response["models"].([]interface{})
				if !ok {
					t.Fatalf("models is not an array")
				}

				if len(models) < tt.wantMinModels {
					t.Errorf("got %d models, want at least %d", len(models), tt.wantMinModels)
				}

				// Check first model structure if any
				if len(models) > 0 {
					model, ok := models[0].(map[string]interface{})
					if !ok {
						t.Fatalf("first model is not an object")
					}

					// Check required fields
					if _, hasID := model["id"]; !hasID {
						t.Error("model missing 'id' field")
					}
					if _, hasDesc := model["description"]; !hasDesc {
						t.Error("model missing 'description' field")
					}
					if _, hasRec := model["recommended"]; !hasRec {
						t.Error("model missing 'recommended' field")
					}
				}
			}
		})
	}
}

func TestHandleModels_WrongMethod(t *testing.T) {
	runtime, _ := NewRuntime(Config{
		Model: ModelConfig{Provider: "openai", Model: "gpt-4o", APIKey: "test"},
	})

	logger, _ := NewLogger(t.TempDir(), 7)

	server := &Server{
		runtime: runtime,
		logger:  logger,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/models?provider=openai", nil)
	w := httptest.NewRecorder()

	server.handleModels(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// decodeModelsResponse issues a GET /api/models and returns the decoded models.
func decodeModelsResponse(t *testing.T, server *Server, query string) []LocalizedModelInfo {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/models?"+query, nil)
	w := httptest.NewRecorder()
	server.handleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var response struct {
		Provider string               `json:"provider"`
		Models   []LocalizedModelInfo `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Models
}

// newModelsTestServer builds a Server whose config carries the given locale.
func newModelsTestServer(t *testing.T, locale string) *Server {
	t.Helper()
	runtime, err := NewRuntime(Config{
		Locale: locale,
		Model:  ModelConfig{Provider: "openai", Model: "gpt-4o", APIKey: "test"},
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	logger, err := NewLogger(t.TempDir(), 7)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	return &Server{runtime: runtime, logger: logger}
}

// TestHandleModelsLocaleSelectsDescription pins that the locale parameter
// actually changes the returned text. The original test asserted only that a
// description field was PRESENT, so ignoring the locale entirely still passed.
func TestHandleModelsLocaleSelectsDescription(t *testing.T) {
	server := newModelsTestServer(t, localeSimplifiedChinese)

	zh := decodeModelsResponse(t, server, "provider=openai&locale=zh-CN")
	en := decodeModelsResponse(t, server, "provider=openai&locale=en-US")
	if len(zh) == 0 || len(en) == 0 {
		t.Fatal("expected openai to return models for both locales")
	}
	if zh[0].Description == en[0].Description {
		t.Errorf("zh-CN and en-US descriptions are identical (%q); locale is being ignored", zh[0].Description)
	}
	// The ids are locale-independent and must line up.
	if zh[0].ID != en[0].ID {
		t.Errorf("model ids differ across locales: %q vs %q", zh[0].ID, en[0].ID)
	}
}

// TestHandleModelsLocaleFallsBackToConfig pins the fallback path: with no
// locale in the query, the configured locale must be used. Deleting the
// fallback used to go unnoticed because English is also the default.
func TestHandleModelsLocaleFallsBackToConfig(t *testing.T) {
	zhServer := newModelsTestServer(t, localeSimplifiedChinese)
	enServer := newModelsTestServer(t, localeEnglishUS)

	fromZhConfig := decodeModelsResponse(t, zhServer, "provider=openai")
	fromEnConfig := decodeModelsResponse(t, enServer, "provider=openai")
	if len(fromZhConfig) == 0 || len(fromEnConfig) == 0 {
		t.Fatal("expected openai to return models")
	}
	if fromZhConfig[0].Description == fromEnConfig[0].Description {
		t.Errorf("config locale is not honored: both returned %q", fromZhConfig[0].Description)
	}

	// And an explicit query locale must win over the configured one.
	explicit := decodeModelsResponse(t, zhServer, "provider=openai&locale=en-US")
	if explicit[0].Description != fromEnConfig[0].Description {
		t.Errorf("explicit locale ignored: got %q, want %q",
			explicit[0].Description, fromEnConfig[0].Description)
	}
}

// TestHandleModelsUnknownProviderReturnsEmpty replaces an assertion that could
// not fail (len(models) < 0). An unknown provider must yield an empty list, not
// a fabricated entry.
func TestHandleModelsUnknownProviderReturnsEmpty(t *testing.T) {
	server := newModelsTestServer(t, localeSimplifiedChinese)
	models := decodeModelsResponse(t, server, "provider=unknown-provider&locale=zh-CN")
	if len(models) != 0 {
		t.Errorf("got %d models for an unknown provider, want 0", len(models))
	}
}

// TestHandleModelsNilRuntime covers the locale fallback when the server has no
// runtime attached: it must not panic.
func TestHandleModelsNilRuntime(t *testing.T) {
	logger, err := NewLogger(t.TempDir(), 7)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	server := &Server{logger: logger}

	req := httptest.NewRequest(http.MethodGet, "/api/models?provider=openai", nil)
	w := httptest.NewRecorder()
	server.handleModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
}
