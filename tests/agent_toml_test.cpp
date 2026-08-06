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
    cfg.locale = "en-US";
    cfg.custom_instruction = "Hello \"world\"";
    cfg.input_mode = "stt";
    cfg.trigger_mode = "manual";
    cfg.vad_backend = "cpu";
    cfg.vad_model_path = "/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn";
    cfg.vad_helper_path = "/oem/usr/bin/rknn_vad";
    cfg.vad_speech_threshold = 0.5;
    cfg.silence_ms = 1000;
    cfg.min_speech_ms = 250;
    cfg.voice_followup_enabled = true;
    cfg.voice_followup_timeout_ms = 6500;
    cfg.voice_first_turn_timeout_ms = 11000;
    cfg.voice_max_turns = 4;
    cfg.voice_interrupt_on_wakeup = false;
    cfg.voice_streaming_tts_enabled = false;
    cfg.voice_tool_call_speech = false;
    cfg.voice_progress_speech_enabled = false;
    cfg.voice_max_response_tokens = 240;
    cfg.load_all_tools = true;
    cfg.max_iterations = 6;
    cfg.termination_policy.enabled = false;
    cfg.termination_policy.max_seconds = 12.5;
    cfg.termination_policy.repeat_action_limit = 7;
    cfg.termination_policy.same_result_limit = 8;
    cfg.termination_policy.screen_unchanged_limit = 9;
    cfg.termination_policy.soft_notice_stall_score = 10;
    cfg.termination_policy.restrict_tools_stall_score = 11;
    cfg.termination_policy.terminate_stall_score = 12;
    cfg.termination_policy.parse_failure_limit = 13;
    cfg.screenshot_keep_n = 5;
    cfg.screenshot_prune_interval = 40;
    cfg.screen_stable_timeout_ms = 4500;
    cfg.screen_stable_ms = 700;
    cfg.screen_stable_diff_threshold = 2.5;
    cfg.locale = "en-US";

    cfg.model.provider = "openrouter";
    cfg.model.model = "openai/gpt-4o-mini";
    cfg.model.api_key = "sk-or-test";
    cfg.model.temperature = 0.2;
    cfg.model.has_temperature = true;
    cfg.model.max_response_tokens = 1000;
    cfg.model.context_window = 64000;
    cfg.model.model_max_output_tokens = 4096;

    cfg.tts.provider = "minimax-cn";
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
    cfg.audio.playback_backend = "local";

    cfg.audio_archive.enabled = false;
    cfg.audio_archive.max_files = 42;
    cfg.audio_archive.max_size_mb = 17;
    cfg.audio_archive.storage_path = "/tmp/audio-archive";

    cfg.hid.keyboard_device = "/dev/hidg0";
    cfg.hid.keyboard_layout = "azerty";
    cfg.hid.mouse_device = "/dev/hidg1";
    cfg.hid.frame_socket = "/run/frame_service/frame_service.sock";
    cfg.device.device_type = "Android";

    cfg.search.provider = "duckduckgo";
    cfg.search.api_key = "tvly-test";

    cfg.telemetry.enabled = true;
    cfg.telemetry.provider = "langfuse";
    cfg.telemetry.base_url = "http://langfuse.example.com:3000";
    cfg.telemetry.public_key = "pk-lf-test";
    cfg.telemetry.secret_key = "sk-lf-test";
    cfg.telemetry.upload_screenshots = false;
    cfg.telemetry.upload_timeout_sec = 45;
    cfg.telemetry.max_retry = 3;
    cfg.telemetry.tags.push_back("aiden-hardware");
    cfg.telemetry.tags.push_back("field-test");
    cfg.telemetry.environment = "staging";

    cfg.live_activity.enabled = true;
    cfg.live_activity.relay_url = "https://relay.example.com";
    cfg.live_activity.relay_api_key = "relay-secret";
    cfg.live_activity.board_id = "board-001";
    cfg.live_activity.phone_id = "phone-001";
    cfg.live_activity.bundle_id = "com.aiden.bridge";
    cfg.live_activity.topic = "com.aiden.bridge.push-type.liveactivity";
    cfg.live_activity.environment = "production";
    cfg.live_activity.team_id = "TEAM123456";
    cfg.live_activity.key_id = "KEY123456";
    cfg.live_activity.private_key_path = "/userdata/agent/AuthKey_KEY123456.p8";
    cfg.live_activity.private_key_pem = "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----";
    cfg.live_activity.timeout_sec = 12;

    std::string path = make_temp_path("roundtrip.toml");
    std::string err;
    REQUIRE(aiden::save_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());

    {
        std::ifstream saved_in(path);
        REQUIRE(saved_in.good());
        std::string contents((std::istreambuf_iterator<char>(saved_in)), std::istreambuf_iterator<char>());
        CHECK(contents.find("locale = \"en-US\"") != std::string::npos);
        CHECK(contents.find("custom_instruction = \"Hello \\\"world\\\"\"") != std::string::npos);
        CHECK(contents.find("[device]") != std::string::npos);
        CHECK(contents.find("device_type = \"Android\"") != std::string::npos);
        CHECK(contents.find("pointer_mode") == std::string::npos);
        CHECK(contents.rfind("instruction =", 0) != 0);
        CHECK(contents.find("\ninstruction =") == std::string::npos);
    }

    aiden::AgentToml loaded;
    REQUIRE(aiden::load_agent_toml(path.c_str(), loaded, &err));
    REQUIRE(err.empty());

    CHECK(loaded.locale == "en-US");
    CHECK(loaded.custom_instruction == "Hello \"world\"");
    CHECK(loaded.input_mode == "stt");
    CHECK(loaded.trigger_mode == "manual");
    CHECK(loaded.vad_backend == "cpu");
    CHECK(loaded.vad_model_path == "/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn");
    CHECK(loaded.vad_helper_path == "/oem/usr/bin/rknn_vad");
    CHECK(loaded.vad_speech_threshold == doctest::Approx(0.5));
    CHECK(loaded.silence_ms == 1000);
    CHECK(loaded.min_speech_ms == 250);
    CHECK(loaded.voice_followup_enabled == true);
    CHECK(loaded.voice_followup_timeout_ms == 6500);
    CHECK(loaded.voice_first_turn_timeout_ms == 11000);
    CHECK(loaded.voice_max_turns == 4);
    CHECK(loaded.voice_interrupt_on_wakeup == false);
	CHECK(loaded.voice_streaming_tts_enabled == false);
	CHECK(loaded.voice_tool_call_speech == false);
	CHECK(loaded.voice_progress_speech_enabled == false);
	CHECK(loaded.voice_max_response_tokens == 240);
	CHECK(loaded.load_all_tools == true);
	CHECK(loaded.max_iterations == 6);
	CHECK(loaded.termination_policy.enabled == false);
	CHECK(loaded.termination_policy.max_seconds == doctest::Approx(12.5));
	CHECK(loaded.termination_policy.repeat_action_limit == 7);
	CHECK(loaded.termination_policy.same_result_limit == 8);
	CHECK(loaded.termination_policy.screen_unchanged_limit == 9);
	CHECK(loaded.termination_policy.soft_notice_stall_score == 10);
	CHECK(loaded.termination_policy.restrict_tools_stall_score == 11);
	CHECK(loaded.termination_policy.terminate_stall_score == 12);
	CHECK(loaded.termination_policy.parse_failure_limit == 13);
	CHECK(loaded.screenshot_keep_n == 5);
	CHECK(loaded.screenshot_prune_interval == 40);
	CHECK(loaded.screen_stable_timeout_ms == 4500);
	CHECK(loaded.screen_stable_ms == 700);
	CHECK(loaded.screen_stable_diff_threshold == doctest::Approx(2.5));

    CHECK(loaded.model.provider == "openrouter");
    CHECK(loaded.model.model == "openai/gpt-4o-mini");
    CHECK(loaded.model.api_key == "sk-or-test");
    CHECK(loaded.model.temperature == doctest::Approx(0.2));
    CHECK(loaded.model.max_response_tokens == 1000);
    CHECK(loaded.model.context_window == 64000);
    CHECK(loaded.model.model_max_output_tokens == 4096);

    // The flat voice credentials set above are written flat and then migrated
    // onto a [tts_providers]/[stt_providers] record at load, so they arrive on
    // the record rather than on the flat section. Nothing is lost -- which is
    // what this round-trip guards -- it just lands where the config page can
    // edit it. See agent_toml_voice_providers_test.cpp for the migration rules.
    CHECK(loaded.tts.provider == "minimax-cn");
    CHECK(loaded.tts_providers["minimax-cn"].api_key == "mx-test");
    CHECK(loaded.tts_providers["minimax-cn"].voice_id == "male-qn-qingse");
    CHECK(loaded.tts_providers["minimax-cn"].emotion == "happy");
    // speed stays flat: it is a listening preference, global by design.
    CHECK(loaded.tts.speed == doctest::Approx(1.25));

    CHECK(loaded.stt.provider == "openai-whisper");
    CHECK(loaded.stt_providers["openai-whisper"].api_key == "sk-stt");
    CHECK(loaded.stt_providers["openai-whisper"].model == "whisper-1");

    CHECK(loaded.audio.socket == "/run/audio_service/audio_service.sock");
    CHECK(loaded.audio.sample_rate == 16000);
    CHECK(loaded.audio.channels == 1);
    CHECK(loaded.audio.bit_width == 16);
    CHECK(loaded.audio.playback_backend == "local");

    CHECK(loaded.audio_archive.enabled == false);
    CHECK(loaded.audio_archive.max_files == 42);
    CHECK(loaded.audio_archive.max_size_mb == 17);
    CHECK(loaded.audio_archive.storage_path == "/tmp/audio-archive");
    CHECK(loaded.locale == "en-US");

    CHECK(loaded.hid.keyboard_device == "/dev/hidg0");
    CHECK(loaded.hid.keyboard_layout == "azerty");
    CHECK(loaded.hid.mouse_device == "/dev/hidg1");
    CHECK(loaded.hid.android_keyboard_device == "/dev/hidg2");
    CHECK(loaded.hid.frame_socket == "/run/frame_service/frame_service.sock");
    CHECK(loaded.device.device_type == "Android");

    CHECK(loaded.search.provider == "duckduckgo");
    CHECK(loaded.search.api_key == "tvly-test");

    CHECK(loaded.telemetry.enabled == true);
    CHECK(loaded.telemetry.provider == "langfuse");
    CHECK(loaded.telemetry.base_url == "http://langfuse.example.com:3000");
    CHECK(loaded.telemetry.public_key == "pk-lf-test");
    CHECK(loaded.telemetry.secret_key == "sk-lf-test");
    CHECK(loaded.telemetry.upload_screenshots == false);
    CHECK(loaded.telemetry.upload_timeout_sec == 45);
    CHECK(loaded.telemetry.max_retry == 3);
    REQUIRE(loaded.telemetry.tags.size() == 2);
    CHECK(loaded.telemetry.tags[0] == "aiden-hardware");
    CHECK(loaded.telemetry.tags[1] == "field-test");
    CHECK(loaded.telemetry.environment == "staging");

    CHECK(loaded.live_activity.enabled == true);
    CHECK(loaded.live_activity.relay_url == "https://relay.example.com");
    CHECK(loaded.live_activity.relay_api_key == "relay-secret");
    CHECK(loaded.live_activity.has_relay_api_key == true);
    CHECK(loaded.live_activity.board_id == "board-001");
    CHECK(loaded.live_activity.phone_id == "phone-001");
    CHECK(loaded.live_activity.bundle_id == "com.aiden.bridge");
    CHECK(loaded.live_activity.topic == "com.aiden.bridge.push-type.liveactivity");
    CHECK(loaded.live_activity.environment == "production");
    CHECK(loaded.live_activity.team_id == "TEAM123456");
    CHECK(loaded.live_activity.key_id == "KEY123456");
    CHECK(loaded.live_activity.private_key_path == "/userdata/agent/AuthKey_KEY123456.p8");
    CHECK(loaded.live_activity.private_key_pem == "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----");
    CHECK(loaded.live_activity.has_private_key_pem == true);
    CHECK(loaded.live_activity.timeout_sec == 12);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml defaults missing android keyboard device for old configs") {
    std::string path = make_temp_path("old_hid_defaults.toml");
    {
        std::ofstream out(path);
        out << "[hid]\n"
            << "keyboard_device = \"/dev/hidg0\"\n"
            << "mouse_device = \"/dev/hidg1\"\n"
            << "frame_socket = \"/run/frame_service/frame_service.sock\"\n"
            << "pointer_mode = \"absolute\"\n";
    }

    aiden::AgentToml loaded;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), loaded, &err));
    REQUIRE(err.empty());
    CHECK(loaded.hid.android_keyboard_device == "/dev/hidg2");
    CHECK(loaded.hid.keyboard_layout == "qwerty");

    std::remove(path.c_str());
}

TEST_CASE("agent_toml round-trip preserves voice notification settings") {
    aiden::AgentToml cfg;
    cfg.voice_notifications.enabled = false;
    cfg.voice_notifications.max_pending = 6;
    cfg.voice_notifications.response_tail.enabled = false;
    cfg.voice_notifications.response_tail.max_items = 1;
    cfg.voice_notifications.response_tail.max_text_chars = 72;
    cfg.voice_notifications.expiration.default_ttl_seconds = 120;
    cfg.voice_notifications.expiration.code_ttl_seconds.clear();
    cfg.voice_notifications.expiration.code_ttl_seconds["network"] = 123;

    std::string path = make_temp_path("voice_notifications.toml");
    std::string err;
    REQUIRE(aiden::save_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());

    std::ifstream saved_in(path);
    REQUIRE(saved_in.good());
    std::string saved((std::istreambuf_iterator<char>(saved_in)), std::istreambuf_iterator<char>());
    CHECK(saved.find("default_locale") == std::string::npos);
    CHECK(saved.find("network = 123") != std::string::npos);
    CHECK(saved.find("storage = 900") == std::string::npos);

    aiden::AgentToml loaded;
    REQUIRE(aiden::load_agent_toml(path.c_str(), loaded, &err));
    REQUIRE(err.empty());
    CHECK(loaded.voice_notifications.enabled == false);
    CHECK(loaded.voice_notifications.max_pending == 6);
    CHECK(loaded.voice_notifications.response_tail.enabled == false);
    CHECK(loaded.voice_notifications.response_tail.max_items == 1);
    CHECK(loaded.voice_notifications.response_tail.max_text_chars == 72);
    CHECK(loaded.voice_notifications.expiration.default_ttl_seconds == 120);
    CHECK(loaded.voice_notifications.expiration.code_ttl_seconds.at("network") == 123);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml rejects invalid voice notification TTL code keys") {
    aiden::AgentToml cfg;
    cfg.voice_notifications.expiration.code_ttl_seconds["bad key"] = 123;

    std::string path = make_temp_path("invalid_voice_notification_ttl_key.toml");
    std::string err;
    CHECK_FALSE(aiden::save_agent_toml(path.c_str(), cfg, &err));
    CHECK(err.find("expected a bare TOML key") != std::string::npos);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml rejects negative live_activity timeout") {
    std::string path = make_temp_path("negative_live_activity_timeout.toml");
    {
        std::ofstream out(path);
        out << "[live_activity]\n"
            << "timeout_sec = -1\n";
    }

    aiden::AgentToml loaded;
    std::string err;
    CHECK_FALSE(aiden::load_agent_toml(path.c_str(), loaded, &err));
    CHECK(err.find("must be >= 0") != std::string::npos);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml no longer writes legacy proxy section") {
    aiden::AgentToml cfg;
    cfg.model.provider = "fake";
    cfg.search.provider = "duckduckgo";

    std::string path = make_temp_path("no_proxy.toml");
    std::string err;
    REQUIRE(aiden::save_agent_toml(path.c_str(), cfg, &err));

    std::ifstream in(path);
    REQUIRE(in.good());
    std::string contents((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
    CHECK(contents.find("[proxy]") == std::string::npos);
    CHECK(contents.find("http_proxy") == std::string::npos);
    CHECK(contents.find("[benchmark]") == std::string::npos);
    CHECK(contents.find("judge_model") == std::string::npos);
    CHECK(contents.find("benchmark_dir") == std::string::npos);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml ignores legacy instruction key") {
    std::string path = make_temp_path("legacy_instruction.toml");
    {
        std::ofstream out(path);
        out << "instruction = \"legacy value\"\n"
            << "custom_instruction = \"custom value\"\n"
            << "[model]\n"
            << "provider = \"fake\"\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());
    CHECK(cfg.custom_instruction == "custom value");

    std::remove(path.c_str());
}

TEST_CASE("agent_toml accepts legacy model max_tokens") {
    std::string path = make_temp_path("legacy_max_tokens.toml");
    {
        std::ofstream out(path);
        out << "[model]\n"
            << "provider = \"openrouter\"\n"
            << "model = \"vendor/test-model\"\n"
            << "max_tokens = 777\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());
    CHECK(cfg.model.max_response_tokens == 777);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml prefers max_response_tokens over legacy max_tokens") {
    std::string path = make_temp_path("legacy_max_tokens_precedence.toml");
    {
        std::ofstream out(path);
        out << "[model]\n"
            << "provider = \"openrouter\"\n"
            << "model = \"vendor/test-model\"\n"
            << "max_response_tokens = 1000\n"
            << "max_tokens = 777\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());
    CHECK(cfg.model.max_response_tokens == 1000);

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

TEST_CASE("agent_toml ignores legacy voice speech max runes") {
    std::string path = make_temp_path("legacy_voice_speech_max_runes.toml");
    {
        std::ofstream out(path);
        out << "voice_speech_max_runes = -1\n";
        out << "voice_max_response_tokens = 240\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    CHECK(err.empty());
    CHECK(cfg.voice_max_response_tokens == 240);

    std::remove(path.c_str());
}

TEST_CASE("agent_toml rejects negative model metadata overrides") {
    struct Case {
        const char* leaf;
        const char* section;
        const char* field;
    };
    const Case cases[] = {
        {"negative_context_window.toml", "model", "context_window"},
        {"negative_model_max_output_tokens.toml", "model_text", "model_max_output_tokens"},
    };

    for (const auto& tc : cases) {
        std::string path = make_temp_path(tc.leaf);
        {
            std::ofstream out(path);
            out << "[" << tc.section << "]\n"
                << "provider = \"fake\"\n"
                << tc.field << " = -1\n";
        }

        aiden::AgentToml cfg;
        std::string err;
        CHECK_FALSE(aiden::load_agent_toml(path.c_str(), cfg, &err));
        CHECK(err.find(tc.field) != std::string::npos);
        CHECK(err.find(">= 0") != std::string::npos);

        std::remove(path.c_str());
    }
}

TEST_CASE("agent_toml rejects negative model response token limits") {
    struct Case {
        const char* leaf;
        const char* section;
        const char* field;
    };
    const Case cases[] = {
        {"negative_max_response_tokens.toml", "model", "max_response_tokens"},
        {"negative_legacy_max_tokens.toml", "model_text", "max_tokens"},
    };

    for (const auto& tc : cases) {
        std::string path = make_temp_path(tc.leaf);
        {
            std::ofstream out(path);
            out << "[" << tc.section << "]\n"
                << "provider = \"fake\"\n"
                << tc.field << " = -1\n";
        }

        aiden::AgentToml cfg;
        std::string err;
        CHECK_FALSE(aiden::load_agent_toml(path.c_str(), cfg, &err));
        CHECK(err.find(tc.field) != std::string::npos);
        CHECK(err.find(">= 0") != std::string::npos);

        std::remove(path.c_str());
    }
}

TEST_CASE("agent_toml loads named providers") {
    std::string path = make_temp_path("providers.toml");
    {
        std::ofstream out(path);
        out << "[providers.openai-work]\n"
            << "provider = \"openai\"\n"
            << "api_key = \"sk-work\"\n"
            << "[providers.ollama_local]\n"
            << "provider = \"ollama\"\n"
            << "base_url = \"http://127.0.0.1:11434\"\n"
            << "token_env = \"OLLAMA_TOKEN\"\n"
            << "[model]\n"
            << "provider = \"openai-work\"\n";
    }

    aiden::AgentToml cfg;
    std::string err;
    REQUIRE(aiden::load_agent_toml(path.c_str(), cfg, &err));
    REQUIRE(err.empty());
    REQUIRE(cfg.providers.size() == 2);
    CHECK(cfg.providers["openai-work"].provider == "openai");
    CHECK(cfg.providers["openai-work"].api_key == "sk-work");
    CHECK(cfg.providers["ollama_local"].provider == "ollama");
    CHECK(cfg.providers["ollama_local"].base_url == "http://127.0.0.1:11434");
    CHECK(cfg.providers["ollama_local"].token_env == "OLLAMA_TOKEN");
    CHECK(cfg.model.provider == "openai-work");

    std::remove(path.c_str());
}

// A provider name that save_agent_toml would refuse must be rejected at load
// time too. Accepting it would produce a config that loads cleanly but fails
// every later save with "invalid provider name", and the empty-name case used
// to insert a providers[""] entry into the caller's struct.
TEST_CASE("agent_toml rejects provider names it cannot write back") {
    struct Case {
        const char* leaf;
        const char* section;
        const char* expected;
    };
    const Case cases[] = {
        {"empty_provider_name.toml", "providers.", "empty provider name"},
        {"dotted_provider_name.toml", "providers.a.b", "invalid provider name"},
        {"spaced_provider_name.toml", "providers.my provider", "invalid provider name"},
    };

    for (const auto& tc : cases) {
        std::string path = make_temp_path(tc.leaf);
        {
            std::ofstream out(path);
            out << "[" << tc.section << "]\n"
                << "provider = \"openai\"\n";
        }

        aiden::AgentToml cfg;
        std::string err;
        CHECK_FALSE(aiden::load_agent_toml(path.c_str(), cfg, &err));
        CHECK(err.find(tc.expected) != std::string::npos);
        CHECK(cfg.providers.empty());

        std::remove(path.c_str());
    }
}
