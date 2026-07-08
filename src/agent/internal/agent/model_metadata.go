package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const providerModelMetadataTimeout = 5 * time.Second
const providerModelMetadataCacheVersion = 1

func providerSupportsModelMetadata(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openrouter", "ollama":
		return true
	default:
		return false
	}
}

func (m *ModelManager) prefetchProviderModelSpecIfNeeded() {
	if !m.needsProviderModelMetadata() {
		return
	}
	m.cachedProviderModelSpec()
}

func (m *ModelManager) needsProviderModelMetadata() bool {
	spec, _ := LookupModelSpec(m.config.Provider, m.config.Model)
	return m.needsProviderModelMetadataForSpec(spec)
}

func (m *ModelManager) needsProviderModelMetadataForSpec(spec ModelSpec) bool {
	if strings.TrimSpace(m.config.Model) == "" {
		return false
	}
	if !providerSupportsModelMetadata(m.config.Provider) {
		return false
	}
	explicitContextWindow := m.config.ContextWindow > 0
	explicitMaxOutput := m.config.ModelMaxOutputTokens > 0
	return (!explicitContextWindow && spec.ContextWindow <= 0) ||
		(!explicitMaxOutput && spec.MaxOutput <= 0)
}

func (m *ModelManager) cachedProviderModelSpec() ModelSpec {
	if strings.TrimSpace(m.config.Model) == "" {
		return ModelSpec{}
	}

	m.specMu.Lock()
	if m.providerSpecLoaded {
		spec := m.providerSpec
		m.specMu.Unlock()
		return spec
	}
	if m.providerSpecFetchStarted {
		m.specMu.Unlock()
		return ModelSpec{}
	}
	m.providerSpecFetchStarted = true
	m.specMu.Unlock()

	if spec, ok := m.readProviderModelSpecCache(); ok {
		m.storeProviderModelSpec(spec)
		return spec
	}

	go m.fetchProviderModelSpecInBackground()
	return ModelSpec{}
}

func (m *ModelManager) fetchProviderModelSpecInBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), providerModelMetadataTimeout)
	defer cancel()

	spec, err := m.fetchProviderModelSpec(ctx)
	if err != nil {
		m.resetProviderModelSpecFetchStarted()
		log.Printf("[WARN] [model-spec] fetch %s/%s metadata: %v", m.config.Provider, m.config.Model, err)
		return
	}
	if hasProviderModelSpecMetadata(spec) {
		if err := m.writeProviderModelSpecCache(spec); err != nil {
			log.Printf("[WARN] [model-spec] write provider metadata cache %s: %v", m.providerMetadataCachePath, err)
		}
	}
	m.storeProviderModelSpec(spec)
}

func (m *ModelManager) resetProviderModelSpecFetchStarted() {
	m.specMu.Lock()
	defer m.specMu.Unlock()

	m.providerSpecFetchStarted = false
	m.providerSpecLoaded = false
}

func (m *ModelManager) storeProviderModelSpec(spec ModelSpec) {
	m.specMu.Lock()
	defer m.specMu.Unlock()

	m.providerSpec = spec
	m.providerSpecLoaded = true
}

type providerModelMetadataCacheFile struct {
	Version int                                        `json:"version"`
	Entries map[string]providerModelMetadataCacheEntry `json:"entries"`
}

type providerModelMetadataCacheEntry struct {
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Endpoint          string    `json:"endpoint"`
	Spec              ModelSpec `json:"spec"`
	PromptCachePolicy string    `json:"prompt_cache_policy,omitempty"`
	FetchedAt         time.Time `json:"fetched_at"`
}

func (m *ModelManager) readProviderModelSpecCache() (ModelSpec, bool) {
	if strings.TrimSpace(m.providerMetadataCachePath) == "" {
		return ModelSpec{}, false
	}

	data, err := os.ReadFile(m.providerMetadataCachePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[WARN] [model-spec] read provider metadata cache %s: %v", m.providerMetadataCachePath, err)
		}
		return ModelSpec{}, false
	}

	var cache providerModelMetadataCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Printf("[WARN] [model-spec] parse provider metadata cache %s: %v", m.providerMetadataCachePath, err)
		return ModelSpec{}, false
	}
	if cache.Version != providerModelMetadataCacheVersion {
		return ModelSpec{}, false
	}

	entry, ok := cache.Entries[m.providerModelSpecCacheKey()]
	if !ok || !m.providerModelSpecCacheEntryMatches(entry) || !hasProviderModelSpecMetadata(entry.Spec) {
		return ModelSpec{}, false
	}
	return entry.Spec, true
}

func (m *ModelManager) writeProviderModelSpecCache(spec ModelSpec) error {
	if strings.TrimSpace(m.providerMetadataCachePath) == "" || !hasProviderModelSpecMetadata(spec) {
		return nil
	}

	cache := providerModelMetadataCacheFile{
		Version: providerModelMetadataCacheVersion,
		Entries: map[string]providerModelMetadataCacheEntry{},
	}
	data, err := os.ReadFile(m.providerMetadataCachePath)
	if err == nil && len(data) > 0 {
		var existing providerModelMetadataCacheFile
		if json.Unmarshal(data, &existing) == nil && existing.Version == providerModelMetadataCacheVersion {
			cache = existing
			if cache.Entries == nil {
				cache.Entries = map[string]providerModelMetadataCacheEntry{}
			}
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	key := m.providerModelSpecCacheKey()
	entry := cache.Entries[key]
	if entry.Provider == "" {
		entry.Provider = normalizedProviderName(m.config.Provider)
		entry.Model = normalizedModelName(m.config.Model)
		entry.Endpoint = m.providerMetadataEndpoint()
	}
	entry.Spec = spec
	entry.FetchedAt = time.Now().UTC()
	cache.Entries[key] = entry

	encoded, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeFileAtomic(m.providerMetadataCachePath, encoded, 0o644)
}

func hasProviderModelSpecMetadata(spec ModelSpec) bool {
	return spec.ContextWindow > 0 || spec.MaxOutput > 0
}

func (m *ModelManager) providerModelSpecCacheKey() string {
	parts := []string{
		normalizedProviderName(m.config.Provider),
		m.providerMetadataEndpoint(),
		normalizedModelName(m.config.Model),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (m *ModelManager) providerModelSpecCacheEntryMatches(entry providerModelMetadataCacheEntry) bool {
	return entry.Provider == normalizedProviderName(m.config.Provider) &&
		entry.Model == normalizedModelName(m.config.Model) &&
		entry.Endpoint == m.providerMetadataEndpoint()
}

func (m *ModelManager) providerMetadataEndpoint() string {
	switch normalizedProviderName(m.config.Provider) {
	case "openrouter":
		return openRouterModelsURL(m.config.BaseURL)
	case "ollama":
		return ollamaShowURL(m.config.BaseURL)
	default:
		return ""
	}
}

func normalizedProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func normalizedModelName(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func (m *ModelManager) fetchProviderModelSpec(ctx context.Context) (ModelSpec, error) {
	switch strings.ToLower(strings.TrimSpace(m.config.Provider)) {
	case "openrouter":
		return m.fetchOpenRouterModelSpec(ctx)
	case "ollama":
		return m.fetchOllamaModelSpec(ctx)
	default:
		return ModelSpec{}, nil
	}
}

func (m *ModelManager) modelMetadataHTTPClient() *http.Client {
	if m.metadataHTTPClient != nil {
		return m.metadataHTTPClient
	}
	return newProxyHTTPClient(m.proxy)
}

func (m *ModelManager) fetchOpenRouterModelSpec(ctx context.Context) (ModelSpec, error) {
	token := resolveToken(m.config)
	if token == "" {
		return ModelSpec{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelsURL(m.config.BaseURL), nil)
	if err != nil {
		return ModelSpec{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := m.modelMetadataHTTPClient().Do(req)
	if err != nil {
		return ModelSpec{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ModelSpec{}, fmt.Errorf("openrouter models returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ModelSpec{}, err
	}

	modelID := strings.ToLower(strings.TrimSpace(m.config.Model))
	for _, model := range payload.Data {
		if strings.ToLower(strings.TrimSpace(model.ID)) != modelID {
			continue
		}
		spec := ModelSpec{
			ContextWindow: firstPositiveInt(model.TopProvider.ContextLength, model.ContextLength),
			MaxOutput:     model.TopProvider.MaxCompletionTokens,
		}
		return spec, nil
	}
	return ModelSpec{}, nil
}

func openRouterModelsURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	return baseURL + "/models"
}

type openRouterModelsResponse struct {
	Data []openRouterModelMetadata `json:"data"`
}

type openRouterModelMetadata struct {
	ID            string `json:"id"`
	ContextLength int    `json:"context_length"`
	TopProvider   struct {
		ContextLength       int `json:"context_length"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

func (m *ModelManager) fetchOllamaModelSpec(ctx context.Context) (ModelSpec, error) {
	body, err := json.Marshal(map[string]string{"model": m.config.Model})
	if err != nil {
		return ModelSpec{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaShowURL(m.config.BaseURL), bytes.NewReader(body))
	if err != nil {
		return ModelSpec{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.modelMetadataHTTPClient().Do(req)
	if err != nil {
		return ModelSpec{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ModelSpec{}, fmt.Errorf("ollama show returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload ollamaShowResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ModelSpec{}, err
	}

	return ModelSpec{ContextWindow: firstPositiveInt(
		ollamaParametersNumCtx(payload.Parameters),
		ollamaModelInfoContextLength(payload.ModelInfo),
	)}, nil
}

func ollamaShowURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return baseURL + "/api/show"
}

type ollamaShowResponse struct {
	ModelInfo  map[string]json.RawMessage `json:"model_info"`
	Parameters json.RawMessage            `json:"parameters"`
}

func ollamaModelInfoContextLength(modelInfo map[string]json.RawMessage) int {
	best := 0
	for key, raw := range modelInfo {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized != "context_length" && !strings.HasSuffix(normalized, ".context_length") {
			continue
		}
		if value := positiveJSONInt(raw); value > best {
			best = value
		}
	}
	return best
}

func ollamaParametersNumCtx(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		for key, value := range fields {
			if strings.EqualFold(strings.TrimSpace(key), "num_ctx") {
				return positiveJSONInt(value)
			}
		}
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0
	}
	return parseOllamaNumCtx(text)
}

func parseOllamaNumCtx(text string) int {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ReplaceAll(line, "=", " ")
		parts := strings.Fields(line)
		for i, part := range parts {
			if !strings.EqualFold(part, "num_ctx") || i+1 >= len(parts) {
				continue
			}
			if value, err := strconv.Atoi(strings.Trim(parts[i+1], `"'`)); err == nil && value > 0 {
				return value
			}
		}
	}
	return 0
}

func positiveJSONInt(raw json.RawMessage) int {
	var value int
	if err := json.Unmarshal(raw, &value); err == nil && value > 0 {
		return value
	}
	var floatValue float64
	if err := json.Unmarshal(raw, &floatValue); err == nil && floatValue > 0 {
		return int(floatValue)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, err := strconv.Atoi(strings.TrimSpace(text))
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
