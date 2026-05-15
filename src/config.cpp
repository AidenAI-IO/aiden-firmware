#include "config.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

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

static void append_line(std::string& out,
                        const char* key,
                        const char* value,
                        bool blank_line_after = false) {
    out += key;
    out += " = ";
    if (value) {
        out += value;
    }
    out += "\n";
    if (blank_line_after) {
        out += "\n";
    }
}

static void append_int_line(std::string& out,
                            const char* key,
                            int value,
                            bool blank_line_after = false) {
    char buf[64];
    snprintf(buf, sizeof(buf), "%d", value);
    append_line(out, key, buf, blank_line_after);
}

static void append_float_line(std::string& out,
                              const char* key,
                              float value,
                              bool blank_line_after = false) {
    char buf[64];
    snprintf(buf, sizeof(buf), "%.3f", static_cast<double>(value));
    append_line(out, key, buf, blank_line_after);
}

static FILE* open_secure_write_file(const char* path, std::string* error) {
    int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, S_IRUSR | S_IWUSR);
    if (fd < 0) {
        int saved_errno = errno;
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot open '%s' for write: %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return NULL;
    }

    if (fchmod(fd, S_IRUSR | S_IWUSR) != 0) {
        int saved_errno = errno;
        close(fd);
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot set permissions on '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return NULL;
    }

    FILE* fp = fdopen(fd, "w");
    if (!fp) {
        int saved_errno = errno;
        close(fd);
        char buf[512];
        snprintf(buf, sizeof(buf), "cannot open '%s' stream for write: %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return NULL;
    }

    return fp;
}

static bool parse_config_file(const char* path, AgentConfig& config, std::string* error) {
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
            else if (strcmp(key, "frame_service_socket") == 0)
                copy_str(config.frame_service_socket, sizeof(config.frame_service_socket), val);
            else if (strcmp(key, "additional_prompt") == 0)
                copy_str(config.additional_prompt, sizeof(config.additional_prompt), val);
            else if (strcmp(key, "energy_threshold") == 0)
                config.energy_threshold = atoi(val);
            else if (strcmp(key, "silence_ms") == 0)
                config.silence_ms = atoi(val);
            else if (strcmp(key, "min_speech_ms") == 0)
                config.min_speech_ms = atoi(val);
        }
        else if (strcmp(current_section, "network") == 0) {
            if (strcmp(key, "proxy") == 0)
                copy_str(config.network.proxy, sizeof(config.network.proxy), val);
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

bool load_config_relaxed(const char* path, AgentConfig& config, std::string* error) {
    return parse_config_file(path, config, error);
}

bool load_config(const char* path, AgentConfig& config, std::string* error) {
    if (!parse_config_file(path, config, error)) {
        return false;
    }

    const bool model_section_used =
        config.model.provider[0] != '\0' ||
        config.model.model[0] != '\0' ||
        config.model.base_url[0] != '\0';
    if (model_section_used && config.model.api_key[0] == '\0') {
        set_error(error, "model section requires api_key");
        return false;
    }

    return true;
}

bool save_config(const char* path, const AgentConfig& config, std::string* error) {
    if (error) error->clear();

    if (!path) {
        set_error(error, "cannot save config: path is null");
        return false;
    }

    FILE* fp = open_secure_write_file(path, error);
    if (!fp) {
        return false;
    }

    std::string text;
    text += "# AI Agent Configuration\n";
    text += "# Generated by config_web\n\n";

    text += "[model]\n";
    append_line(text, "provider", config.model.provider);
    append_line(text, "api_key", config.model.api_key);
    append_line(text, "model", config.model.model);
    append_line(text, "base_url", config.model.base_url, true);

    text += "[model_text]\n";
    append_line(text, "provider", config.model_text.provider);
    append_line(text, "api_key", config.model_text.api_key);
    append_line(text, "model", config.model_text.model);
    append_line(text, "base_url", config.model_text.base_url, true);

    text += "[tts]\n";
    append_line(text, "provider", config.tts.provider);
    append_line(text, "api_key", config.tts.api_key);
    append_line(text, "model", config.tts.model);
    append_line(text, "voice_id", config.tts.voice_id);
    append_line(text, "emotion", config.tts.emotion);
    append_float_line(text, "speed", config.tts.speed, true);

    text += "[agent]\n";
    append_line(text, "asr_mode", config.asr_mode);
    append_line(text, "hid_binary", config.hid_binary);
    append_line(text, "frame_service_socket", config.frame_service_socket);
    append_int_line(text, "energy_threshold", config.energy_threshold);
    append_int_line(text, "silence_ms", config.silence_ms);
    append_int_line(text, "min_speech_ms", config.min_speech_ms);
    append_line(text, "additional_prompt", config.additional_prompt, true);

    text += "[stt]\n";
    append_line(text, "provider", config.stt.provider, true);

    text += "[stt.tencent]\n";
    append_line(text, "secret_id", config.stt.secret_id);
    append_line(text, "secret_key", config.stt.secret_key);
    append_line(text, "region", config.stt.region);
    append_line(text, "engine_model_type", config.stt.engine_model_type, true);

    text += "[stt.openai]\n";
    append_line(text, "api_key", config.stt.api_key);
    append_line(text, "model", config.stt.model);
    append_line(text, "base_url", config.stt.base_url, true);

    text += "[network]\n";
    append_line(text, "proxy", config.network.proxy);

    size_t written = fwrite(text.data(), 1, text.size(), fp);
    int saved_errno = errno;
    if (fclose(fp) != 0 && saved_errno == 0) {
        saved_errno = errno;
    }

    if (written != text.size()) {
        char buf[512];
        snprintf(buf, sizeof(buf), "failed to write '%s': %s (errno=%d)",
                 path, strerror(saved_errno), saved_errno);
        set_error(error, buf);
        return false;
    }

    return true;
}

}
