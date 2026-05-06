#include "doctest.h"
#include "config.h"
#include <cstdio>
#include <string>
#include <unistd.h>

namespace {

struct TempFile {
    std::string path;

    explicit TempFile(const char* contents) {
        char tmpl[] = "/tmp/aiden_cfg_XXXXXX";
        int fd = mkstemp(tmpl);
        REQUIRE(fd >= 0);
        path = tmpl;
        size_t len = std::string(contents).size();
        ssize_t written = ::write(fd, contents, len);
        REQUIRE(written == (ssize_t)len);
        ::close(fd);
    }

    ~TempFile() { ::unlink(path.c_str()); }
};

}

TEST_CASE("load_config parses role-based sections") {
    TempFile f(
        "[model]\n"
        "provider = openrouter\n"
        "api_key = sk-test\n"
        "model = openai/gpt-4o-audio-preview\n"
        "\n"
        "[tts]\n"
        "provider = minimax\n"
        "api_key = mm-test\n"
        "model = speech-2.8-hd\n"
        "voice_id = male-qn-qingse\n"
        "emotion = happy\n"
        "speed = 1.25\n"
        "\n"
        "[agent]\n"
        "hid_binary = ./build/bin/example_usb_hid\n"
        "energy_threshold = 450\n"
        "silence_ms = 700\n"
        "min_speech_ms = 250\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));

    CHECK(std::string(cfg.model.provider) == "openrouter");
    CHECK(std::string(cfg.model.api_key) == "sk-test");
    CHECK(std::string(cfg.model.model) == "openai/gpt-4o-audio-preview");

    CHECK(std::string(cfg.tts.provider) == "minimax");
    CHECK(std::string(cfg.tts.api_key) == "mm-test");
    CHECK(std::string(cfg.tts.model) == "speech-2.8-hd");
    CHECK(std::string(cfg.tts.voice_id) == "male-qn-qingse");
    CHECK(std::string(cfg.tts.emotion) == "happy");
    CHECK(cfg.tts.speed == doctest::Approx(1.25f));

    CHECK(std::string(cfg.hid_binary) == "./build/bin/example_usb_hid");
    CHECK(cfg.energy_threshold == 450);
    CHECK(cfg.silence_ms == 700);
    CHECK(cfg.min_speech_ms == 250);
}

TEST_CASE("load_config rejects config with empty model api_key") {
    TempFile f(
        "[model]\n"
        "provider = openrouter\n"
        "model = foo\n"
        "\n"
        "[tts]\n"
        "api_key = mm-test\n"
    );

    aiden::AgentConfig cfg;
    CHECK_FALSE(aiden::load_config(f.path.c_str(), cfg));
}

TEST_CASE("load_config returns false for missing file") {
    aiden::AgentConfig cfg;
    CHECK_FALSE(aiden::load_config("/tmp/aiden_definitely_does_not_exist_xyz", cfg));
}

TEST_CASE("load_config skips comments and blank lines") {
    TempFile f(
        "# top comment\n"
        "; also a comment\n"
        "\n"
        "[model]\n"
        "# section comment\n"
        "api_key = sk-abc\n"
        "   \n"
        "model = gpt-4\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(std::string(cfg.model.api_key) == "sk-abc");
    CHECK(std::string(cfg.model.model) == "gpt-4");
}

TEST_CASE("load_config trims whitespace around keys and values") {
    TempFile f(
        "[model]\n"
        "   api_key   =   sk-trimmed   \n"
        "\tmodel\t=\tgpt-4\t\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(std::string(cfg.model.api_key) == "sk-trimmed");
    CHECK(std::string(cfg.model.model) == "gpt-4");
}

TEST_CASE("load_config silently ignores unknown sections and keys") {
    TempFile f(
        "[model]\n"
        "api_key = sk-ok\n"
        "unknown_key = whatever\n"
        "\n"
        "[unknown_section]\n"
        "foo = bar\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(std::string(cfg.model.api_key) == "sk-ok");
}

TEST_CASE("load_config applies defaults for absent numeric fields") {
    TempFile f(
        "[model]\n"
        "api_key = sk-ok\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(cfg.energy_threshold == 300);
    CHECK(cfg.silence_ms == 800);
    CHECK(cfg.min_speech_ms == 300);
    CHECK(cfg.tts.speed == doctest::Approx(1.0f));
}

TEST_CASE("load_config uses the last value when a key is repeated") {
    TempFile f(
        "[model]\n"
        "api_key = sk-first\n"
        "api_key = sk-last\n"
        "model = first-model\n"
        "model = final-model\n"
        "\n"
        "[tts]\n"
        "speed = 0.5\n"
        "speed = 1.75\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(std::string(cfg.model.api_key) == "sk-last");
    CHECK(std::string(cfg.model.model) == "final-model");
    CHECK(cfg.tts.speed == doctest::Approx(1.75f));
}

TEST_CASE("load_config parses sections regardless of order") {
    TempFile f(
        "[agent]\n"
        "silence_ms = 650\n"
        "\n"
        "[tts]\n"
        "voice_id = calm\n"
        "\n"
        "[model]\n"
        "api_key = sk-ok\n"
        "provider = openrouter\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(cfg.silence_ms == 650);
    CHECK(std::string(cfg.tts.voice_id) == "calm");
    CHECK(std::string(cfg.model.provider) == "openrouter");
}

TEST_CASE("load_config produces zero for non-numeric numeric field values") {
    TempFile f(
        "[model]\n"
        "api_key = sk-ok\n"
        "\n"
        "[tts]\n"
        "speed = fast\n"
        "\n"
        "[agent]\n"
        "energy_threshold = not-a-number\n"
        "silence_ms = NaN\n"
        "min_speech_ms = ???\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(cfg.energy_threshold == 0);
    CHECK(cfg.silence_ms == 0);
    CHECK(cfg.min_speech_ms == 0);
    CHECK(cfg.tts.speed == doctest::Approx(0.0f));
}

TEST_CASE("load_config keeps optional fields at defaults when omitted") {
    TempFile f(
        "[model]\n"
        "api_key = sk-ok\n"
    );

    aiden::AgentConfig cfg;
    REQUIRE(aiden::load_config(f.path.c_str(), cfg));
    CHECK(std::string(cfg.model.provider).empty());
    CHECK(std::string(cfg.model.model).empty());
    CHECK(std::string(cfg.tts.api_key).empty());
    CHECK(std::string(cfg.tts.voice_id).empty());
    CHECK(std::string(cfg.hid_binary).empty());
    CHECK(std::string(cfg.additional_prompt).empty());
}
