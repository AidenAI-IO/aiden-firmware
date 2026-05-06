#include "provider_factory.h"
#include <string.h>

namespace aiden {

static bool is_empty(const char* s) {
    return !s || s[0] == '\0';
}

ProviderCheckResult check_llm_provider(const char* provider) {
    ProviderCheckResult result;
    if (is_empty(provider)) {
        result.error = "unsupported model provider: ";
        return result;
    }
    if (strcmp(provider, "openrouter") == 0) {
        result.ok = true;
        result.normalized = "openrouter";
        return result;
    }
    result.error = std::string("unsupported model provider: ") + provider;
    return result;
}

ProviderCheckResult check_tts_provider(const char* provider) {
    ProviderCheckResult result;
    if (is_empty(provider)) {
        result.error = "unsupported tts provider: ";
        return result;
    }
    if (strcmp(provider, "minimax") == 0) {
        result.ok = true;
        result.normalized = "minimax";
        return result;
    }
    result.error = std::string("unsupported tts provider: ") + provider;
    return result;
}

ProviderCheckResult check_llm_config(const ModelConfig& config) {
    ProviderCheckResult llm = check_llm_provider(config.provider);
    if (!llm.ok) return llm;

    if (llm.normalized == "openrouter") {
        if (is_empty(config.api_key)) {
            ProviderCheckResult result;
            result.error = "model provider openrouter requires api_key";
            return result;
        }
        if (is_empty(config.model)) {
            ProviderCheckResult result;
            result.error = "model provider openrouter requires model";
            return result;
        }
    }

    ProviderCheckResult result;
    result.ok = true;
    result.normalized = llm.normalized;
    return result;
}

ProviderCheckResult check_tts_config(const TTSConfig& config) {
    ProviderCheckResult tts = check_tts_provider(config.provider);
    if (!tts.ok) return tts;

    if (tts.normalized == "minimax") {
        if (is_empty(config.api_key)) {
            ProviderCheckResult result;
            result.error = "tts provider minimax requires api_key";
            return result;
        }
        if (is_empty(config.voice_id)) {
            ProviderCheckResult result;
            result.error = "tts provider minimax requires voice_id";
            return result;
        }
    }

    ProviderCheckResult result;
    result.ok = true;
    result.normalized = tts.normalized;
    return result;
}

ProviderCheckResult check_provider_config(const AgentConfig& config) {
    ProviderCheckResult llm = check_llm_provider(config.model.provider);
    if (!llm.ok) return llm;

    ProviderCheckResult tts = check_tts_provider(config.tts.provider);
    if (!tts.ok) return tts;

    llm = check_llm_config(config.model);
    if (!llm.ok) return llm;

    return check_tts_config(config.tts);
}

}
