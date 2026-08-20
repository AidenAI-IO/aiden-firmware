#pragma once

#include <map>
#include <string>
#include <vector>

namespace aiden {

struct ModelToml {
    std::string provider;
    std::string model;
    std::string base_url;
    std::string api_key;
    std::string api_mode;
    std::string reasoning_effort;
    double temperature = 0.0;
    bool has_temperature = false;
    int max_response_tokens = 0;
    int context_window = 0;
    int model_max_output_tokens = 0;
};

struct ModelProviderToml {
    std::string type;
    std::string api_key;
    std::string base_url;
};

// TTSProviderToml is one [tts_providers.<name>] record: a provider type plus the
// credentials and voice settings that only mean anything for that type. [tts]
// references one by name, so several stay configured at once.
//
// speed is absent on purpose: it is a listening preference that must not change
// when the voice changes, so it stays global on [tts].
struct TTSProviderToml {
    std::string type;
    std::string api_key;
    std::string model;
    std::string voice_id;
    std::string emotion;
    std::string reference_id;
};

// STTProviderToml is one [stt_providers.<name>] record. language stays on [stt]:
// it holds regardless of which provider transcribes.
struct STTProviderToml {
    std::string type;
    std::string api_key;
    std::string model;
    std::string base_url;
    std::string app_id;
    std::string secret_id;
    std::string secret_key;
    std::string region;
    std::string engine_model_type;
};

struct TTSToml {
    std::string provider;
    std::string api_key;
    std::string model;
    std::string voice_id;
    std::string reference_id;
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
    std::string playback_backend;
};

struct AudioArchiveToml {
    bool enabled = true;
    int max_files = 500;
    int max_size_mb = 100;
    std::string storage_path = "/userdata/audio";
};

struct QuickCaptureToml {
    bool enabled = true;
    int gpio_pin = 0;
    std::string screen_memory_ttl = "90d";
};

struct VoiceNotificationResponseTailToml {
    bool enabled = true;
    int max_items = 1;
    int max_text_chars = 40;
};

struct VoiceNotificationExpirationToml {
    int default_ttl_seconds = 0;
    std::map<std::string, int> code_ttl_seconds{{"storage", 900}};
};

struct VoiceNotificationsToml {
    bool enabled = true;
    int max_pending = 8;
    VoiceNotificationResponseTailToml response_tail;
    VoiceNotificationExpirationToml expiration;
};

struct LogToml {
    int llm_http_retention_days = 7;
};

struct OTAToml {
    std::string github_proxy_url;
};

struct HIDToml {
    std::string keyboard_device = "/dev/hidg0";
    std::string keyboard_layout = "qwerty";
    std::string mouse_device = "/dev/hidg1";
    std::string android_keyboard_device = "/dev/hidg2";
    std::string frame_socket = "/run/frame_service/frame_service.sock";
    std::string pointer_mode = "absolute";
    std::string input_backend = "hid";
};

struct DeviceToml {
    std::string backend;
    std::string device_type;
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
    std::map<std::string, ModelProviderToml> model_providers;
    std::map<std::string, TTSProviderToml> tts_providers;
    std::map<std::string, STTProviderToml> stt_providers;
    ModelToml model;
    TTSToml tts;
    STTToml stt;
    AudioToml audio;
    AudioArchiveToml audio_archive;
    QuickCaptureToml quick_capture;
    VoiceNotificationsToml voice_notifications;
    LogToml log;
    OTAToml ota;
    DeviceToml device;
    HIDToml hid;
    SearchToml search;
    TelemetryToml telemetry;
    LiveActivityToml live_activity;
    TerminationPolicyToml termination_policy;

    std::string locale = "zh-CN";
    std::string custom_instruction;
    std::string additional_prompt;
    std::string input_mode;
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
};

bool load_agent_toml(const char* path, AgentToml& config, std::string* error = nullptr);
void migrate_flat_voice_provider_fields(AgentToml& config);
bool save_agent_toml(const char* path, const AgentToml& config, std::string* error = nullptr);

}
