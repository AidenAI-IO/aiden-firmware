package agent

import (
	"net/http"
	"strings"
)

type sttProviderFactory func(Config, ProxyConfig, *http.Client, string) (STTClient, error)

type sttProviderDefinition struct {
	providerType string
	aliases      []string
	build        sttProviderFactory
}

var sttProviderDefinitions = []sttProviderDefinition{
	{
		providerType: defaultSTTProvider,
		aliases:      []string{"openai"},
		build: func(cfg Config, _ ProxyConfig, httpClient *http.Client, language string) (STTClient, error) {
			return NewOpenAIWhisperSTT(cfg.STT.APIKey, cfg.STT.Model, cfg.STT.BaseURL, language, httpClient), nil
		},
	},
	{
		providerType: tencentASRProvider,
		aliases:      []string{legacyTencentProvider, legacyTencentASRProvider},
		build: func(cfg Config, proxy ProxyConfig, httpClient *http.Client, language string) (STTClient, error) {
			client := NewTencentASRSTT(cfg.STT.SecretID, cfg.STT.SecretKey, cfg.STT.AppID, cfg.STT.Region, cfg.STT.EngineModelType, language, httpClient)
			client.proxy = proxy
			return client, nil
		},
	},
	{
		providerType: "openrouter",
		build: func(cfg Config, _ ProxyConfig, httpClient *http.Client, language string) (STTClient, error) {
			return NewOpenRouterSTT(cfg.STT.APIKey, cfg.STT.Model, cfg.STT.BaseURL, language, httpClient), nil
		},
	},
	{
		providerType: "qwen-asr",
		build: func(cfg Config, proxy ProxyConfig, _ *http.Client, language string) (STTClient, error) {
			client := NewDashScopeRealtimeSTT(cfg.STT.APIKey, cfg.STT.Model, cfg.STT.BaseURL, language)
			client.proxy = proxy
			return client, nil
		},
	},
	{
		providerType: "google-cloud",
		build: func(cfg Config, _ ProxyConfig, httpClient *http.Client, language string) (STTClient, error) {
			return NewGoogleCloudSTT(cfg.STT.APIKey, cfg.STT.BaseURL, language, cfg.STT.Model, httpClient)
		},
	},
}

func lookupSTTProviderDefinition(providerType string) (sttProviderDefinition, bool) {
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	for _, definition := range sttProviderDefinitions {
		if definition.providerType == providerType {
			return definition, true
		}
		for _, alias := range definition.aliases {
			if alias == providerType {
				return definition, true
			}
		}
	}
	return sttProviderDefinition{}, false
}

func canonicalSTTProviderType(providerType string) (string, bool) {
	definition, ok := lookupSTTProviderDefinition(providerType)
	if !ok {
		return "", false
	}
	return definition.providerType, true
}

func sttProviderNamesForCanonical(providerType string) []string {
	providerType = strings.ToLower(strings.TrimSpace(providerType))
	for _, definition := range sttProviderDefinitions {
		if definition.providerType != providerType {
			continue
		}
		names := make([]string, 1, 1+len(definition.aliases))
		names[0] = definition.providerType
		return append(names, definition.aliases...)
	}
	return nil
}

func sttProviderTypes() []string {
	types := make([]string, 0, len(sttProviderDefinitions))
	for _, definition := range sttProviderDefinitions {
		types = append(types, definition.providerType)
	}
	return types
}
