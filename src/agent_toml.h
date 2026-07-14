#pragma once

#include <string>
#include <vector>

namespace aiden {

struct ModelToml {
    std::string provider;
    std::string model;
    std::string base_url;
    std::string api_key;
    std::string token_env;
    std::string reasoning_effort;
    double temperature = 0.0;
    int max_response_tokens = 0;
    int context_window = 0;
    int model_max_output_tokens = 0;
};

struct TTSToml {
    std::string provider;
    std::string api_key;
    std::string model;
    std::string voice_id;
    std::string emotion;
    double speed = 1.0;
};

struct STTToml {
    std::string provider;
    std::string language;
    std::string api_key;
    std::string model;
    std::string base_url;
    std::string app_id;
    std::string secret_id;
    std::string secret_key;
    std::string region;
    std::string engine_model_type;
};

struct AudioToml {
    std::string socket;
    int sample_rate = 0;
    int channels = 0;
    int bit_width = 0;
};

struct AudioArchiveToml {
    bool enabled = true;
    int max_files = 500;
    int max_size_mb = 100;
    std::string storage_path = "/userdata/audio";
};

struct LogToml {
    int llm_http_retention_days = 7;
};

struct HIDToml {
    std::string keyboard_device = "/dev/hidg0";
    std::string mouse_device = "/dev/hidg1";
    std::string android_keyboard_device = "/dev/hidg2";
    std::string frame_socket = "/run/frame_service/frame_service.sock";
    std::string pointer_mode = "absolute";
};

struct SearchToml {
    std::string provider;
    std::string api_key;
    bool has_api_key = false;
};

struct TelemetryToml {
    bool enabled = false;
    std::string provider = "langfuse";
    std::string base_url;
    std::string public_key;
    std::string secret_key;
    bool upload_screenshots = true;
    int upload_timeout_sec = 30;
    int max_retry = 2;
    std::vector<std::string> tags;
    std::string environment = "default";
};

struct LiveActivityToml {
    bool enabled = true;
    std::string relay_url;
    std::string relay_api_key;
    bool has_relay_api_key = false;
    std::string board_id;
    std::string phone_id;
    std::string bundle_id;
    std::string topic;
    std::string environment = "sandbox";
    std::string team_id;
    std::string key_id;
    std::string private_key_path;
    std::string private_key_pem;
    bool has_private_key_pem = false;
    int timeout_sec = 0;
};

struct TerminationPolicyToml {
    bool enabled = true;
    double max_seconds = 0.0;
    int repeat_action_limit = 3;
    int same_result_limit = 3;
    int screen_unchanged_limit = 5;
    int soft_notice_stall_score = 2;
    int restrict_tools_stall_score = 4;
    int terminate_stall_score = 6;
    int parse_failure_limit = 3;
};

struct AgentToml {
    ModelToml model;
    ModelToml model_text;
    TTSToml tts;
    STTToml stt;
    AudioToml audio;
    AudioArchiveToml audio_archive;
    LogToml log;
    HIDToml hid;
    SearchToml search;
    TelemetryToml telemetry;
    LiveActivityToml live_activity;
    TerminationPolicyToml termination_policy;

    std::string custom_instruction;
    std::string additional_prompt;
    std::string input_mode;
    std::string trigger_mode;
    std::string vad_backend;
    std::string vad_model_path;
    std::string vad_helper_path;
    double vad_speech_threshold = 0.0;
    int silence_ms = 0;
    int min_speech_ms = 0;
    bool voice_followup_enabled = false;
    int voice_followup_timeout_ms = 6000;
    int voice_first_turn_timeout_ms = 10000;
    int voice_max_turns = 0;
    bool voice_interrupt_on_wakeup = true;
    bool voice_streaming_tts_enabled = true;
    bool voice_tool_call_speech = true;
    bool voice_progress_speech_enabled = true;
    int voice_max_response_tokens = 300;
    bool load_all_tools = false;
    int max_iterations = -1;
    int screenshot_keep_n = 3;
    int screenshot_prune_interval = 2;
    int screen_stable_timeout_ms = 3500;
    int screen_stable_ms = 500;
    double screen_stable_diff_threshold = 2.0;
    std::string default_platform;
};

bool load_agent_toml(const char* path, AgentToml& config, std::string* error = nullptr);
bool save_agent_toml(const char* path, const AgentToml& config, std::string* error = nullptr);

}
