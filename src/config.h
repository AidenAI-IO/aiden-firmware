#pragma once

#include <string>

namespace aiden {

struct ModelConfig {
    char provider[32];
    char api_key[256];
    char model[128];
    char base_url[256];

    ModelConfig() {
        provider[0] = '\0';
        api_key[0] = '\0';
        model[0] = '\0';
        base_url[0] = '\0';
    }
};

struct TTSConfig {
    char provider[32];
    char api_key[256];
    char model[128];
    char voice_id[128];
    char emotion[64];
    float speed;

    TTSConfig() {
        provider[0] = '\0';
        api_key[0] = '\0';
        model[0] = '\0';
        voice_id[0] = '\0';
        emotion[0] = '\0';
        speed = 1.0f;
    }
};

struct AgentConfig {
    ModelConfig model;
    TTSConfig tts;
    char hid_binary[256];
    char additional_prompt[2048];
    int energy_threshold;
    int silence_ms;
    int min_speech_ms;

    AgentConfig() {
        hid_binary[0] = '\0';
        additional_prompt[0] = '\0';
        energy_threshold = 300;
        silence_ms = 800;
        min_speech_ms = 300;
    }
};

bool load_config(const char* path, AgentConfig& config, std::string* error = nullptr);

}
