#include "config.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <errno.h>

namespace aiden {

static void set_error(std::string* error, const std::string& msg) {
    if (error) *error = msg;
}

static void trim(char* s) {
    char* end = s + strlen(s) - 1;
    while (end > s && isspace(*end)) *end-- = '\0';
    char* start = s;
    while (*start && isspace(*start)) start++;
    if (start != s) memmove(s, start, strlen(start) + 1);
}

static void copy_str(char* dst, size_t dst_size, const char* src) {
    strncpy(dst, src, dst_size - 1);
    dst[dst_size - 1] = '\0';
}

bool load_config(const char* path, AgentConfig& config, std::string* error) {
    if (error) error->clear();

    if (!path) {
        set_error(error, "cannot open config: path is null");
        return false;
    }

    FILE* fp = fopen(path, "r");
    if (!fp) {
        int saved_errno = errno;
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot open '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return false;
    }

    char line[512];
    char current_section[64] = "";
    int line_no = 0;

    while (fgets(line, sizeof(line), fp)) {
        line_no++;
        trim(line);
        if (line[0] == '#' || line[0] == ';' || line[0] == '\0')
            continue;

        if (line[0] == '[') {
            char* end = strchr(line, ']');
            if (!end) {
                char buf[512];
                snprintf(buf, sizeof(buf),
                         "%s:%d: unterminated section header: '%s'",
                         path, line_no, line);
                set_error(error, buf);
                fclose(fp);
                return false;
            }
            *end = '\0';
            copy_str(current_section, sizeof(current_section), line + 1);
            continue;
        }

        char* eq = strchr(line, '=');
        if (!eq) {
            char buf[512];
            snprintf(buf, sizeof(buf),
                     "%s:%d: expected 'key = value', got: '%s'",
                     path, line_no, line);
            set_error(error, buf);
            fclose(fp);
            return false;
        }

        *eq = '\0';
        char key[128], val[256];
        copy_str(key, sizeof(key), line);
        copy_str(val, sizeof(val), eq + 1);
        trim(key);
        trim(val);

        if (strcmp(current_section, "model") == 0) {
            if (strcmp(key, "provider") == 0)
                copy_str(config.model.provider, sizeof(config.model.provider), val);
            else if (strcmp(key, "api_key") == 0)
                copy_str(config.model.api_key, sizeof(config.model.api_key), val);
            else if (strcmp(key, "model") == 0)
                copy_str(config.model.model, sizeof(config.model.model), val);
            else if (strcmp(key, "base_url") == 0)
                copy_str(config.model.base_url, sizeof(config.model.base_url), val);
        }
        else if (strcmp(current_section, "model_text") == 0) {
            if (strcmp(key, "provider") == 0)
                copy_str(config.model_text.provider, sizeof(config.model_text.provider), val);
            else if (strcmp(key, "api_key") == 0)
                copy_str(config.model_text.api_key, sizeof(config.model_text.api_key), val);
            else if (strcmp(key, "model") == 0)
                copy_str(config.model_text.model, sizeof(config.model_text.model), val);
            else if (strcmp(key, "base_url") == 0)
                copy_str(config.model_text.base_url, sizeof(config.model_text.base_url), val);
        }
        else if (strcmp(current_section, "tts") == 0) {
            if (strcmp(key, "provider") == 0)
                copy_str(config.tts.provider, sizeof(config.tts.provider), val);
            else if (strcmp(key, "api_key") == 0)
                copy_str(config.tts.api_key, sizeof(config.tts.api_key), val);
            else if (strcmp(key, "model") == 0)
                copy_str(config.tts.model, sizeof(config.tts.model), val);
            else if (strcmp(key, "voice_id") == 0)
                copy_str(config.tts.voice_id, sizeof(config.tts.voice_id), val);
            else if (strcmp(key, "emotion") == 0)
                copy_str(config.tts.emotion, sizeof(config.tts.emotion), val);
            else if (strcmp(key, "speed") == 0)
                config.tts.speed = atof(val);
        }
        else if (strcmp(current_section, "stt") == 0) {
            if (strcmp(key, "provider") == 0)
                copy_str(config.stt.provider, sizeof(config.stt.provider), val);
            else if (strcmp(key, "api_key") == 0)
                copy_str(config.stt.api_key, sizeof(config.stt.api_key), val);
            else if (strcmp(key, "model") == 0)
                copy_str(config.stt.model, sizeof(config.stt.model), val);
            else if (strcmp(key, "base_url") == 0)
                copy_str(config.stt.base_url, sizeof(config.stt.base_url), val);
            else if (strcmp(key, "secret_id") == 0)
                copy_str(config.stt.secret_id, sizeof(config.stt.secret_id), val);
            else if (strcmp(key, "secret_key") == 0)
                copy_str(config.stt.secret_key, sizeof(config.stt.secret_key), val);
            else if (strcmp(key, "region") == 0)
                copy_str(config.stt.region, sizeof(config.stt.region), val);
            else if (strcmp(key, "engine_model_type") == 0)
                copy_str(config.stt.engine_model_type, sizeof(config.stt.engine_model_type), val);
        }
        else if (strcmp(current_section, "stt.tencent") == 0) {
            if (strcmp(key, "secret_id") == 0)
                copy_str(config.stt.secret_id, sizeof(config.stt.secret_id), val);
            else if (strcmp(key, "secret_key") == 0)
                copy_str(config.stt.secret_key, sizeof(config.stt.secret_key), val);
            else if (strcmp(key, "region") == 0)
                copy_str(config.stt.region, sizeof(config.stt.region), val);
            else if (strcmp(key, "engine_model_type") == 0)
                copy_str(config.stt.engine_model_type, sizeof(config.stt.engine_model_type), val);
        }
        else if (strcmp(current_section, "stt.openai") == 0) {
            if (strcmp(key, "api_key") == 0)
                copy_str(config.stt.api_key, sizeof(config.stt.api_key), val);
            else if (strcmp(key, "model") == 0)
                copy_str(config.stt.model, sizeof(config.stt.model), val);
            else if (strcmp(key, "base_url") == 0)
                copy_str(config.stt.base_url, sizeof(config.stt.base_url), val);
        }
        else if (strcmp(current_section, "agent") == 0) {
            if (strcmp(key, "asr_mode") == 0)
                copy_str(config.asr_mode, sizeof(config.asr_mode), val);
            else if (strcmp(key, "hid_binary") == 0)
                copy_str(config.hid_binary, sizeof(config.hid_binary), val);
            else if (strcmp(key, "additional_prompt") == 0)
                copy_str(config.additional_prompt, sizeof(config.additional_prompt), val);
            else if (strcmp(key, "energy_threshold") == 0)
                config.energy_threshold = atoi(val);
            else if (strcmp(key, "silence_ms") == 0)
                config.silence_ms = atoi(val);
            else if (strcmp(key, "min_speech_ms") == 0)
                config.min_speech_ms = atoi(val);
        }
    }

    fclose(fp);

    if (config.asr_mode[0] == '\0') {
        copy_str(config.asr_mode, sizeof(config.asr_mode), "direct_audio");
    }

    if (config.stt.region[0] == '\0') {
        copy_str(config.stt.region, sizeof(config.stt.region), "ap-guangzhou");
    }
    if (config.stt.engine_model_type[0] == '\0') {
        copy_str(config.stt.engine_model_type, sizeof(config.stt.engine_model_type), "16k_zh");
    }

    return true;
}

}
