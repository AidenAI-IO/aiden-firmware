#include "config.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>

namespace aiden {

static void trim(char* s) {
    char* end = s + strlen(s) - 1;
    while (end > s && isspace(*end)) *end-- = '\0';
    char* start = s;
    while (*start && isspace(*start)) start++;
    if (start != s) memmove(s, start, strlen(start) + 1);
}

bool load_config(const char* path, AgentConfig& config) {
    FILE* fp = fopen(path, "r");
    if (!fp) return false;

    char line[512];
    char current_section[64] = "";

    while (fgets(line, sizeof(line), fp)) {
        trim(line);
        if (line[0] == '#' || line[0] == ';' || line[0] == '\0')
            continue;

        // Parse section header
        if (line[0] == '[') {
            char* end = strchr(line, ']');
            if (end) {
                *end = '\0';
                strncpy(current_section, line + 1, sizeof(current_section) - 1);
                current_section[sizeof(current_section) - 1] = '\0';
            }
            continue;
        }

        char* eq = strchr(line, '=');
        if (!eq) continue;

        *eq = '\0';
        char key[128], val[256];
        strncpy(key, line, sizeof(key) - 1);
        key[sizeof(key) - 1] = '\0';
        strncpy(val, eq + 1, sizeof(val) - 1);
        val[sizeof(val) - 1] = '\0';
        trim(key);
        trim(val);

        // Parse based on section and key
        if (strcmp(current_section, "openrouter") == 0) {
            if (strcmp(key, "api_key") == 0)
                strncpy(config.api_key, val, sizeof(config.api_key) - 1);
            else if (strcmp(key, "llm_model") == 0)
                strncpy(config.llm_model, val, sizeof(config.llm_model) - 1);
            else if (strcmp(key, "tts_model") == 0)
                strncpy(config.tts_model, val, sizeof(config.tts_model) - 1);
        }
        else if (strcmp(current_section, "minimax") == 0) {
            if (strcmp(key, "api_key") == 0)
                strncpy(config.minimax_api_key, val, sizeof(config.minimax_api_key) - 1);
            else if (strcmp(key, "voice_id") == 0)
                strncpy(config.minimax_voice_id, val, sizeof(config.minimax_voice_id) - 1);
            else if (strcmp(key, "emotion") == 0)
                strncpy(config.minimax_emotion, val, sizeof(config.minimax_emotion) - 1);
            else if (strcmp(key, "speed") == 0)
                config.minimax_speed = atof(val);
        }
        else if (strcmp(current_section, "agent") == 0) {
            if (strcmp(key, "hid_binary") == 0)
                strncpy(config.hid_binary, val, sizeof(config.hid_binary) - 1);
            else if (strcmp(key, "energy_threshold") == 0)
                config.energy_threshold = atoi(val);
            else if (strcmp(key, "silence_ms") == 0)
                config.silence_ms = atoi(val);
            else if (strcmp(key, "min_speech_ms") == 0)
                config.min_speech_ms = atoi(val);
        }
    }

    fclose(fp);
    return config.api_key[0] != '\0';
}

}
