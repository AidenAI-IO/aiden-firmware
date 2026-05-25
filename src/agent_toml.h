#pragma once

#include <string>

namespace aiden {

struct ModelToml {
    std::string provider;
    std::string model;
    std::string base_url;
    std::string api_key;
    std::string token_env;
    double temperature = 0.0;
    int max_tokens = 0;
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

struct HIDToml {
    std::string keyboard_device;
    std::string mouse_device;
    std::string frame_socket;
};

struct ProxyToml {
    std::string http_proxy;
    std::string https_proxy;
    std::string all_proxy;
    std::string no_proxy;
};

struct SearchToml {
    std::string provider;
    std::string api_key;
};

struct AgentToml {
    ModelToml model;
    ModelToml model_text;
    TTSToml tts;
    STTToml stt;
    AudioToml audio;
    HIDToml hid;
    ProxyToml proxy;
    SearchToml search;

    std::string instruction;
    std::string additional_prompt;
    std::string input_mode;
    std::string trigger_mode;
    int energy_threshold = 0;
    int silence_ms = 0;
    int min_speech_ms = 0;
    bool voice_session_enabled = true;
    int voice_followup_timeout_ms = 6000;
    int voice_first_turn_timeout_ms = 10000;
    int voice_max_turns = 0;
    bool voice_interrupt_on_wakeup = true;
    bool voice_streaming_tts_enabled = true;
    bool voice_tool_call_speech = true;
    int voice_max_response_tokens = 400;
    int max_iterations = -1;
};

bool load_agent_toml(const char* path, AgentToml& config, std::string* error = nullptr);
bool save_agent_toml(const char* path, const AgentToml& config, std::string* error = nullptr);

}
