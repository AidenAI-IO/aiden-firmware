#pragma once

namespace aiden {

struct AgentConfig {
    char api_key[256];
    char llm_model[128];
    char tts_model[128];
    char minimax_api_key[256];
    char minimax_voice_id[128];
    char minimax_emotion[64];
    char hid_binary[256];
    char additional_prompt[2048];
    int energy_threshold;
    int silence_ms;
    int min_speech_ms;
    float minimax_speed;

    AgentConfig() {
        api_key[0] = '\0';
        llm_model[0] = '\0';
        tts_model[0] = '\0';
        minimax_api_key[0] = '\0';
        minimax_voice_id[0] = '\0';
        minimax_emotion[0] = '\0';
        hid_binary[0] = '\0';
        additional_prompt[0] = '\0';
        energy_threshold = 300;
        silence_ms = 800;
        min_speech_ms = 300;
        minimax_speed = 1.0f;
    }
};

bool load_config(const char* path, AgentConfig& config);

}
