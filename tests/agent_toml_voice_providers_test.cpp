#include "doctest.h"

#include "agent_toml.h"

#include <cstdio>
#include <fstream>
#include <string>
#include <unistd.h>

namespace {

std::string voice_temp_path(const char* leaf) {
    char tmpl[] = "/tmp/aiden_voice_toml_XXXXXX";
    int fd = mkstemp(tmpl);
    REQUIRE(fd >= 0);
    close(fd);
    std::remove(tmpl);
    std::string base = tmpl;
    return base + "_" + leaf;
}

}

TEST_CASE("agent_toml loads named voice providers") {
    std::string path = voice_temp_path("voice_providers.toml");
    {
        std::ofstream out(path);
        out << "[tts_providers.minimax-main]\n"
            << "provider = \"minimax\"\n"
            << "api_key = \"sk-aaa\"\n"
            << "voice_id = \"male-qn-qingse\"\n"
            << "emotion = \"happy\"\n"
            << "[tts_providers.fish]\n"
            << "provider = \"fish-audio\"\n"
            << "token_env = \"FISH_KEY\"\n"
            << "reference_id = \"ref-abc\"\n"
            << "model = \"s2-pro\"\n"
            << "[stt_providers.tencent]\n"
            << "provider = \"tencent-asr\"\n"
            << "app_id = \"1234\"\n"
            << "secret_id = \"AKID-xxx\"\n"
            << "secret_key = \"secret-yyy\"\n"
            << "region = \"ap-shanghai\"\n"
            << "engine_model_type = \"16k_zh\"\n"
            << "[stt_providers.whisper]\n"
            << "provider = \"openai-whisper\"\n"
            << "api_key = \"sk-w\"\n"
            << "base_url = \"https://api.openai.com/v1\"\n"
            << "[tts]\n"
            << "provider = \"minimax-main\"\n"
            << "speed = 1.2\n"
            << "[stt]\n"
            << "provider = \"tencent\"\n"
            << "language = \"zh\"\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());

    REQUIRE(cfg.tts_providers.size() == 2);
    CHECK(cfg.tts_providers["minimax-main"].provider == "minimax");
    CHECK(cfg.tts_providers["minimax-main"].api_key == "sk-aaa");
    CHECK(cfg.tts_providers["minimax-main"].voice_id == "male-qn-qingse");
    CHECK(cfg.tts_providers["minimax-main"].emotion == "happy");
    CHECK(cfg.tts_providers["fish"].provider == "fish-audio");
    CHECK(cfg.tts_providers["fish"].token_env == "FISH_KEY");
    CHECK(cfg.tts_providers["fish"].reference_id == "ref-abc");
    CHECK(cfg.tts_providers["fish"].model == "s2-pro");

    REQUIRE(cfg.stt_providers.size() == 2);
    CHECK(cfg.stt_providers["tencent"].provider == "tencent-asr");
    CHECK(cfg.stt_providers["tencent"].app_id == "1234");
    CHECK(cfg.stt_providers["tencent"].secret_id == "AKID-xxx");
    CHECK(cfg.stt_providers["tencent"].secret_key == "secret-yyy");
    CHECK(cfg.stt_providers["tencent"].region == "ap-shanghai");
    CHECK(cfg.stt_providers["tencent"].engine_model_type == "16k_zh");
    CHECK(cfg.stt_providers["whisper"].base_url == "https://api.openai.com/v1");

    // The reference stays unresolved here: resolving is the Go runtime's job,
    // and the config page edits the reference itself.
    CHECK(cfg.tts.provider == "minimax-main");
    CHECK(cfg.stt.provider == "tencent");
    CHECK(cfg.tts.speed == doctest::Approx(1.2));
    CHECK(cfg.stt.language == "zh");

    std::remove(path.c_str());
}

TEST_CASE("agent_toml round-trips named voice providers") {
    aiden::AgentToml cfg;
    cfg.model.provider = "fake";
    cfg.search.provider = "duckduckgo";

    aiden::TTSProviderToml tts_record;
    tts_record.provider = "fish-audio";
    tts_record.api_key = "sk-fish";
    tts_record.reference_id = "ref-abc";
    cfg.tts_providers["fish"] = tts_record;

    aiden::STTProviderToml stt_record;
    stt_record.provider = "tencent-asr";
    stt_record.app_id = "1234";
    stt_record.secret_key = "secret-yyy";
    cfg.stt_providers["tencent"] = stt_record;

    cfg.tts.provider = "fish";
    cfg.tts.speed = 1.1;
    cfg.stt.provider = "tencent";
    cfg.stt.language = "zh";

    std::string path = voice_temp_path("voice_roundtrip.toml");
    std::string err;
    REQUIRE(aiden::save_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());

    aiden::AgentToml loaded;
    REQUIRE(aiden::load_agent_toml(path.c_str(), loaded, &err));
    REQUIRE(err.empty());

    REQUIRE(loaded.tts_providers.size() == 1);
    CHECK(loaded.tts_providers["fish"].provider == "fish-audio");
    CHECK(loaded.tts_providers["fish"].api_key == "sk-fish");
    CHECK(loaded.tts_providers["fish"].reference_id == "ref-abc");
    REQUIRE(loaded.stt_providers.size() == 1);
    CHECK(loaded.stt_providers["tencent"].provider == "tencent-asr");
    CHECK(loaded.stt_providers["tencent"].app_id == "1234");
    CHECK(loaded.stt_providers["tencent"].secret_key == "secret-yyy");
    CHECK(loaded.tts.provider == "fish");
    CHECK(loaded.stt.provider == "tencent");

    std::remove(path.c_str());
}

// A flat [tts]/[stt] credential set must become a record on load. Go migrates
// this too, but only in memory -- C++ is the only writer of agent.toml, so
// without migrating here a device's key would stay flat forever and the config
// page (which now edits records) would show no card for it: the key would be
// invisible and un-editable while still working at runtime.
TEST_CASE("agent_toml migrates flat voice credentials into records") {
    std::string path = voice_temp_path("flat_voice.toml");
    {
        std::ofstream out(path);
        out << "[model]\n"
            << "provider = \"fake\"\n"
            << "[tts]\n"
            << "provider = \"minimax-cn\"\n"
            << "api_key = \"sk-flat\"\n"
            << "voice_id = \"male-qn-qingse\"\n"
            << "speed = 1.3\n"
            << "[stt]\n"
            << "provider = \"tencent-asr\"\n"
            << "app_id = \"1234\"\n"
            << "secret_key = \"secret-yyy\"\n"
            << "language = \"zh\"\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());

    REQUIRE(cfg.tts_providers.count("minimax-cn") == 1);
    CHECK(cfg.tts_providers["minimax-cn"].provider == "minimax-cn");
    CHECK(cfg.tts_providers["minimax-cn"].api_key == "sk-flat");
    CHECK(cfg.tts_providers["minimax-cn"].voice_id == "male-qn-qingse");

    REQUIRE(cfg.stt_providers.count("tencent-asr") == 1);
    CHECK(cfg.stt_providers["tencent-asr"].app_id == "1234");
    CHECK(cfg.stt_providers["tencent-asr"].secret_key == "secret-yyy");

    // The reference is left naming the record, and the global settings stay put.
    CHECK(cfg.tts.provider == "minimax-cn");
    CHECK(cfg.tts.speed == doctest::Approx(1.3));
    CHECK(cfg.stt.language == "zh");

    // The credentials moved, so the flat fields must be cleared -- two editors
    // for one credential would disagree the moment either is used.
    CHECK(cfg.tts.api_key.empty());
    CHECK(cfg.tts.voice_id.empty());
    CHECK(cfg.stt.app_id.empty());
    CHECK(cfg.stt.secret_key.empty());

    std::remove(path.c_str());
}

// A record name that save_agent_toml could not write back must be rejected at
// load, matching the [providers.*] contract: accepting it yields a config that
// loads cleanly but fails every later save.
TEST_CASE("agent_toml rejects voice provider names it cannot write back") {
    struct Case {
        const char* leaf;
        const char* section;
        const char* expected;
    };
    const Case cases[] = {
        {"empty_tts_name.toml", "tts_providers.", "empty tts provider name"},
        {"dotted_tts_name.toml", "tts_providers.a.b", "invalid tts provider name"},
        {"spaced_tts_name.toml", "tts_providers.my provider", "invalid tts provider name"},
        {"empty_stt_name.toml", "stt_providers.", "empty stt provider name"},
        {"dotted_stt_name.toml", "stt_providers.a.b", "invalid stt provider name"},
        {"spaced_stt_name.toml", "stt_providers.my provider", "invalid stt provider name"},
    };

    for (const auto& tc : cases) {
        std::string path = voice_temp_path(tc.leaf);
        {
            std::ofstream out(path);
            out << "[" << tc.section << "]\n"
                << "provider = \"minimax\"\n";
        }

        aiden::AgentToml cfg;
        std::string err;
        CHECK_FALSE(aiden::load_agent_toml(path.c_str(), cfg, &err));
        CHECK(err.find(tc.expected) != std::string::npos);
        CHECK(cfg.tts_providers.empty());
        CHECK(cfg.stt_providers.empty());

        std::remove(path.c_str());
    }
}

// With no voice section at all nothing is invented: voice stays opt-in.
TEST_CASE("agent_toml invents no voice record when none is configured") {
    std::string path = voice_temp_path("no_voice.toml");
    {
        std::ofstream out(path);
        out << "[model]\n"
            << "provider = \"fake\"\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    CHECK(cfg.tts_providers.empty());
    CHECK(cfg.stt_providers.empty());

    std::remove(path.c_str());
}
