#pragma once
#include "config.h"
#include "provider_interfaces.h"
#include <memory>
#include <string>

namespace aiden {

struct ProviderCheckResult {
    bool ok;
    std::string normalized;
    std::string error;

    ProviderCheckResult() : ok(false) {}
};

ProviderCheckResult check_llm_provider(const char* provider);
ProviderCheckResult check_tts_provider(const char* provider);
ProviderCheckResult check_stt_provider(const char* provider);
ProviderCheckResult check_llm_config(const ModelConfig& config);
ProviderCheckResult check_tts_config(const TTSConfig& config);
ProviderCheckResult check_stt_config(const STTConfig& config);
ProviderCheckResult check_provider_config(const AgentConfig& config);

std::unique_ptr<LlmClient> create_llm_client(const AgentConfig& config, std::string& error);
std::unique_ptr<TtsClient> create_tts_client(const AgentConfig& config, std::string& error);
std::unique_ptr<SttClient> create_stt_client(const AgentConfig& config, std::string& error);

}
