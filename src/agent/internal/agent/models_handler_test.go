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
		name           string
		queryParams    string
		wantStatus     int
		wantProvider   string
		checkModels    bool
		wantMinModels  int
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
