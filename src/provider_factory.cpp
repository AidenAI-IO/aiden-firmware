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
    if (strcmp(provider, "openai") == 0) {
        result.ok = true;
        result.normalized = "openai";
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

ProviderCheckResult check_stt_provider(const char* provider) {
    ProviderCheckResult result;
    if (is_empty(provider)) {
        result.error = "unsupported stt provider: ";
        return result;
    }
    if (strcmp(provider, "openai_whisper") == 0) {
        result.ok = true;
        result.normalized = "openai_whisper";
        return result;
    }
    if (strcmp(provider, "tencent_asr") == 0) {
        result.ok = true;
        result.normalized = "tencent_asr";
        return result;
    }
    result.error = std::string("unsupported stt provider: ") + provider;
    return result;
}

ProviderCheckResult check_llm_config(const ModelConfig& config) {
    ProviderCheckResult llm = check_llm_provider(config.provider);
    if (!llm.ok) return llm;

    if (llm.normalized == "openrouter" || llm.normalized == "openai") {
        if (is_empty(config.api_key)) {
            ProviderCheckResult result;
            result.error = std::string("model provider ") + llm.normalized + " requires api_key";
            return result;
        }
        if (is_empty(config.model)) {
            ProviderCheckResult result;
            result.error = std::string("model provider ") + llm.normalized + " requires model";
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

ProviderCheckResult check_stt_config(const STTConfig& config) {
    ProviderCheckResult stt = check_stt_provider(config.provider);
    if (!stt.ok) return stt;

    if (stt.normalized == "openai_whisper") {
        if (is_empty(config.api_key)) {
            ProviderCheckResult result;
            result.error = "stt provider openai_whisper requires api_key";
            return result;
        }
        if (is_empty(config.model)) {
            ProviderCheckResult result;
            result.error = "stt provider openai_whisper requires model";
            return result;
        }
    }

    if (stt.normalized == "tencent_asr") {
        if (is_empty(config.secret_id)) {
            ProviderCheckResult result;
            result.error = "stt provider tencent_asr requires secret_id";
            return result;
        }
        if (is_empty(config.secret_key)) {
            ProviderCheckResult result;
            result.error = "stt provider tencent_asr requires secret_key";
            return result;
        }
        if (is_empty(config.engine_model_type)) {
            ProviderCheckResult result;
            result.error = "stt provider tencent_asr requires engine_model_type";
            return result;
        }
    }

    ProviderCheckResult result;
    result.ok = true;
    result.normalized = stt.normalized;
    return result;
}

ProviderCheckResult check_provider_config(const AgentConfig& config) {
    ProviderCheckResult llm = check_llm_provider(config.model.provider);
    if (!llm.ok) return llm;

    ProviderCheckResult tts = check_tts_provider(config.tts.provider);
    if (!tts.ok) return tts;

    llm = check_llm_config(config.model);
    if (!llm.ok) return llm;

    ProviderCheckResult tts_cfg = check_tts_config(config.tts);
    if (!tts_cfg.ok) return tts_cfg;

    if (strcmp(config.asr_mode, "stt_then_text") == 0) {
        ProviderCheckResult stt_cfg = check_stt_config(config.stt);
        if (!stt_cfg.ok) return stt_cfg;
    }

    ProviderCheckResult ok;
    ok.ok = true;
    return ok;
}

}
