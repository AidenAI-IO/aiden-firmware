#include "provider_factory.h"
#include "openrouter_client.h"
#include "openai_client.h"
#include "minimax_tts.h"

namespace aiden {

std::unique_ptr<LlmClient> create_llm_client(const AgentConfig& config, std::string& error) {
    ProviderCheckResult check = check_llm_config(config.model);
    if (!check.ok) {
        error = check.error;
        return std::unique_ptr<LlmClient>();
    }

    if (check.normalized == "openrouter") {
        return std::unique_ptr<LlmClient>(
            new OpenRouterClient(config.model.api_key, config.model.model, config.additional_prompt));
    }

    if (check.normalized == "openai") {
        return std::unique_ptr<LlmClient>(
            new OpenAIClient(config.model.api_key, config.model.model, config.additional_prompt));
    }

    error = std::string("unsupported model provider: ") + config.model.provider;
    return std::unique_ptr<LlmClient>();
}

std::unique_ptr<TtsClient> create_tts_client(const AgentConfig& config, std::string& error) {
    ProviderCheckResult check = check_tts_config(config.tts);
    if (!check.ok) {
        error = check.error;
        return std::unique_ptr<TtsClient>();
    }

    if (check.normalized == "minimax") {
        return std::unique_ptr<TtsClient>(
            new MinimaxTTS(config.tts.api_key, config.tts.voice_id,
                           config.tts.emotion, config.tts.speed));
    }

    error = std::string("unsupported tts provider: ") + config.tts.provider;
    return std::unique_ptr<TtsClient>();
}

}
