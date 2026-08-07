// Minimal `agent` CLI replacement used by config_web E2E tests. Behaviour is
// controlled by environment variables so the test driver can flip a single
// stub between scenarios (valid metadata, broken metadata, valid check,
// rejecting check, hung process, ...) without rebuilding.
//
// Variables:
//   AIDEN_AGENT_STUB_META_FILE   path to a file whose contents are written
//                                verbatim to stdout for `config-meta`. If
//                                unset, prints a minimal valid metadata
//                                document.
//   AIDEN_AGENT_STUB_META_EXIT   integer exit code for `config-meta`
//                                (default 0).
//   AIDEN_AGENT_STUB_CHECK_FILE  path to a file whose contents are written
//                                verbatim to stdout for `config-check`. If
//                                unset, prints {"valid": true, "errors": []}.
//   AIDEN_AGENT_STUB_CHECK_EXIT  integer exit code for `config-check`
//                                (default 0).
//   AIDEN_AGENT_STUB_CONFIG_FILE   path to a file whose contents are written
//                                  verbatim to stdout for `config`.
//                                  If unset, prints a minimal resolved config.
//   AIDEN_AGENT_STUB_CONFIG_EXIT   integer exit code for `config` (default 0).
//   AIDEN_AGENT_STUB_CONFIG_TEST_LOG path where `config-test` writes argv and
//                                    stdin for assertions.
//   AIDEN_AGENT_STUB_CONFIG_TEST_FILE path to stdout payload for `config-test`.
//                                    If unset, prints a passing TTS result.
//   AIDEN_AGENT_STUB_CONFIG_TEST_STDERR text emitted before the JSON result.
//   AIDEN_AGENT_STUB_CONFIG_TEST_EXIT integer exit code for `config-test`
//                                    (default 0).
//   AIDEN_AGENT_STUB_SLEEP_MS    if set, sleep this many ms before producing
//                                output -- used to exercise the timeout path.
//
// Unknown subcommands exit 99 with empty stdout.

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <sstream>
#include <string>
#include <thread>
#include <chrono>

namespace {

const char* kDefaultMeta =
    "{\"sections\":["
    "{\"name\":\"agent\",\"fields\":["
    "{\"key\":\"input_mode\",\"widget\":\"select\","
    "\"enum\":[{\"value\":\"text\"}],\"default\":\"text\"}"
    "]}"
    "]}\n";

const char* kDefaultCheck = "{\"valid\":true,\"errors\":[]}\n";

const char* kDefaultConfig =
    "{"
    "\"model_providers\":{\"stub-openai\":{\"type\":\"openai\",\"api_key\":\"sk-stub-secret-1234\"},"
    "\"stub-ollama\":{\"type\":\"ollama\",\"base_url\":\"http://127.0.0.1:11434\"}},"
    "\"model\":{\"provider\":\"openrouter\",\"api_key\":\"\",\"model\":\"bytedance-seed/seed-2.0-lite\","
    "\"base_url\":\"\",\"temperature\":0.2,\"max_response_tokens\":1000,"
    "\"context_window\":0,\"model_max_output_tokens\":0},"
    "\"tts_providers\":{\"minimax-cn\":{\"type\":\"minimax-cn\"}},"
    "\"stt_providers\":{\"openai-whisper\":{\"type\":\"openai-whisper\"}},"
    "\"tts\":{\"provider\":\"minimax-cn\",\"api_key\":\"\",\"model\":\"\",\"voice_id\":\"male-qn-qingse\","
    "\"emotion\":\"happy\",\"speed\":1},"
    "\"stt\":{\"provider\":\"openai-whisper\",\"api_key\":\"\",\"model\":\"whisper-1\",\"base_url\":\"\","
    "\"app_id\":\"\",\"secret_id\":\"\",\"secret_key\":\"\",\"region\":\"\",\"engine_model_type\":\"\"},"
    "\"audio\":{\"socket\":\"/run/audio_service/audio_service.sock\",\"sample_rate\":16000,"
    "\"channels\":1,\"bit_width\":16},"
    "\"audio_archive\":{\"enabled\":true,\"max_files\":500,\"max_size_mb\":100,"
    "\"storage_path\":\"/userdata/audio\"},"
    "\"voice_notifications\":{\"enabled\":true,\"max_pending\":8,"
    "\"response_tail\":{\"enabled\":true,\"max_items\":1,\"max_text_chars\":40},"
    "\"expiration\":{\"default_ttl_seconds\":0,\"code_ttl_seconds\":{\"storage\":900}}},"
    "\"ota\":{\"github_proxy_url\":\"\"},"
    "\"hid\":{\"keyboard_device\":\"/dev/hidg0\",\"keyboard_layout\":\"qwerty\",\"mouse_device\":\"/dev/hidg1\","
    "\"android_keyboard_device\":\"/dev/hidg2\","
    "\"frame_socket\":\"/run/frame_service/frame_service.sock\",\"pointer_mode\":\"absolute\"},"
    "\"search\":{\"provider\":\"duckduckgo\",\"has_api_key\":false},"
    "\"telemetry\":{\"enabled\":false,\"provider\":\"langfuse\",\"base_url\":\"\","
    "\"public_key\":\"\",\"secret_key\":\"\",\"upload_screenshots\":true,"
    "\"upload_timeout_sec\":30,\"max_retry\":2,\"tags\":[],\"environment\":\"default\"},"
    "\"agent\":{\"custom_instruction\":\"stub custom instruction\",\"additional_prompt\":\"\","
    "\"input_mode\":\"text\",\"trigger_mode\":\"manual\",\"vad_backend\":\"rknn\","
    "\"vad_model_path\":\"/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn\","
    "\"vad_helper_path\":\"/oem/usr/bin/rknn_vad\",\"vad_speech_threshold\":0.5,"
    "\"silence_ms\":650,\"min_speech_ms\":300,\"voice_followup_enabled\":false,"
    "\"voice_followup_timeout_ms\":6000,\"voice_first_turn_timeout_ms\":10000,"
    "\"voice_max_turns\":0,\"voice_interrupt_on_wakeup\":true,"
    "\"voice_streaming_tts_enabled\":true,\"voice_tool_call_speech\":true,"
    "\"voice_max_response_tokens\":300,\"load_all_tools\":false,\"max_iterations\":-1,"
    "\"screenshot_keep_n\":3,"
    "\"screenshot_prune_interval\":2,\"screen_stable_timeout_ms\":3500,"
    "\"screen_stable_ms\":500,\"screen_stable_diff_threshold\":2}"
    "}\n";

const char* kDefaultConfigTest =
    "{\"ok\":true,\"results\":[{\"check\":\"tts_playback\",\"passed\":true,"
    "\"detail\":\"played test passed\"}]}\n";

void maybe_sleep() {
    const char* sleep_ms = std::getenv("AIDEN_AGENT_STUB_SLEEP_MS");
    if (!sleep_ms || sleep_ms[0] == '\0') {
        return;
    }
    int ms = std::atoi(sleep_ms);
    if (ms > 0) {
        std::this_thread::sleep_for(std::chrono::milliseconds(ms));
    }
}

bool write_file_contents(const char* env_var, const char* default_text) {
    const char* path = std::getenv(env_var);
    if (!path || path[0] == '\0') {
        std::fputs(default_text, stdout);
        return true;
    }
    std::ifstream in(path);
    if (!in.good()) {
        std::fprintf(stderr, "stub: failed to open %s=%s\n", env_var, path);
        return false;
    }
    std::ostringstream buf;
    buf << in.rdbuf();
    std::fputs(buf.str().c_str(), stdout);
    return true;
}

void maybe_write_config_test_log(int argc, char** argv, const std::string& stdin_body) {
    const char* path = std::getenv("AIDEN_AGENT_STUB_CONFIG_TEST_LOG");
    if (!path || path[0] == '\0') {
        return;
    }
    std::ofstream out(path);
    if (!out.good()) {
        return;
    }
    out << "argv:";
    for (int i = 1; i < argc; ++i) {
        out << "\n" << argv[i];
    }
    out << "\nstdin:\n" << stdin_body;
}

int env_int(const char* var, int fallback) {
    const char* val = std::getenv(var);
    if (!val || val[0] == '\0') {
        return fallback;
    }
    return std::atoi(val);
}

}  // namespace

int main(int argc, char** argv) {
    if (argc < 2) {
        return 99;
    }
    const std::string sub = argv[1];

    if (sub == "config-meta") {
        maybe_sleep();
        if (!write_file_contents("AIDEN_AGENT_STUB_META_FILE", kDefaultMeta)) {
            return 1;
        }
        std::fflush(stdout);
        return env_int("AIDEN_AGENT_STUB_META_EXIT", 0);
    }

    if (sub == "config-check") {
        // Drain stdin so config_web's writer doesn't see EPIPE before we
        // respond. We don't actually inspect the payload -- the test driver
        // controls what we report via the env file.
        char buf[4096];
        while (std::fread(buf, 1, sizeof(buf), stdin) > 0) {
        }
        maybe_sleep();
        if (!write_file_contents("AIDEN_AGENT_STUB_CHECK_FILE", kDefaultCheck)) {
            return 1;
        }
        std::fflush(stdout);
        return env_int("AIDEN_AGENT_STUB_CHECK_EXIT", 0);
    }

    if (sub == "config") {
        maybe_sleep();
        if (!write_file_contents("AIDEN_AGENT_STUB_CONFIG_FILE", kDefaultConfig)) {
            return 1;
        }
        std::fflush(stdout);
        return env_int("AIDEN_AGENT_STUB_CONFIG_EXIT", 0);
    }

    if (sub == "config-test") {
        std::ostringstream body;
        char buf[4096];
        size_t n = 0;
        while ((n = std::fread(buf, 1, sizeof(buf), stdin)) > 0) {
            body.write(buf, static_cast<std::streamsize>(n));
        }
        maybe_write_config_test_log(argc, argv, body.str());
        maybe_sleep();
        const char* stderr_text = std::getenv("AIDEN_AGENT_STUB_CONFIG_TEST_STDERR");
        if (stderr_text && stderr_text[0] != '\0') {
            std::fprintf(stderr, "%s\n", stderr_text);
            std::fflush(stderr);
        }
        if (!write_file_contents("AIDEN_AGENT_STUB_CONFIG_TEST_FILE", kDefaultConfigTest)) {
            return 1;
        }
        std::fflush(stdout);
        return env_int("AIDEN_AGENT_STUB_CONFIG_TEST_EXIT", 0);
    }

    return 99;
}
