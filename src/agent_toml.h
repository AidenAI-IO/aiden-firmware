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
    std::string api_key;
    std::string model;
    std::string base_url;
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

struct BenchmarkToml {
    std::string judge_model;
    std::string api_key;
    std::string benchmark_dir;
};

struct HIDToml {
    std::string keyboard_device;
    std::string mouse_device;
    std::string frame_socket;
    std::string pointer_mode;
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

struct AgentToml {
    ModelToml model;
    ModelToml model_text;
    TTSToml tts;
    STTToml stt;
    AudioToml audio;
    AudioArchiveToml audio_archive;
    BenchmarkToml benchmark;
    HIDToml hid;
    SearchToml search;
    TelemetryToml telemetry;

    std::string instruction;
    std::string additional_prompt;
    std::string input_mode;
    std::string trigger_mode;
    std::string vad_backend;
    std::string vad_model_path;
    std::string vad_helper_path;
    double vad_speech_threshold = 0.0;
    int silence_ms = 0;
    int min_speech_ms = 0;
    bool voice_session_enabled = true;
    int voice_followup_timeout_ms = 6000;
    int voice_first_turn_timeout_ms = 10000;
    int voice_max_turns = 0;
    bool voice_interrupt_on_wakeup = true;
    bool voice_streaming_tts_enabled = true;
    bool voice_tool_call_speech = false;
    bool voice_speech_summary_enabled = true;
    int voice_speech_max_runes = 120;
    int voice_max_response_tokens = 400;
    int max_iterations = -1;
    bool force_simple_loop = false;
    int screenshot_keep_n = 3;
    int screenshot_prune_interval = 25;
    int screen_stable_timeout_ms = 3500;
    int screen_stable_ms = 500;
    double screen_stable_diff_threshold = 2.0;
};

bool load_agent_toml(const char* path, AgentToml& config, std::string* error = nullptr);
bool save_agent_toml(const char* path, const AgentToml& config, std::string* error = nullptr);

}
