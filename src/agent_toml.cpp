#include "agent_toml.h"

#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>
#include <sys/stat.h>
#include <unistd.h>

namespace aiden {

namespace {

std::string trim(const std::string& s) {
    size_t b = 0;
    size_t e = s.size();
    while (b < e && (s[b] == ' ' || s[b] == '\t' || s[b] == '\r')) ++b;
    while (e > b && (s[e - 1] == ' ' || s[e - 1] == '\t' || s[e - 1] == '\r')) --e;
    return s.substr(b, e - b);
}

bool unquote(const std::string& raw, std::string* out, std::string* err) {
    if (raw.size() < 2 || raw.front() != '"' || raw.back() != '"') {
        if (err) *err = "expected double-quoted string";
        return false;
    }
    std::string s;
    s.reserve(raw.size() - 2);
    for (size_t i = 1; i + 1 < raw.size(); ++i) {
        char c = raw[i];
        if (c == '\\') {
            if (i + 2 >= raw.size()) {
                if (err) *err = "trailing backslash in string";
                return false;
            }
            char esc = raw[++i];
            switch (esc) {
                case '"': s.push_back('"'); break;
                case '\\': s.push_back('\\'); break;
                case 'n': s.push_back('\n'); break;
                case 'r': s.push_back('\r'); break;
                case 't': s.push_back('\t'); break;
                default:
                    if (err) *err = std::string("unsupported escape \\") + esc;
                    return false;
            }
        } else {
            s.push_back(c);
        }
    }
    *out = s;
    return true;
}

std::string quote(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 2);
    out.push_back('"');
    for (char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default: out.push_back(c); break;
        }
    }
    out.push_back('"');
    return out;
}

bool parse_int(const std::string& raw, int* out, std::string* err) {
    if (raw.empty()) {
        if (err) *err = "empty integer";
        return false;
    }
    errno = 0;
    char* end = nullptr;
    long v = std::strtol(raw.c_str(), &end, 10);
    if (errno != 0 || !end || *end != '\0') {
        if (err) *err = "invalid integer: " + raw;
        return false;
    }
    *out = static_cast<int>(v);
    return true;
}

bool parse_double(const std::string& raw, double* out, std::string* err) {
    if (raw.empty()) {
        if (err) *err = "empty number";
        return false;
    }
    errno = 0;
    char* end = nullptr;
    double v = std::strtod(raw.c_str(), &end);
    if (errno != 0 || !end || *end != '\0') {
        if (err) *err = "invalid number: " + raw;
        return false;
    }
    *out = v;
    return true;
}

bool parse_bool(const std::string& raw, bool* out, std::string* err) {
    if (raw == "true") {
        *out = true;
        return true;
    }
    if (raw == "false") {
        *out = false;
        return true;
    }
    if (err) *err = "invalid boolean: " + raw;
    return false;
}

bool parse_string_array(const std::string& raw, std::vector<std::string>* out, std::string* err) {
    if (!out) {
        if (err) *err = "null string array target";
        return false;
    }
    if (raw.size() < 2 || raw.front() != '[') {
        if (err) *err = "expected string array";
        return false;
    }

    std::vector<std::string> values;
    size_t i = 1;
    while (i < raw.size()) {
        while (i < raw.size() && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\r')) ++i;
        if (i < raw.size() && raw[i] == ']') {
            ++i;
            while (i < raw.size() && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\r')) ++i;
            if (i != raw.size()) {
                if (err) *err = "unexpected trailing content after array";
                return false;
            }
            *out = values;
            return true;
        }
        if (i >= raw.size() || raw[i] != '"') {
            if (err) *err = "expected double-quoted string in array";
            return false;
        }

        size_t start = i;
        ++i;
        bool closed = false;
        while (i < raw.size()) {
            if (raw[i] == '\\') {
                if (i + 1 >= raw.size()) {
                    if (err) *err = "trailing backslash in array string";
                    return false;
                }
                i += 2;
                continue;
            }
            if (raw[i] == '"') {
                ++i;
                closed = true;
                break;
            }
            ++i;
        }
        if (!closed) {
            if (err) *err = "unterminated string in array";
            return false;
        }

        std::string value;
        if (!unquote(raw.substr(start, i - start), &value, err)) {
            return false;
        }
        values.push_back(value);

        while (i < raw.size() && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\r')) ++i;
        if (i < raw.size() && raw[i] == ',') {
            ++i;
            continue;
        }
        if (i < raw.size() && raw[i] == ']') {
            continue;
        }
        if (i >= raw.size()) {
            if (err) *err = "missing closing bracket in array";
        } else {
            if (err) *err = "expected comma or closing bracket in array";
        }
        return false;
    }

    if (err) *err = "missing closing bracket in array";
    return false;
}

bool assign_string(std::string* dst, const std::string& raw, std::string* err) {
    return unquote(raw, dst, err);
}

bool assign_int(int* dst, const std::string& raw, std::string* err) {
    return parse_int(raw, dst, err);
}

bool assign_double(double* dst, const std::string& raw, std::string* err) {
    return parse_double(raw, dst, err);
}

bool assign_bool(bool* dst, const std::string& raw, std::string* err) {
    return parse_bool(raw, dst, err);
}

bool assign_string_array(std::vector<std::string>* dst, const std::string& raw, std::string* err) {
    return parse_string_array(raw, dst, err);
}

void apply_kv(AgentToml& cfg,
              const std::string& section,
              const std::string& key,
              const std::string& raw,
              std::string* err) {
    auto fail = [&](const std::string& m) {
        if (err && err->empty()) {
            *err = section.empty() ? key + ": " + m
                                   : "[" + section + "] " + key + ": " + m;
        }
    };
    std::string sub_err;

    if (section.empty()) {
        if (key == "instruction") {
            if (!assign_string(&cfg.instruction, raw, &sub_err)) fail(sub_err);
        } else if (key == "additional_prompt") {
            if (!assign_string(&cfg.additional_prompt, raw, &sub_err)) fail(sub_err);
        } else if (key == "input_mode") {
            if (!assign_string(&cfg.input_mode, raw, &sub_err)) fail(sub_err);
        } else if (key == "trigger_mode") {
            if (!assign_string(&cfg.trigger_mode, raw, &sub_err)) fail(sub_err);
        } else if (key == "vad_backend") {
            if (!assign_string(&cfg.vad_backend, raw, &sub_err)) fail(sub_err);
        } else if (key == "vad_model_path") {
            if (!assign_string(&cfg.vad_model_path, raw, &sub_err)) fail(sub_err);
        } else if (key == "vad_helper_path") {
            if (!assign_string(&cfg.vad_helper_path, raw, &sub_err)) fail(sub_err);
        } else if (key == "vad_speech_threshold") {
            if (!assign_double(&cfg.vad_speech_threshold, raw, &sub_err)) fail(sub_err);
        } else if (key == "silence_ms") {
            if (!assign_int(&cfg.silence_ms, raw, &sub_err)) fail(sub_err);
        } else if (key == "min_speech_ms") {
            if (!assign_int(&cfg.min_speech_ms, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_session_enabled") {
            if (!assign_bool(&cfg.voice_session_enabled, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_followup_timeout_ms") {
            if (!assign_int(&cfg.voice_followup_timeout_ms, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_first_turn_timeout_ms") {
            if (!assign_int(&cfg.voice_first_turn_timeout_ms, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_max_turns") {
            if (!assign_int(&cfg.voice_max_turns, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_interrupt_on_wakeup") {
            if (!assign_bool(&cfg.voice_interrupt_on_wakeup, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_streaming_tts_enabled") {
            if (!assign_bool(&cfg.voice_streaming_tts_enabled, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_tool_call_speech") {
            if (!assign_bool(&cfg.voice_tool_call_speech, raw, &sub_err)) fail(sub_err);
        } else if (key == "voice_max_response_tokens") {
            if (!assign_int(&cfg.voice_max_response_tokens, raw, &sub_err)) fail(sub_err);
        } else if (key == "max_iterations") {
            if (!assign_int(&cfg.max_iterations, raw, &sub_err)) fail(sub_err);
        } else if (key == "screenshot_keep_n") {
            if (!assign_int(&cfg.screenshot_keep_n, raw, &sub_err)) fail(sub_err);
        } else if (key == "screenshot_prune_interval") {
            if (!assign_int(&cfg.screenshot_prune_interval, raw, &sub_err)) fail(sub_err);
        } else if (key == "screen_stable_timeout_ms") {
            if (!assign_int(&cfg.screen_stable_timeout_ms, raw, &sub_err)) fail(sub_err);
        } else if (key == "screen_stable_ms") {
            if (!assign_int(&cfg.screen_stable_ms, raw, &sub_err)) fail(sub_err);
        } else if (key == "screen_stable_diff_threshold") {
            if (!assign_double(&cfg.screen_stable_diff_threshold, raw, &sub_err)) fail(sub_err);
        }
        // Unknown top-level keys are ignored to remain forward-compatible.
        return;
    }

    auto fill_model = [&](ModelToml& m) {
        if (key == "provider") assign_string(&m.provider, raw, &sub_err);
        else if (key == "model") assign_string(&m.model, raw, &sub_err);
        else if (key == "base_url") assign_string(&m.base_url, raw, &sub_err);
        else if (key == "api_key") assign_string(&m.api_key, raw, &sub_err);
        else if (key == "token_env") assign_string(&m.token_env, raw, &sub_err);
        else if (key == "temperature") assign_double(&m.temperature, raw, &sub_err);
        else if (key == "max_response_tokens") assign_int(&m.max_response_tokens, raw, &sub_err);
        else if (key == "context_window") assign_int(&m.context_window, raw, &sub_err);
        else if (key == "model_max_output_tokens") assign_int(&m.model_max_output_tokens, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    };

    if (section == "model") {
        fill_model(cfg.model);
    } else if (section == "model_text") {
        fill_model(cfg.model_text);
    } else if (section == "tts") {
        if (key == "provider") assign_string(&cfg.tts.provider, raw, &sub_err);
        else if (key == "api_key") assign_string(&cfg.tts.api_key, raw, &sub_err);
        else if (key == "model") assign_string(&cfg.tts.model, raw, &sub_err);
        else if (key == "voice_id") assign_string(&cfg.tts.voice_id, raw, &sub_err);
        else if (key == "emotion") assign_string(&cfg.tts.emotion, raw, &sub_err);
        else if (key == "speed") assign_double(&cfg.tts.speed, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    } else if (section == "stt") {
        if (key == "provider") assign_string(&cfg.stt.provider, raw, &sub_err);
        else if (key == "api_key") assign_string(&cfg.stt.api_key, raw, &sub_err);
        else if (key == "model") assign_string(&cfg.stt.model, raw, &sub_err);
        else if (key == "base_url") assign_string(&cfg.stt.base_url, raw, &sub_err);
        else if (key == "secret_id") assign_string(&cfg.stt.secret_id, raw, &sub_err);
        else if (key == "secret_key") assign_string(&cfg.stt.secret_key, raw, &sub_err);
        else if (key == "region") assign_string(&cfg.stt.region, raw, &sub_err);
        else if (key == "engine_model_type") assign_string(&cfg.stt.engine_model_type, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    } else if (section == "audio") {
        if (key == "socket") assign_string(&cfg.audio.socket, raw, &sub_err);
        else if (key == "sample_rate") assign_int(&cfg.audio.sample_rate, raw, &sub_err);
        else if (key == "channels") assign_int(&cfg.audio.channels, raw, &sub_err);
        else if (key == "bit_width") assign_int(&cfg.audio.bit_width, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    } else if (section == "hid") {
        if (key == "keyboard_device") assign_string(&cfg.hid.keyboard_device, raw, &sub_err);
        else if (key == "mouse_device") assign_string(&cfg.hid.mouse_device, raw, &sub_err);
        else if (key == "frame_socket") assign_string(&cfg.hid.frame_socket, raw, &sub_err);
        else if (key == "pointer_mode") assign_string(&cfg.hid.pointer_mode, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    } else if (section == "search") {
        if (key == "provider") assign_string(&cfg.search.provider, raw, &sub_err);
        else if (key == "api_key") assign_string(&cfg.search.api_key, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    } else if (section == "telemetry") {
        if (key == "enabled") assign_bool(&cfg.telemetry.enabled, raw, &sub_err);
        else if (key == "provider") assign_string(&cfg.telemetry.provider, raw, &sub_err);
        else if (key == "base_url") assign_string(&cfg.telemetry.base_url, raw, &sub_err);
        else if (key == "public_key") assign_string(&cfg.telemetry.public_key, raw, &sub_err);
        else if (key == "secret_key") assign_string(&cfg.telemetry.secret_key, raw, &sub_err);
        else if (key == "upload_screenshots") assign_bool(&cfg.telemetry.upload_screenshots, raw, &sub_err);
        else if (key == "upload_timeout_sec") assign_int(&cfg.telemetry.upload_timeout_sec, raw, &sub_err);
        else if (key == "max_retry") assign_int(&cfg.telemetry.max_retry, raw, &sub_err);
        else if (key == "tags") assign_string_array(&cfg.telemetry.tags, raw, &sub_err);
        else if (key == "environment") assign_string(&cfg.telemetry.environment, raw, &sub_err);
        if (!sub_err.empty()) fail(sub_err);
    }
    // Unknown sections / keys are ignored.
}

bool atomic_write(const std::string& path, const std::string& content, std::string* err) {
    std::string tmp = path + ".tmp";
    FILE* fp = std::fopen(tmp.c_str(), "w");
    if (!fp) {
        if (err) *err = "open " + tmp + ": " + std::strerror(errno);
        return false;
    }
    if (std::fwrite(content.data(), 1, content.size(), fp) != content.size()) {
        if (err) *err = "write " + tmp + ": " + std::strerror(errno);
        std::fclose(fp);
        std::remove(tmp.c_str());
        return false;
    }
    if (std::fflush(fp) != 0) {
        if (err) *err = "flush " + tmp + ": " + std::strerror(errno);
        std::fclose(fp);
        std::remove(tmp.c_str());
        return false;
    }
    int fd = fileno(fp);
    if (fd >= 0) fsync(fd);
    std::fclose(fp);
    if (std::rename(tmp.c_str(), path.c_str()) != 0) {
        if (err) *err = "rename " + tmp + " -> " + path + ": " + std::strerror(errno);
        std::remove(tmp.c_str());
        return false;
    }
    return true;
}

void emit_string(std::ostringstream& out, const char* key, const std::string& value) {
    out << key << " = " << quote(value) << "\n";
}

void emit_int(std::ostringstream& out, const char* key, int value) {
    out << key << " = " << value << "\n";
}

void emit_double(std::ostringstream& out, const char* key, double value) {
    char buf[64];
    std::snprintf(buf, sizeof(buf), "%g", value);
    out << key << " = " << buf << "\n";
}

void emit_bool(std::ostringstream& out, const char* key, bool value) {
    out << key << " = " << (value ? "true" : "false") << "\n";
}

void emit_string_array(std::ostringstream& out, const char* key, const std::vector<std::string>& values) {
    out << key << " = [";
    for (size_t i = 0; i < values.size(); ++i) {
        if (i) out << ", ";
        out << quote(values[i]);
    }
    out << "]\n";
}

void emit_model(std::ostringstream& out, const char* section, const ModelToml& m) {
    out << "[" << section << "]\n";
    emit_string(out, "provider", m.provider);
    emit_string(out, "model", m.model);
    if (!m.base_url.empty()) emit_string(out, "base_url", m.base_url);
    emit_string(out, "api_key", m.api_key);
    if (!m.token_env.empty()) emit_string(out, "token_env", m.token_env);
    if (m.temperature != 0.0) emit_double(out, "temperature", m.temperature);
    if (m.max_response_tokens != 0) emit_int(out, "max_response_tokens", m.max_response_tokens);
    if (m.context_window != 0) emit_int(out, "context_window", m.context_window);
    if (m.model_max_output_tokens != 0) emit_int(out, "model_max_output_tokens", m.model_max_output_tokens);
    out << "\n";
}

}

bool load_agent_toml(const char* path, AgentToml& cfg, std::string* error) {
    if (error) error->clear();
    if (!path) {
        if (error) *error = "null path";
        return false;
    }

    std::ifstream in(path);
    if (!in.is_open()) {
        if (error) *error = std::string("open ") + path + ": " + std::strerror(errno);
        return false;
    }

    std::string section;
    std::string line;
    int lineno = 0;
    std::string parse_error;

    while (std::getline(in, line)) {
        ++lineno;
        std::string t = trim(line);
        if (t.empty() || t[0] == '#') continue;

        if (t.front() == '[') {
            size_t close = t.find(']');
            if (close == std::string::npos) {
                if (error) {
                    *error = "line " + std::to_string(lineno) + ": unclosed section header";
                }
                return false;
            }
            section = trim(t.substr(1, close - 1));
            continue;
        }

        size_t eq = t.find('=');
        if (eq == std::string::npos) {
            if (error) {
                *error = "line " + std::to_string(lineno) + ": expected '=' in '" + t + "'";
            }
            return false;
        }

        std::string key = trim(t.substr(0, eq));
        std::string value = trim(t.substr(eq + 1));
        // Strip trailing line comments. For quoted strings, only strip past the
        // closing quote; for unquoted scalars, strip on the first '#'.
        if (!value.empty() && value.front() == '"') {
            size_t i = 1;
            bool closed = false;
            while (i < value.size()) {
                if (value[i] == '\\' && i + 1 < value.size()) {
                    i += 2;
                    continue;
                }
                if (value[i] == '"') {
                    closed = true;
                    ++i;
                    break;
                }
                ++i;
            }
            if (closed) {
                value = trim(value.substr(0, i));
            }
        } else if (!value.empty()) {
            size_t hash = value.find('#');
            if (hash != std::string::npos) {
                value = trim(value.substr(0, hash));
            }
        }

        std::string apply_err;
        apply_kv(cfg, section, key, value, &apply_err);
        if (!apply_err.empty() && parse_error.empty()) {
            parse_error = "line " + std::to_string(lineno) + ": " + apply_err;
        }
    }

    if (!parse_error.empty()) {
        if (error) *error = parse_error;
        return false;
    }
    return true;
}

bool save_agent_toml(const char* path, const AgentToml& cfg, std::string* error) {
    if (error) error->clear();
    if (!path) {
        if (error) *error = "null path";
        return false;
    }

    std::ostringstream out;
    if (!cfg.instruction.empty()) emit_string(out, "instruction", cfg.instruction);
    if (!cfg.additional_prompt.empty()) emit_string(out, "additional_prompt", cfg.additional_prompt);
    if (!cfg.input_mode.empty()) emit_string(out, "input_mode", cfg.input_mode);
    if (!cfg.trigger_mode.empty()) emit_string(out, "trigger_mode", cfg.trigger_mode);
    if (!cfg.vad_backend.empty()) emit_string(out, "vad_backend", cfg.vad_backend);
    if (!cfg.vad_model_path.empty()) emit_string(out, "vad_model_path", cfg.vad_model_path);
    if (!cfg.vad_helper_path.empty()) emit_string(out, "vad_helper_path", cfg.vad_helper_path);
    if (cfg.vad_speech_threshold != 0.0) emit_double(out, "vad_speech_threshold", cfg.vad_speech_threshold);
    if (cfg.silence_ms != 0) emit_int(out, "silence_ms", cfg.silence_ms);
    if (cfg.min_speech_ms != 0) emit_int(out, "min_speech_ms", cfg.min_speech_ms);
    emit_bool(out, "voice_session_enabled", cfg.voice_session_enabled);
    if (cfg.voice_followup_timeout_ms != 0) emit_int(out, "voice_followup_timeout_ms", cfg.voice_followup_timeout_ms);
    if (cfg.voice_first_turn_timeout_ms != 0) emit_int(out, "voice_first_turn_timeout_ms", cfg.voice_first_turn_timeout_ms);
    if (cfg.voice_max_turns != 0) emit_int(out, "voice_max_turns", cfg.voice_max_turns);
    emit_bool(out, "voice_interrupt_on_wakeup", cfg.voice_interrupt_on_wakeup);
    emit_bool(out, "voice_streaming_tts_enabled", cfg.voice_streaming_tts_enabled);
    emit_bool(out, "voice_tool_call_speech", cfg.voice_tool_call_speech);
    if (cfg.voice_max_response_tokens != 0) emit_int(out, "voice_max_response_tokens", cfg.voice_max_response_tokens);
    if (cfg.max_iterations != 0) emit_int(out, "max_iterations", cfg.max_iterations);
    if (cfg.screenshot_keep_n != 0) emit_int(out, "screenshot_keep_n", cfg.screenshot_keep_n);
    if (cfg.screenshot_prune_interval != 0) emit_int(out, "screenshot_prune_interval", cfg.screenshot_prune_interval);
    if (cfg.screen_stable_timeout_ms != 0) emit_int(out, "screen_stable_timeout_ms", cfg.screen_stable_timeout_ms);
    if (cfg.screen_stable_ms != 0) emit_int(out, "screen_stable_ms", cfg.screen_stable_ms);
    if (cfg.screen_stable_diff_threshold > 0.0) emit_double(out, "screen_stable_diff_threshold", cfg.screen_stable_diff_threshold);
    out << "\n";

    emit_model(out, "model", cfg.model);
    if (!cfg.model_text.provider.empty() || !cfg.model_text.model.empty()) {
        emit_model(out, "model_text", cfg.model_text);
    }

    out << "[tts]\n";
    emit_string(out, "provider", cfg.tts.provider);
    emit_string(out, "api_key", cfg.tts.api_key);
    if (!cfg.tts.model.empty()) emit_string(out, "model", cfg.tts.model);
    emit_string(out, "voice_id", cfg.tts.voice_id);
    emit_string(out, "emotion", cfg.tts.emotion);
    emit_double(out, "speed", cfg.tts.speed);
    out << "\n";

    out << "[stt]\n";
    emit_string(out, "provider", cfg.stt.provider);
    if (!cfg.stt.api_key.empty()) emit_string(out, "api_key", cfg.stt.api_key);
    if (!cfg.stt.model.empty()) emit_string(out, "model", cfg.stt.model);
    if (!cfg.stt.base_url.empty()) emit_string(out, "base_url", cfg.stt.base_url);
    if (!cfg.stt.secret_id.empty()) emit_string(out, "secret_id", cfg.stt.secret_id);
    if (!cfg.stt.secret_key.empty()) emit_string(out, "secret_key", cfg.stt.secret_key);
    if (!cfg.stt.region.empty()) emit_string(out, "region", cfg.stt.region);
    if (!cfg.stt.engine_model_type.empty()) emit_string(out, "engine_model_type", cfg.stt.engine_model_type);
    out << "\n";

    out << "[audio]\n";
    emit_string(out, "socket", cfg.audio.socket);
    if (cfg.audio.sample_rate != 0) emit_int(out, "sample_rate", cfg.audio.sample_rate);
    if (cfg.audio.channels != 0) emit_int(out, "channels", cfg.audio.channels);
    if (cfg.audio.bit_width != 0) emit_int(out, "bit_width", cfg.audio.bit_width);
    out << "\n";

    out << "[hid]\n";
    emit_string(out, "keyboard_device", cfg.hid.keyboard_device);
    emit_string(out, "mouse_device", cfg.hid.mouse_device);
    emit_string(out, "frame_socket", cfg.hid.frame_socket);
    if (!cfg.hid.pointer_mode.empty()) emit_string(out, "pointer_mode", cfg.hid.pointer_mode);
    out << "\n";

    out << "[search]\n";
    emit_string(out, "provider", cfg.search.provider);
    emit_string(out, "api_key", cfg.search.api_key);
    out << "\n";

    out << "[telemetry]\n";
    emit_bool(out, "enabled", cfg.telemetry.enabled);
    emit_string(out, "provider", cfg.telemetry.provider);
    if (!cfg.telemetry.base_url.empty()) emit_string(out, "base_url", cfg.telemetry.base_url);
    emit_string(out, "public_key", cfg.telemetry.public_key);
    emit_string(out, "secret_key", cfg.telemetry.secret_key);
    emit_bool(out, "upload_screenshots", cfg.telemetry.upload_screenshots);
    if (cfg.telemetry.upload_timeout_sec != 0) emit_int(out, "upload_timeout_sec", cfg.telemetry.upload_timeout_sec);
    if (cfg.telemetry.max_retry != 0) emit_int(out, "max_retry", cfg.telemetry.max_retry);
    if (!cfg.telemetry.tags.empty()) emit_string_array(out, "tags", cfg.telemetry.tags);
    if (!cfg.telemetry.environment.empty()) emit_string(out, "environment", cfg.telemetry.environment);
    out << "\n";

    return atomic_write(path, out.str(), error);
}

}
