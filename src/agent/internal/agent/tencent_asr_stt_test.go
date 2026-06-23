package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTencentASRSTTTranscribeWAV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("X-TC-Action"); got != tencentASRAction {
			t.Fatalf("X-TC-Action = %q, want %q", got, tencentASRAction)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, tencentASRAlgorithm) {
			t.Fatalf("Authorization = %q, want TC3 prefix", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		var req tencentASRRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if req.EngSerViceType != "16k_zh" || req.VoiceFormat != "wav" || req.Data == "" {
			t.Fatalf("unexpected request: %+v", req)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"Response": map[string]string{"Result": "你好"},
		})
	}))
	defer server.Close()

	client := NewTencentASRSTT("test-id", "test-key", "ap-guangzhou", "16k_zh", "", server.Client())
	client.apiURL = server.URL

	text, err := client.TranscribeWAV([]byte("RIFFxxxxWAVEfmt "))
	if err != nil {
		t.Fatalf("TranscribeWAV() error = %v", err)
	}
	if text != "你好" {
		t.Fatalf("text = %q, want 你好", text)
	}
}

func TestNewSTTClientTencentASRProviderNames(t *testing.T) {
	for _, provider := range []string{"tencent-asr", "tencent_asr", "tencent"} {
		t.Run(provider, func(t *testing.T) {
			cfg := Config{
				STT: STTConfig{
					Provider:        provider,
					SecretID:        "id",
					SecretKey:       "key",
					Region:          "ap-guangzhou",
					EngineModelType: "16k_zh",
				},
			}
			client, err := NewSTTClientFromConfig(cfg)
			if err != nil {
				t.Fatalf("NewSTTClientFromConfig() error = %v", err)
			}
			if _, ok := client.(*TencentASRSTT); !ok {
				t.Fatalf("client type = %T, want *TencentASRSTT", client)
			}
		})
	}
}

func TestBuildTencentTC3AuthorizationDeterministic(t *testing.T) {
	auth, err := buildTencentTC3Authorization("AKIDTEST", "SECRET", `{"EngSerViceType":"16k_zh"}`, 1700000000)
	if err != nil {
		t.Fatalf("buildTencentTC3Authorization() error = %v", err)
	}
	if !strings.Contains(auth, "Credential=AKIDTEST/2023-11-14/asr/tc3_request") {
		t.Fatalf("authorization missing credential scope: %q", auth)
	}
	if !strings.Contains(auth, "Signature=") {
		t.Fatalf("authorization missing signature: %q", auth)
	}
}

func TestTencentASRSTTRequiresCredentials(t *testing.T) {
	client := NewTencentASRSTT("", "", "", "", "")
	_, err := client.TranscribeWAV([]byte("wav"))
	if err == nil || !strings.Contains(err.Error(), "secret_id") {
		t.Fatalf("TranscribeWAV() error = %v, want missing credentials", err)
	}
}
