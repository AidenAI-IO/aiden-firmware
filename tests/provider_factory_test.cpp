#include "doctest.h"
#include "provider_factory.h"
#include "config.h"
#include <cstdio>
#include <string>

using aiden::ProviderCheckResult;
using aiden::check_llm_provider;
using aiden::check_tts_provider;
using aiden::check_llm_config;
using aiden::check_tts_config;
using aiden::check_provider_config;

TEST_CASE("check_llm_provider accepts openrouter") {
    ProviderCheckResult r = check_llm_provider("openrouter");
    CHECK(r.ok);
    CHECK(r.normalized == "openrouter");
}

TEST_CASE("check_tts_provider accepts minimax") {
    ProviderCheckResult r = check_tts_provider("minimax");
    CHECK(r.ok);
    CHECK(r.normalized == "minimax");
}

TEST_CASE("check_llm_config validates openrouter independently") {
    aiden::ModelConfig cfg;
    std::snprintf(cfg.provider, sizeof(cfg.provider), "%s", "openrouter");

    ProviderCheckResult missing_key = check_llm_config(cfg);
    CHECK_FALSE(missing_key.ok);
    CHECK(missing_key.error == "model provider openrouter requires api_key");

    std::snprintf(cfg.api_key, sizeof(cfg.api_key), "%s", "sk-key");
    ProviderCheckResult missing_model = check_llm_config(cfg);
    CHECK_FALSE(missing_model.ok);
    CHECK(missing_model.error == "model provider openrouter requires model");

    std::snprintf(cfg.model, sizeof(cfg.model), "%s", "openai/gpt-4o-audio-preview");
    ProviderCheckResult ok = check_llm_config(cfg);
    CHECK(ok.ok);
}

TEST_CASE("check_tts_config validates minimax independently") {
    aiden::TTSConfig cfg;
    std::snprintf(cfg.provider, sizeof(cfg.provider), "%s", "minimax");

    ProviderCheckResult missing_key = check_tts_config(cfg);
    CHECK_FALSE(missing_key.ok);
    CHECK(missing_key.error == "tts provider minimax requires api_key");

    std::snprintf(cfg.api_key, sizeof(cfg.api_key), "%s", "mm-key");
    ProviderCheckResult missing_voice = check_tts_config(cfg);
    CHECK_FALSE(missing_voice.ok);
    CHECK(missing_voice.error == "tts provider minimax requires voice_id");

    std::snprintf(cfg.voice_id, sizeof(cfg.voice_id), "%s", "voice-a");
    ProviderCheckResult ok = check_tts_config(cfg);
    CHECK(ok.ok);
}

TEST_CASE("provider checks reject empty provider names") {
    CHECK_FALSE(check_llm_provider("").ok);
    CHECK_FALSE(check_tts_provider("").ok);
}

TEST_CASE("provider checks reject unknown providers with clear errors") {
    ProviderCheckResult llm = check_llm_provider("anthropic");
    CHECK_FALSE(llm.ok);
    CHECK(llm.error == "unsupported model provider: anthropic");

    ProviderCheckResult tts = check_tts_provider("elevenlabs");
    CHECK_FALSE(tts.ok);
    CHECK(tts.error == "unsupported tts provider: elevenlabs");
}

TEST_CASE("provider checks are case-sensitive for now") {
    CHECK_FALSE(check_llm_provider("OpenRouter").ok);
    CHECK_FALSE(check_tts_provider("MiniMax").ok);
}

TEST_CASE("check_provider_config validates both sections") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.model.api_key, sizeof(cfg.model.api_key), "%s", "sk-key");
    std::snprintf(cfg.model.model, sizeof(cfg.model.model), "%s", "openai/gpt-4o-audio-preview");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");
    std::snprintf(cfg.tts.api_key, sizeof(cfg.tts.api_key), "%s", "mm-key");
    std::snprintf(cfg.tts.voice_id, sizeof(cfg.tts.voice_id), "%s", "voice-a");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK(r.ok);
}

TEST_CASE("check_provider_config fails fast on bad llm provider") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "bad-llm");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK_FALSE(r.ok);
    CHECK(r.error == "unsupported model provider: bad-llm");
}

TEST_CASE("check_provider_config fails on bad tts provider") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "bad-tts");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK_FALSE(r.ok);
    CHECK(r.error == "unsupported tts provider: bad-tts");
}

TEST_CASE("check_provider_config requires openrouter api_key") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.model.model, sizeof(cfg.model.model), "%s", "openai/gpt-4o-audio-preview");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");
    std::snprintf(cfg.tts.api_key, sizeof(cfg.tts.api_key), "%s", "mm-key");
    std::snprintf(cfg.tts.voice_id, sizeof(cfg.tts.voice_id), "%s", "voice-a");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK_FALSE(r.ok);
    CHECK(r.error == "model provider openrouter requires api_key");
}

TEST_CASE("check_provider_config requires openrouter model") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.model.api_key, sizeof(cfg.model.api_key), "%s", "sk-key");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");
    std::snprintf(cfg.tts.api_key, sizeof(cfg.tts.api_key), "%s", "mm-key");
    std::snprintf(cfg.tts.voice_id, sizeof(cfg.tts.voice_id), "%s", "voice-a");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK_FALSE(r.ok);
    CHECK(r.error == "model provider openrouter requires model");
}

TEST_CASE("check_provider_config requires minimax api_key") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.model.api_key, sizeof(cfg.model.api_key), "%s", "sk-key");
    std::snprintf(cfg.model.model, sizeof(cfg.model.model), "%s", "openai/gpt-4o-audio-preview");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");
    std::snprintf(cfg.tts.voice_id, sizeof(cfg.tts.voice_id), "%s", "voice-a");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK_FALSE(r.ok);
    CHECK(r.error == "tts provider minimax requires api_key");
}

TEST_CASE("check_provider_config requires minimax voice_id") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.model.api_key, sizeof(cfg.model.api_key), "%s", "sk-key");
    std::snprintf(cfg.model.model, sizeof(cfg.model.model), "%s", "openai/gpt-4o-audio-preview");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");
    std::snprintf(cfg.tts.api_key, sizeof(cfg.tts.api_key), "%s", "mm-key");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK_FALSE(r.ok);
    CHECK(r.error == "tts provider minimax requires voice_id");
}

TEST_CASE("check_provider_config accepts complete provider config") {
    aiden::AgentConfig cfg;
    std::snprintf(cfg.model.provider, sizeof(cfg.model.provider), "%s", "openrouter");
    std::snprintf(cfg.model.api_key, sizeof(cfg.model.api_key), "%s", "sk-key");
    std::snprintf(cfg.model.model, sizeof(cfg.model.model), "%s", "openai/gpt-4o-audio-preview");
    std::snprintf(cfg.tts.provider, sizeof(cfg.tts.provider), "%s", "minimax");
    std::snprintf(cfg.tts.api_key, sizeof(cfg.tts.api_key), "%s", "mm-key");
    std::snprintf(cfg.tts.voice_id, sizeof(cfg.tts.voice_id), "%s", "voice-a");

    ProviderCheckResult r = check_provider_config(cfg);
    CHECK(r.ok);
}
