#include "doctest.h"

#include "agent_toml.h"

#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <string>
#include <unistd.h>

namespace {

std::string make_temp_path(const char* leaf) {
    char tmpl[] = "/tmp/aiden_agent_toml_XXXXXX";
    int fd = mkstemp(tmpl);
    REQUIRE(fd >= 0);
    close(fd);
    std::remove(tmpl);
    std::string base = tmpl;
    return base + "_" + leaf;
}

}

TEST_CASE("agent_toml round-trip preserves Go-agent schema fields") {
    aiden::AgentToml cfg;
    cfg.instruction = "Hello \"world\"";
    cfg.input_mode = "stt";
    cfg.trigger_mode = "manual";
    cfg.vad_backend = "cpu";
    cfg.vad_model_path = "/userdata/agent/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn";
    cfg.vad_helper_path = "/oem/usr/bin/rknn_vad";
    cfg.vad_speech_threshold = 0.5;
    cfg.silence_ms = 1000;
    cfg.min_speech_ms = 250;
    cfg.voice_session_enabled = false;
    cfg.voice_followup_timeout_ms = 6500;
    cfg.voice_first_turn_timeout_ms = 11000;
    cfg.voice_max_turns = 4;
    cfg.voice_interrupt_on_wakeup = false;
    cfg.voice_streaming_tts_enabled = false;
    cfg.voice_tool_call_speech = false;
    cfg.voice_max_response_tokens = 240;
    cfg.max_iterations = 6;

    cfg.model.provider = "openrouter";
    cfg.model.model = "openai/gpt-4o-mini";
    cfg.model.api_key = "sk-or-test";
    cfg.model.token_env = "OPENROUTER_API_KEY";
    cfg.model.temperature = 0.2;
    cfg.model.max_tokens = 1000;

    cfg.tts.provider = "minimax";
    cfg.tts.api_key = "mx-test";
    cfg.tts.voice_id = "male-qn-qingse";
    cfg.tts.emotion = "happy";
    cfg.tts.speed = 1.25;

    cfg.stt.provider = "openai-whisper";
    cfg.stt.api_key = "sk-stt";
    cfg.stt.model = "whisper-1";

    cfg.audio.socket = "/run/audio_service/audio_service.sock";
    cfg.audio.sample_rate = 16000;
    cfg.audio.channels = 1;
    cfg.audio.bit_width = 16;

    cfg.hid.keyboard_device = "/dev/hidg0";
    cfg.hid.mouse_device = "/dev/hidg1";
    cfg.hid.frame_socket = "/run/frame_service/frame_service.sock";

    cfg.proxy.http_proxy = "http://127.0.0.1:7890";
    cfg.proxy.https_proxy = "http://127.0.0.1:7890";
    cfg.proxy.all_proxy = "socks5://127.0.0.1:7891";
    cfg.proxy.no_proxy = "localhost,127.0.0.1,192.168.0.0/16";

    cfg.search.provider = "duckduckgo";
    cfg.search.api_key = "tvly-test";

    std::string path = make_temp_path("roundtrip.toml");
    std::string err;
    REQUIRE(aiden::save_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());

    aiden::AgentToml loaded;
    REQUIRE(aiden::load_agent_toml(path.c_str(), loaded, &err));
    REQUIRE(err.empty());

    CHECK(loaded.instruction == "Hello \"world\"");
    CHECK(loaded.input_mode == "stt");
    CHECK(loaded.trigger_mode == "manual");
    CHECK(loaded.vad_backend == "cpu");
    CHECK(loaded.vad_model_path == "/userdata/agent/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn");
    CHECK(loaded.vad_helper_path == "/oem/usr/bin/rknn_vad");
    CHECK(loaded.vad_speech_threshold == doctest::Approx(0.5));
    CHECK(loaded.silence_ms == 1000);
    CHECK(loaded.min_speech_ms == 250);
    CHECK(loaded.voice_session_enabled == false);
    CHECK(loaded.voice_followup_timeout_ms == 6500);
    CHECK(loaded.voice_first_turn_timeout_ms == 11000);
    CHECK(loaded.voice_max_turns == 4);
    CHECK(loaded.voice_interrupt_on_wakeup == false);
    CHECK(loaded.voice_streaming_tts_enabled == false);
    CHECK(loaded.voice_tool_call_speech == false);
    CHECK(loaded.voice_max_response_tokens == 240);
    CHECK(loaded.max_iterations == 6);

    CHECK(loaded.model.provider == "openrouter");
    CHECK(loaded.model.model == "openai/gpt-4o-mini");
    CHECK(loaded.model.api_key == "sk-or-test");
    CHECK(loaded.model.token_env == "OPENROUTER_API_KEY");
    CHECK(loaded.model.temperature == doctest::Approx(0.2));
    CHECK(loaded.model.max_tokens == 1000);

    CHECK(loaded.tts.provider == "minimax");
    CHECK(loaded.tts.api_key == "mx-test");
    CHECK(loaded.tts.voice_id == "male-qn-qingse");
    CHECK(loaded.tts.emotion == "happy");
    CHECK(loaded.tts.speed == doctest::Approx(1.25));

    CHECK(loaded.stt.provider == "openai-whisper");
    CHECK(loaded.stt.api_key == "sk-stt");
    CHECK(loaded.stt.model == "whisper-1");

    CHECK(loaded.audio.socket == "/run/audio_service/audio_service.sock");
    CHECK(loaded.audio.sample_rate == 16000);
    CHECK(loaded.audio.channels == 1);
    CHECK(loaded.audio.bit_width == 16);

    CHECK(loaded.hid.keyboard_device == "/dev/hidg0");
    CHECK(loaded.hid.mouse_device == "/dev/hidg1");
    CHECK(loaded.hid.frame_socket == "/run/frame_service/frame_service.sock");

    CHECK(loaded.proxy.http_proxy == "http://127.0.0.1:7890");
    CHECK(loaded.proxy.https_proxy == "http://127.0.0.1:7890");
    CHECK(loaded.proxy.all_proxy == "socks5://127.0.0.1:7891");
    CHECK(loaded.proxy.no_proxy == "localhost,127.0.0.1,192.168.0.0/16");

    CHECK(loaded.search.provider == "duckduckgo");
    CHECK(loaded.search.api_key == "tvly-test");

    std::remove(path.c_str());
}

TEST_CASE("agent_toml ignores unknown sections and keys") {
    std::string path = make_temp_path("unknown.toml");
    {
        std::ofstream out(path);
        out << "input_mode = \"text\"\n"
            << "future_field = 7\n"
            << "[unknown]\n"
            << "x = 42\n"
            << "[model]\n"
            << "provider = \"openai\"\n"
            << "model = \"gpt-4o-mini\"\n"
            << "future = \"who-knows\"\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());
    CHECK(cfg.input_mode == "text");
    CHECK(cfg.max_iterations == -1);
    CHECK(cfg.model.provider == "openai");
    CHECK(cfg.model.model == "gpt-4o-mini");

    std::remove(path.c_str());
}

TEST_CASE("agent_toml strips inline comments after unquoted scalars") {
    std::string path = make_temp_path("comments.toml");
    {
        std::ofstream out(path);
        out << "vad_speech_threshold = 0.5   # threshold comment\n"
            << "[model]\n"
            << "provider = \"openrouter\"   # inline\n"
            << "model = \"x/y\"  # ok\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());
    CHECK(cfg.vad_speech_threshold == doctest::Approx(0.5));
    CHECK(cfg.model.provider == "openrouter");
    CHECK(cfg.model.model == "x/y");

    std::remove(path.c_str());
}
