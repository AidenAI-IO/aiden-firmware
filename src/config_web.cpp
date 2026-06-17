#include "agent_toml.h"
#include "config_web_html.h"
#include "system_env_parser.h"
#include "wifi_config.h"

#include "cJSON/cJSON.h"

#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <ctype.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/socket.h>
#include <sys/select.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#include <iostream>
#include <map>
#include <set>
#include <sstream>
#include <string>
#include <vector>

namespace {

struct Options {
    std::string bind_address = "0.0.0.0";
    int port = 80;
    std::string agent_config_path = "/userdata/agent/agent.toml";
    std::string wifi_config_path = "/userdata/wpa_supplicant.conf";
    std::string wifi_interface = "wlan0";
    std::string ota_state_path = "/userdata/ota/state.json";
    std::string cmdline_path = "/proc/cmdline";
    std::string system_env_path = "/userdata/system/env";
};

struct HttpRequest {
    std::string method;
    std::string path;
    std::string body;
    int error_status_code = 0;
    std::string error_status_text;
    std::string error_body;
};

struct ApiResponse {
    int status_code = 200;
    std::string status_text = "OK";
    std::string content_type = "application/json; charset=utf-8";
    std::string body = "{}";
};

struct CommandResult {
    int exit_code = -1;
    bool timed_out = false;
    std::string output;
};

struct WifiRuntimeStatus {
    bool connected = false;
    std::string ssid;
    std::string ip_address;
    std::string state = "DISCONNECTED";
    std::string detail;
};

struct AgentRuntimeStatus {
    bool watchdog_running = false;
    bool process_running = false;
    int watchdog_pid = 0;
    int pid = 0;
    std::string addr = ":8080";
    std::string state = "unknown";
    std::string detail;
    std::string port_host = "127.0.0.1";
    int port = 8080;
    bool port_reachable = false;
    std::string port_detail;
    std::string startup_error;
};

struct AgentLogSnapshot {
    bool exists = false;
    bool truncated = false;
    long size_bytes = 0;
    std::string path;
    std::string log;
    std::string error;
};

using SystemProxy = aiden::SystemEnvProxy;

struct TcpPortStatus {
    bool reachable = false;
    std::string detail;
};

volatile sig_atomic_t g_should_stop = 0;

const char* kAnyBindAddress = "0.0.0.0";
const char* kUsbBindAddress = "192.168.42.1";
const char* kLoopbackBindAddress = "127.0.0.1";
const char* kAgentInitScript = "/etc/init.d/S53agent";
const char* kAgentLogPath = "/var/log/agent/agent.log";
// Default path to the agent binary on device. Tests override this via the
// AIDEN_AGENT_BIN environment variable, resolved once on first use to keep
// production calls free of getenv overhead.
const char* kDefaultAgentBin = "/oem/usr/bin/agent";

// agent_bin_path returns the resolved path to the agent CLI. Honours the
// AIDEN_AGENT_BIN env var if set (test-only injection point); otherwise
// returns kDefaultAgentBin. Cached on first call, so changing the env var
// after process start has no effect.
const char* agent_bin_path() {
    static const char* cached = []() -> const char* {
        const char* override_path = std::getenv("AIDEN_AGENT_BIN");
        if (override_path && override_path[0] != '\0') {
            return override_path;
        }
        return kDefaultAgentBin;
    }();
    return cached;
}
const char* kOtaBin = "/oem/usr/bin/ota";
const char* kEnvRunBin = "/oem/usr/bin/aiden-env-run";
const char* kOtaHealthLogPath = "/var/log/ota/ota.log";
const char* kOtaWebUpdateLockPath = "/tmp/config_web_ota_update.lock";
const char* kOtaWebUpdateLogPath = "/tmp/config_web_ota_update.log";
const char* kAgentPortHost = "127.0.0.1";
const int kDefaultAgentPort = 8080;
const int kAgentStatusCommandTimeoutMs = 1500;
const size_t kAgentStatusLogReadSize = 16 * 1024;
const size_t kAgentStatusLogDisplaySize = 4096;
const size_t kAgentLogReadSize = 64 * 1024;
const size_t kAgentLogDisplaySize = 48 * 1024;
const size_t kOtaLogReadSize = 128 * 1024;
const size_t kOtaLogDisplaySize = 96 * 1024;
const size_t kMaxHttpHeaderSize = 8 * 1024;
const size_t kMaxHttpBodySize = 64 * 1024;
const int kClientReadTimeoutSeconds = 5;
const char* kDefaultNoProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16";
const int kSystemEnvCommandTimeoutMs = 1500;
const size_t kMaxSystemEnvSize = 64 * 1024;

std::string read_file_contents(const char* path, size_t max_size);
std::string validate_proxy_url(const std::string& url);
std::string lowercase_copy(const std::string& text);
std::string parent_dir(const std::string& path);
bool mkdir_p(const std::string& dir, std::string* error);
bool prepare_ota_update_log_file(const std::string& path, std::string* error);
bool set_fd_cloexec(int fd, std::string* error);
CommandResult run_command_with_stdin(const std::string& command, const std::string& input, int timeout_ms);
void update_config_from_json(cJSON* root, aiden::AgentToml* config);

// ValidationOutcome distinguishes "the user submitted something invalid" (the
// CLI ran and rejected it -> 400) from "we could not run validation at all"
// (CLI binary missing, timed out, or returned junk -> 503). Collapsing both
// into a single string error masked a real bug: a missing agent binary made
// /api/config/meta correctly return 503 while POST /api/config returned 400
// for the same root cause, leading the UI to mislabel a server-side
// dependency outage as a user request error.
enum class ValidationOutcome {
    OK,
    INVALID,
    UNAVAILABLE,
};

struct ValidationResult {
    ValidationOutcome outcome = ValidationOutcome::OK;
    std::string message;

    static ValidationResult ok() {
        ValidationResult r;
        r.outcome = ValidationOutcome::OK;
        return r;
    }
    static ValidationResult invalid(const std::string& msg) {
        ValidationResult r;
        r.outcome = ValidationOutcome::INVALID;
        r.message = msg;
        return r;
    }
    static ValidationResult unavailable(const std::string& msg) {
        ValidationResult r;
        r.outcome = ValidationOutcome::UNAVAILABLE;
        r.message = msg;
        return r;
    }
};

ValidationResult validate_agent_config_via_cli(const aiden::AgentToml& config, const char* agent_bin_path);

enum ReadStatus {
    READ_STATUS_OK,
    READ_STATUS_EOF,
    READ_STATUS_ERROR,
    READ_STATUS_TIMEOUT,
    READ_STATUS_LIMIT_EXCEEDED,
};

void on_signal(int) {
    g_should_stop = 1;
}

bool file_exists(const char* path) {
    return path && access(path, F_OK) == 0;
}

std::string env_path_or_default(const char* env_name, const char* default_path) {
    const char* override_path = std::getenv(env_name);
    if (override_path && override_path[0] != '\0') {
        return override_path;
    }
    return default_path ? std::string(default_path) : std::string();
}

std::string ota_bin_path() {
    return env_path_or_default("AIDEN_OTA_BIN", kOtaBin);
}

std::string env_run_bin_path() {
    return env_path_or_default("AIDEN_ENV_RUN_BIN", kEnvRunBin);
}

std::string ota_update_lock_path() {
    return env_path_or_default("AIDEN_CONFIG_WEB_OTA_UPDATE_LOCK", kOtaWebUpdateLockPath);
}

std::string ota_update_log_path() {
    return env_path_or_default("AIDEN_CONFIG_WEB_OTA_UPDATE_LOG", kOtaWebUpdateLogPath);
}

std::string ota_health_log_path() {
    return env_path_or_default("AIDEN_CONFIG_WEB_OTA_HEALTH_LOG", kOtaHealthLogPath);
}

std::string trim_copy(const std::string& input) {
    size_t start = 0;
    while (start < input.size() && isspace(static_cast<unsigned char>(input[start]))) {
        ++start;
    }

    size_t end = input.size();
    while (end > start && isspace(static_cast<unsigned char>(input[end - 1]))) {
        --end;
    }

    return input.substr(start, end - start);
}

std::string normalize_pointer_mode(const std::string& value) {
    std::string mode = trim_copy(value);
    for (size_t i = 0; i < mode.size(); ++i) {
        mode[i] = static_cast<char>(tolower(static_cast<unsigned char>(mode[i])));
    }
    return mode.empty() ? "absolute" : mode;
}

bool starts_with(const std::string& text, const std::string& prefix) {
    return text.compare(0, prefix.size(), prefix) == 0;
}

bool has_proxy_url(const SystemProxy& proxy) {
    return !trim_copy(proxy.http_proxy).empty() ||
           !trim_copy(proxy.https_proxy).empty() ||
           !trim_copy(proxy.all_proxy).empty();
}

bool consume_prefix(const char* arg, const char* prefix, std::string* out) {
    size_t prefix_len = strlen(prefix);
    if (strncmp(arg, prefix, prefix_len) != 0) {
        return false;
    }
    if (out) {
        *out = arg + prefix_len;
    }
    return true;
}

bool json_is_type(cJSON* item, int type) {
    return item && ((item->type & 0xff) == type);
}

bool json_is_object(cJSON* item) {
    return json_is_type(item, cJSON_Object);
}

bool json_is_string(cJSON* item) {
    return json_is_type(item, cJSON_String) && item->valuestring != NULL;
}

bool json_is_number(cJSON* item) {
    return json_is_type(item, cJSON_Number);
}

bool json_is_array(cJSON* item) {
    return json_is_type(item, cJSON_Array);
}

bool json_is_bool(cJSON* item) {
    return json_is_type(item, cJSON_True) || json_is_type(item, cJSON_False);
}

std::string json_type_name(cJSON* item) {
    if (!item) {
        return "missing";
    }
    switch (item->type & 0xff) {
        case cJSON_False:
        case cJSON_True:
            return "bool";
        case cJSON_NULL:
            return "null";
        case cJSON_Number:
            return "number";
        case cJSON_String:
            return "string";
        case cJSON_Array:
            return "array";
        case cJSON_Object:
            return "object";
        default:
            return "unknown";
    }
}

bool config_schema_error(std::string* error,
                         const std::string& path,
                         const std::string& expected,
                         cJSON* item) {
    if (!error) {
        return false;
    }
    if (!item) {
        *error = "agent config missing required field " + path + " (expected " + expected + ")";
    } else {
        *error = "agent config invalid field " + path + ": expected " + expected
               + ", got " + json_type_name(item);
    }
    return false;
}

std::string config_field_path(const char* section, const char* key) {
    return std::string(section) + "." + key;
}

enum ConfigFieldKind {
    CONFIG_FIELD_STRING,
    CONFIG_FIELD_NUMBER,
    CONFIG_FIELD_BOOL,
    CONFIG_FIELD_ARRAY,
};

bool validate_config_field_type(cJSON* obj,
                                const char* section,
                                const char* key,
                                ConfigFieldKind kind,
                                std::string* error) {
    cJSON* item = obj ? cJSON_GetObjectItem(obj, key) : NULL;
    if (!item) {
        return true;
    }

    const std::string path = config_field_path(section, key);
    switch (kind) {
        case CONFIG_FIELD_STRING:
            if (!json_is_string(item)) {
                return config_schema_error(error, path, "string", item);
            }
            return true;
        case CONFIG_FIELD_NUMBER:
            if (!json_is_number(item)) {
                return config_schema_error(error, path, "number", item);
            }
            return true;
        case CONFIG_FIELD_BOOL:
            if (!json_is_bool(item)) {
                return config_schema_error(error, path, "bool", item);
            }
            return true;
        case CONFIG_FIELD_ARRAY:
            if (!json_is_array(item)) {
                return config_schema_error(error, path, "array", item);
            }
            return true;
    }
    return config_schema_error(error, path, "known field kind", item);
}

bool validate_required_string(cJSON* obj,
                              const char* section,
                              const char* key,
                              bool allow_empty,
                              std::string* error) {
    cJSON* item = obj ? cJSON_GetObjectItem(obj, key) : NULL;
    const std::string path = config_field_path(section, key);
    if (!json_is_string(item)) {
        return config_schema_error(error, path, allow_empty ? "string" : "non-empty string", item);
    }
    if (!allow_empty && trim_copy(item->valuestring).empty()) {
        if (error) {
            *error = "agent config invalid field " + path + ": expected non-empty string, got empty string";
        }
        return false;
    }
    return true;
}

bool validate_model_section(cJSON* root, std::string* error) {
    cJSON* model = cJSON_GetObjectItem(root, "model");
    if (!json_is_object(model)) {
        return config_schema_error(error, "model", "object", model);
    }

    if (!validate_required_string(model, "model", "provider", false, error)) {
        return false;
    }
    cJSON* provider_item = cJSON_GetObjectItem(model, "provider");
    const std::string provider = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
    if (provider != "fake" && !validate_required_string(model, "model", "model", false, error)) {
        return false;
    }
    return true;
}

bool validate_search_secret_presence(cJSON* root, std::string* error) {
    cJSON* search = cJSON_GetObjectItem(root, "search");
    if (!search) {
        return true;
    }
    if (!json_is_object(search)) {
        return config_schema_error(error, "search", "object", search);
    }
    if (!validate_config_field_type(search, "search", "provider", CONFIG_FIELD_STRING, error) ||
        !validate_config_field_type(search, "search", "api_key", CONFIG_FIELD_STRING, error) ||
        !validate_config_field_type(search, "search", "has_api_key", CONFIG_FIELD_BOOL, error)) {
        return false;
    }

    cJSON* provider_item = cJSON_GetObjectItem(search, "provider");
    const std::string provider = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
    if (provider != "brave" && provider != "brave-free" && provider != "tavily") {
        return true;
    }

    cJSON* key_item = cJSON_GetObjectItem(search, "api_key");
    if (json_is_string(key_item) && !trim_copy(key_item->valuestring).empty()) {
        return true;
    }
    cJSON* has_key_item = cJSON_GetObjectItem(search, "has_api_key");
    if (!has_key_item) {
        return config_schema_error(error, "search.has_api_key", "bool", has_key_item);
    }
    return true;
}

bool validate_known_config_field_types(cJSON* root, std::string* error) {
    struct FieldSpec {
        const char* section;
        const char* key;
        ConfigFieldKind kind;
    };
    const FieldSpec fields[] = {
        {"model", "provider", CONFIG_FIELD_STRING},
        {"model", "model", CONFIG_FIELD_STRING},
        {"model", "api_key", CONFIG_FIELD_STRING},
        {"model", "base_url", CONFIG_FIELD_STRING},
        {"model", "token_env", CONFIG_FIELD_STRING},
        {"model", "temperature", CONFIG_FIELD_NUMBER},
        {"model", "max_response_tokens", CONFIG_FIELD_NUMBER},
        {"model", "context_window", CONFIG_FIELD_NUMBER},
        {"model", "model_max_output_tokens", CONFIG_FIELD_NUMBER},
        {"model_text", "provider", CONFIG_FIELD_STRING},
        {"model_text", "model", CONFIG_FIELD_STRING},
        {"model_text", "api_key", CONFIG_FIELD_STRING},
        {"model_text", "base_url", CONFIG_FIELD_STRING},
        {"model_text", "token_env", CONFIG_FIELD_STRING},
        {"model_text", "temperature", CONFIG_FIELD_NUMBER},
        {"model_text", "max_response_tokens", CONFIG_FIELD_NUMBER},
        {"model_text", "context_window", CONFIG_FIELD_NUMBER},
        {"model_text", "model_max_output_tokens", CONFIG_FIELD_NUMBER},
        {"tts", "provider", CONFIG_FIELD_STRING},
        {"tts", "api_key", CONFIG_FIELD_STRING},
        {"tts", "model", CONFIG_FIELD_STRING},
        {"tts", "voice_id", CONFIG_FIELD_STRING},
        {"tts", "emotion", CONFIG_FIELD_STRING},
        {"tts", "speed", CONFIG_FIELD_NUMBER},
        {"stt", "provider", CONFIG_FIELD_STRING},
        {"stt", "api_key", CONFIG_FIELD_STRING},
        {"stt", "model", CONFIG_FIELD_STRING},
        {"stt", "base_url", CONFIG_FIELD_STRING},
        {"stt", "secret_id", CONFIG_FIELD_STRING},
        {"stt", "secret_key", CONFIG_FIELD_STRING},
        {"stt", "region", CONFIG_FIELD_STRING},
        {"stt", "engine_model_type", CONFIG_FIELD_STRING},
        {"audio", "socket", CONFIG_FIELD_STRING},
        {"audio", "sample_rate", CONFIG_FIELD_NUMBER},
        {"audio", "channels", CONFIG_FIELD_NUMBER},
        {"audio", "bit_width", CONFIG_FIELD_NUMBER},
        {"audio_archive", "enabled", CONFIG_FIELD_BOOL},
        {"audio_archive", "storage_path", CONFIG_FIELD_STRING},
        {"audio_archive", "max_files", CONFIG_FIELD_NUMBER},
        {"audio_archive", "max_size_mb", CONFIG_FIELD_NUMBER},
        {"benchmark", "judge_model", CONFIG_FIELD_STRING},
        {"benchmark", "api_key", CONFIG_FIELD_STRING},
        {"benchmark", "benchmark_dir", CONFIG_FIELD_STRING},
        {"hid", "keyboard_device", CONFIG_FIELD_STRING},
        {"hid", "mouse_device", CONFIG_FIELD_STRING},
        {"hid", "frame_socket", CONFIG_FIELD_STRING},
        {"hid", "pointer_mode", CONFIG_FIELD_STRING},
        {"search", "provider", CONFIG_FIELD_STRING},
        {"search", "api_key", CONFIG_FIELD_STRING},
        {"search", "has_api_key", CONFIG_FIELD_BOOL},
        {"telemetry", "enabled", CONFIG_FIELD_BOOL},
        {"telemetry", "provider", CONFIG_FIELD_STRING},
        {"telemetry", "base_url", CONFIG_FIELD_STRING},
        {"telemetry", "public_key", CONFIG_FIELD_STRING},
        {"telemetry", "secret_key", CONFIG_FIELD_STRING},
        {"telemetry", "upload_screenshots", CONFIG_FIELD_BOOL},
        {"telemetry", "upload_timeout_sec", CONFIG_FIELD_NUMBER},
        {"telemetry", "max_retry", CONFIG_FIELD_NUMBER},
        {"telemetry", "tags", CONFIG_FIELD_ARRAY},
        {"telemetry", "environment", CONFIG_FIELD_STRING},
        {"agent", "custom_instruction", CONFIG_FIELD_STRING},
        {"agent", "additional_prompt", CONFIG_FIELD_STRING},
        {"agent", "input_mode", CONFIG_FIELD_STRING},
        {"agent", "trigger_mode", CONFIG_FIELD_STRING},
        {"agent", "vad_backend", CONFIG_FIELD_STRING},
        {"agent", "vad_model_path", CONFIG_FIELD_STRING},
        {"agent", "vad_helper_path", CONFIG_FIELD_STRING},
        {"agent", "vad_speech_threshold", CONFIG_FIELD_NUMBER},
        {"agent", "silence_ms", CONFIG_FIELD_NUMBER},
        {"agent", "min_speech_ms", CONFIG_FIELD_NUMBER},
        {"agent", "voice_followup_enabled", CONFIG_FIELD_BOOL},
        {"agent", "voice_followup_timeout_ms", CONFIG_FIELD_NUMBER},
        {"agent", "voice_first_turn_timeout_ms", CONFIG_FIELD_NUMBER},
        {"agent", "voice_max_turns", CONFIG_FIELD_NUMBER},
        {"agent", "voice_interrupt_on_wakeup", CONFIG_FIELD_BOOL},
        {"agent", "voice_streaming_tts_enabled", CONFIG_FIELD_BOOL},
        {"agent", "voice_tool_call_speech", CONFIG_FIELD_BOOL},
        {"agent", "voice_progress_speech_enabled", CONFIG_FIELD_BOOL},
        {"agent", "voice_speech_summary_enabled", CONFIG_FIELD_BOOL},
        {"agent", "voice_speech_max_runes", CONFIG_FIELD_NUMBER},
        {"agent", "voice_max_response_tokens", CONFIG_FIELD_NUMBER},
        {"agent", "max_iterations", CONFIG_FIELD_NUMBER},
        {"agent", "force_simple_loop", CONFIG_FIELD_BOOL},
        {"agent", "screenshot_keep_n", CONFIG_FIELD_NUMBER},
        {"agent", "screenshot_prune_interval", CONFIG_FIELD_NUMBER},
        {"agent", "screen_stable_timeout_ms", CONFIG_FIELD_NUMBER},
        {"agent", "screen_stable_ms", CONFIG_FIELD_NUMBER},
        {"agent", "screen_stable_diff_threshold", CONFIG_FIELD_NUMBER},
        {NULL, NULL, CONFIG_FIELD_STRING},
    };

    for (int i = 0; fields[i].section; ++i) {
        cJSON* section = cJSON_GetObjectItem(root, fields[i].section);
        if (!section) {
            continue;
        }
        if (!validate_config_field_type(section,
                                        fields[i].section,
                                        fields[i].key,
                                        fields[i].kind,
                                        error)) {
            return false;
        }
    }
    return true;
}

cJSON* add_object(cJSON* parent, const char* key) {
    cJSON* child = cJSON_CreateObject();
    cJSON_AddItemToObject(parent, key, child);
    return child;
}

cJSON* add_array(cJSON* parent, const char* key) {
    cJSON* child = cJSON_CreateArray();
    cJSON_AddItemToObject(parent, key, child);
    return child;
}

void add_string_array_to_object(cJSON* parent, const char* key, const std::vector<std::string>& values) {
    cJSON* array = add_array(parent, key);
    for (size_t i = 0; i < values.size(); ++i) {
        cJSON_AddItemToArray(array, cJSON_CreateString(values[i].c_str()));
    }
}

std::string shell_quote(const std::string& text) {
    std::string out = "'";
    for (size_t i = 0; i < text.size(); ++i) {
        if (text[i] == '\'') {
            out += "'\\''";
        } else {
            out.push_back(text[i]);
        }
    }
    out.push_back('\'');
    return out;
}

long long current_time_ms() {
    struct timeval tv;
    gettimeofday(&tv, NULL);
    return static_cast<long long>(tv.tv_sec) * 1000LL + tv.tv_usec / 1000;
}

void read_available_output(int fd, std::string* output, bool* pipe_open) {
    char buffer[512];
    while (*pipe_open) {
        ssize_t n = read(fd, buffer, sizeof(buffer));
        if (n > 0) {
            output->append(buffer, static_cast<size_t>(n));
        } else if (n == 0) {
            *pipe_open = false;
        } else if (errno == EINTR) {
            continue;
        } else if (errno == EAGAIN || errno == EWOULDBLOCK) {
            break;
        } else {
            output->append("read failed: ");
            output->append(strerror(errno));
            *pipe_open = false;
        }
    }
}

CommandResult run_shell_command(const std::string& command) {
    CommandResult result;
    FILE* pipe = popen(command.c_str(), "r");
    if (!pipe) {
        result.output = "popen failed";
        return result;
    }

    char buffer[512];
    while (fgets(buffer, sizeof(buffer), pipe)) {
        result.output += buffer;
    }

    int status = pclose(pipe);
    if (status >= 0 && WIFEXITED(status)) {
        result.exit_code = WEXITSTATUS(status);
    } else {
        result.exit_code = status;
    }
    return result;
}

CommandResult run_shell_command_with_timeout(const std::string& command, int timeout_ms) {
    if (timeout_ms <= 0) {
        return run_shell_command(command);
    }

    CommandResult result;
    int pipe_fds[2];
    if (pipe(pipe_fds) != 0) {
        result.output = std::string("pipe failed: ") + strerror(errno);
        return result;
    }

    pid_t pid = fork();
    if (pid < 0) {
        result.output = std::string("fork failed: ") + strerror(errno);
        close(pipe_fds[0]);
        close(pipe_fds[1]);
        return result;
    }

    if (pid == 0) {
        setpgid(0, 0);
        close(pipe_fds[0]);
        dup2(pipe_fds[1], STDOUT_FILENO);
        dup2(pipe_fds[1], STDERR_FILENO);
        if (pipe_fds[1] > STDERR_FILENO) {
            close(pipe_fds[1]);
        }
        execl("/bin/sh", "sh", "-c", command.c_str(), static_cast<char*>(NULL));
        _exit(127);
    }

    setpgid(pid, pid);
    close(pipe_fds[1]);

    int flags = fcntl(pipe_fds[0], F_GETFL, 0);
    if (flags >= 0) {
        fcntl(pipe_fds[0], F_SETFL, flags | O_NONBLOCK);
    }

    const long long deadline = current_time_ms() + timeout_ms;
    bool pipe_open = true;
    bool child_done = false;
    int child_status = -1;

    while (pipe_open || !child_done) {
        if (pipe_open) {
            read_available_output(pipe_fds[0], &result.output, &pipe_open);
        }

        if (!child_done) {
            pid_t waited = waitpid(pid, &child_status, WNOHANG);
            if (waited == pid) {
                child_done = true;
            } else if (waited < 0 && errno != EINTR) {
                child_done = true;
            }
        }

        if (!pipe_open && child_done) {
            break;
        }

        long long now = current_time_ms();
        if (now >= deadline) {
            kill(-pid, SIGKILL);
            kill(pid, SIGKILL);
            for (int i = 0; i < 20; ++i) {
                pid_t waited = waitpid(pid, &child_status, WNOHANG);
                if (waited == pid || (waited < 0 && errno != EINTR)) {
                    break;
                }
                usleep(1000);
            }
            close(pipe_fds[0]);
            result.exit_code = -1;
            result.timed_out = true;
            result.output = "agent status command timed out after " + std::to_string(timeout_ms) + " ms";
            return result;
        }

        int wait_ms = 50;
        long long remaining_ms = deadline - now;
        if (remaining_ms < wait_ms) {
            wait_ms = remaining_ms > 0 ? static_cast<int>(remaining_ms) : 0;
        }

        if (pipe_open && wait_ms > 0) {
            fd_set read_fds;
            FD_ZERO(&read_fds);
            FD_SET(pipe_fds[0], &read_fds);
            struct timeval timeout;
            timeout.tv_sec = wait_ms / 1000;
            timeout.tv_usec = (wait_ms % 1000) * 1000;
            select(pipe_fds[0] + 1, &read_fds, NULL, NULL, &timeout);
        } else if (wait_ms > 0) {
            usleep(static_cast<useconds_t>(wait_ms) * 1000);
        }
    }

    close(pipe_fds[0]);
    if (child_status >= 0 && WIFEXITED(child_status)) {
        result.exit_code = WEXITSTATUS(child_status);
    } else if (child_status >= 0 && WIFSIGNALED(child_status)) {
        result.exit_code = 128 + WTERMSIG(child_status);
    } else {
        result.exit_code = child_status;
    }
    return result;
}

CommandResult run_command_with_stdin(const std::string& command, const std::string& input, int timeout_ms) {
    CommandResult result;
    int stdin_pipe[2];
    int stdout_pipe[2];

    if (pipe(stdin_pipe) != 0 || pipe(stdout_pipe) != 0) {
        result.output = std::string("pipe failed: ") + strerror(errno);
        return result;
    }

    pid_t pid = fork();
    if (pid < 0) {
        result.output = std::string("fork failed: ") + strerror(errno);
        close(stdin_pipe[0]);
        close(stdin_pipe[1]);
        close(stdout_pipe[0]);
        close(stdout_pipe[1]);
        return result;
    }

    if (pid == 0) {
        // Child process
        close(stdin_pipe[1]);
        close(stdout_pipe[0]);

        dup2(stdin_pipe[0], STDIN_FILENO);
        dup2(stdout_pipe[1], STDOUT_FILENO);
        dup2(stdout_pipe[1], STDERR_FILENO);

        close(stdin_pipe[0]);
        close(stdout_pipe[1]);

        execl("/bin/sh", "sh", "-c", command.c_str(), static_cast<char*>(NULL));
        _exit(127);
    }

    // Parent process
    close(stdin_pipe[0]);
    close(stdout_pipe[1]);

    // Write input to stdin
    if (!input.empty()) {
        ssize_t written = write(stdin_pipe[1], input.data(), input.size());
        if (written < 0 || static_cast<size_t>(written) != input.size()) {
            // Non-fatal, continue
        }
    }
    close(stdin_pipe[1]);

    // Read output with timeout
    time_t start_time = time(NULL);
    time_t deadline = start_time + (timeout_ms / 1000);

    while (true) {
        fd_set read_fds;
        FD_ZERO(&read_fds);
        FD_SET(stdout_pipe[0], &read_fds);

        time_t now = time(NULL);
        if (now >= deadline) {
            result.timed_out = true;
            kill(pid, SIGKILL);
            break;
        }

        struct timeval timeout;
        timeout.tv_sec = deadline - now;
        timeout.tv_usec = 0;

        int select_result = select(stdout_pipe[0] + 1, &read_fds, NULL, NULL, &timeout);
        if (select_result > 0) {
            char buffer[4096];
            ssize_t bytes_read = read(stdout_pipe[0], buffer, sizeof(buffer));
            if (bytes_read > 0) {
                result.output.append(buffer, static_cast<size_t>(bytes_read));
            } else {
                break;
            }
        } else if (select_result == 0) {
            result.timed_out = true;
            kill(pid, SIGKILL);
            break;
        } else {
            break;
        }
    }

    close(stdout_pipe[0]);

    int status;
    waitpid(pid, &status, 0);

    if (WIFEXITED(status)) {
        result.exit_code = WEXITSTATUS(status);
    } else if (WIFSIGNALED(status)) {
        result.exit_code = 128 + WTERMSIG(status);
    } else {
        result.exit_code = -1;
    }

    return result;
}

bool command_exists(const char* name) {
    if (!name || name[0] == '\0') {
        return false;
    }
    static std::map<std::string, bool> cache;
    std::string key(name);
    auto it = cache.find(key);
    if (it != cache.end()) {
        return it->second;
    }
    std::string cmd = "command -v ";
    cmd += name;
    cmd += " >/dev/null 2>&1";
    CommandResult result = run_shell_command(cmd);
    bool ok = result.exit_code == 0;
    cache[key] = ok;
    return ok;
}

std::string trim_trailing_newlines(const std::string& text) {
    size_t end = text.size();
    while (end > 0 && (text[end - 1] == '\n' || text[end - 1] == '\r')) {
        --end;
    }
    return text.substr(0, end);
}

std::vector<std::string> split_lines(const std::string& text) {
    std::vector<std::string> lines;
    std::istringstream input(text);
    std::string line;
    while (std::getline(input, line)) {
        if (!line.empty() && line[line.size() - 1] == '\r') {
            line.resize(line.size() - 1);
        }
        lines.push_back(line);
    }
    return lines;
}

std::string line_or_empty(const std::vector<std::string>& lines, size_t index) {
    return index < lines.size() ? trim_copy(lines[index]) : "";
}

bool load_system_env_proxy(const std::string& path, SystemProxy* proxy) {
    if (!proxy || path.empty() || !file_exists(path.c_str())) {
        return false;
    }

    std::string content = read_file_contents(path.c_str(), kMaxSystemEnvSize);
    std::vector<aiden::EnvAssignment> assignments;
    SystemProxy loaded;
    std::string error;
    if (!aiden::parse_system_env_content(content, &assignments, &loaded, &error)) {
        return false;
    }

    if (has_proxy_url(loaded) && loaded.no_proxy.empty()) {
        loaded.no_proxy = kDefaultNoProxy;
    }

    bool has_values = has_proxy_url(loaded) || !trim_copy(loaded.no_proxy).empty();
    if (has_values) {
        *proxy = loaded;
    }
    return has_values;
}

std::string find_key_value_line(const std::string& text, const std::string& key) {
    const std::string prefix = key + "=";
    std::istringstream input(text);
    std::string line;
    while (std::getline(input, line)) {
        if (starts_with(line, prefix)) {
            return trim_copy(line.substr(prefix.size()));
        }
    }
    return "";
}

std::string extract_iw_ssid(const std::string& text) {
    std::istringstream input(text);
    std::string line;
    while (std::getline(input, line)) {
        std::string trimmed = trim_copy(line);
        if (starts_with(trimmed, "SSID:")) {
            return trim_copy(trimmed.substr(5));
        }
    }
    return "";
}

bool write_all(int fd, const void* data, size_t size) {
    const char* bytes = static_cast<const char*>(data);
    size_t offset = 0;
    while (offset < size) {
        ssize_t n = write(fd, bytes + offset, size - offset);
        if (n < 0) {
            if (errno == EINTR) {
                continue;
            }
            return false;
        }
        offset += static_cast<size_t>(n);
    }
    return true;
}

bool is_socket_timeout(int error_code) {
    return error_code == EAGAIN || error_code == EWOULDBLOCK;
}

ReadStatus read_until(int fd,
                      const std::string& delimiter,
                      size_t max_length,
                      std::string* out) {
    if (!out) {
        return READ_STATUS_ERROR;
    }

    out->clear();
    char ch = '\0';
    while (true) {
        ssize_t n = read(fd, &ch, 1);
        if (n < 0) {
            if (errno == EINTR) {
                continue;
            }
            if (is_socket_timeout(errno)) {
                return READ_STATUS_TIMEOUT;
            }
            return READ_STATUS_ERROR;
        }
        if (n == 0) {
            return READ_STATUS_EOF;
        }

        out->push_back(ch);
        if (out->size() > max_length) {
            return READ_STATUS_LIMIT_EXCEEDED;
        }
        if (out->size() >= delimiter.size() &&
            out->compare(out->size() - delimiter.size(), delimiter.size(), delimiter) == 0) {
            return READ_STATUS_OK;
        }
    }
}

ReadStatus read_exact(int fd, std::string* out, size_t length) {
    if (!out) {
        return READ_STATUS_ERROR;
    }

    out->assign(length, '\0');
    size_t offset = 0;
    while (offset < length) {
        ssize_t n = read(fd, &(*out)[offset], length - offset);
        if (n < 0) {
            if (errno == EINTR) {
                continue;
            }
            if (is_socket_timeout(errno)) {
                return READ_STATUS_TIMEOUT;
            }
            return READ_STATUS_ERROR;
        }
        if (n == 0) {
            return READ_STATUS_EOF;
        }
        offset += static_cast<size_t>(n);
    }
    return READ_STATUS_OK;
}

void set_request_error(HttpRequest* request,
                       int status_code,
                       const char* status_text,
                       const char* body) {
    if (!request) {
        return;
    }
    request->error_status_code = status_code;
    request->error_status_text = status_text ? status_text : "Bad Request";
    request->error_body = body ? body : "invalid request";
}

bool parse_size(const std::string& text, size_t* value) {
    if (!value) {
        return false;
    }

    std::string trimmed = trim_copy(text);
    if (trimmed.empty()) {
        return false;
    }

    errno = 0;
    char* end = NULL;
    unsigned long parsed = strtoul(trimmed.c_str(), &end, 10);
    if (errno != 0 || !end || *end != '\0') {
        return false;
    }

    *value = static_cast<size_t>(parsed);
    return true;
}

bool is_allowed_bind_address(const std::string& bind_address) {
    return bind_address == kAnyBindAddress || bind_address == kUsbBindAddress || bind_address == kLoopbackBindAddress;
}

bool set_socket_recv_timeout(int fd, int timeout_seconds) {
    struct timeval timeout;
    timeout.tv_sec = timeout_seconds;
    timeout.tv_usec = 0;
    return setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout)) == 0;
}

HttpRequest parse_request(int client_fd) {
    HttpRequest req;
    std::string headers;
    ReadStatus header_status = read_until(client_fd, "\r\n\r\n", kMaxHttpHeaderSize, &headers);
    if (header_status == READ_STATUS_TIMEOUT) {
        set_request_error(&req, 408, "Request Timeout", "request read timed out");
        return req;
    }
    if (header_status == READ_STATUS_LIMIT_EXCEEDED) {
        set_request_error(&req, 413, "Payload Too Large", "request headers too large");
        return req;
    }
    if (header_status != READ_STATUS_OK) {
        set_request_error(&req, 400, "Bad Request", "invalid HTTP request");
        return req;
    }

    std::istringstream stream(headers);
    std::string line;
    if (std::getline(stream, line)) {
        std::istringstream first(line);
        first >> req.method >> req.path;
        if (req.method.empty() || req.path.empty()) {
            set_request_error(&req, 400, "Bad Request", "invalid HTTP request line");
            return req;
        }
        size_t query_pos = req.path.find('?');
        if (query_pos != std::string::npos) {
            req.path = req.path.substr(0, query_pos);
        }
    } else {
        set_request_error(&req, 400, "Bad Request", "missing HTTP request line");
        return req;
    }

    size_t content_length = 0;
    while (std::getline(stream, line)) {
        std::string value;
        if (consume_prefix(line.c_str(), "Content-Length:", &value)) {
            if (!parse_size(value, &content_length)) {
                set_request_error(&req, 400, "Bad Request", "invalid Content-Length header");
                return req;
            }
        }
    }

    if (content_length > 0) {
        if (content_length > kMaxHttpBodySize) {
            set_request_error(&req, 413, "Payload Too Large", "request body too large");
            return req;
        }

        ReadStatus body_status = read_exact(client_fd, &req.body, content_length);
        if (body_status == READ_STATUS_TIMEOUT) {
            set_request_error(&req, 408, "Request Timeout", "request body read timed out");
            return req;
        }
        if (body_status != READ_STATUS_OK) {
            set_request_error(&req, 400, "Bad Request", "truncated request body");
            return req;
        }
    }

    return req;
}

void send_response(int client_fd,
                   int status_code,
                   const std::string& status_text,
                   const std::string& content_type,
                   const std::string& body) {
    std::ostringstream header;
    header << "HTTP/1.1 " << status_code << " " << status_text << "\r\n";
    header << "Content-Type: " << content_type << "\r\n";
    header << "Content-Length: " << body.size() << "\r\n";
    header << "Connection: close\r\n\r\n";

    std::string header_text = header.str();
    write_all(client_fd, header_text.data(), header_text.size());
    write_all(client_fd, body.data(), body.size());
}

bool validate_agent_config_json(cJSON* root, std::string* error = NULL) {
    if (!json_is_object(root)) {
        return config_schema_error(error, "root", "object", root);
    }

    const char* sections[] = {
        "model", "model_text", "tts", "stt", "audio", "audio_archive",
        "benchmark", "hid", "search", "telemetry", "agent", NULL,
    };
    for (int i = 0; sections[i]; ++i) {
        cJSON* section = cJSON_GetObjectItem(root, sections[i]);
        if (section && !json_is_object(section)) {
            return config_schema_error(error, sections[i], "object", section);
        }
    }
    if (!validate_model_section(root, error) ||
        !validate_known_config_field_types(root, error) ||
        !validate_search_secret_presence(root, error)) {
        return false;
    }
    return true;
}

bool load_agent_config_via_cli(const Options& options,
                               aiden::AgentToml* config,
                               std::string* error) {
    if (error) {
        error->clear();
    }
    if (!config) {
        return true;
    }

    const char* agent_bin = agent_bin_path();
    if (!file_exists(agent_bin)) {
        if (error) {
            *error = std::string("agent config unavailable: agent binary not found at ") + agent_bin;
        }
        std::cerr << "[config] agent binary not found at " << agent_bin
                  << ", cannot load agent config\n";
        return false;
    }

    std::string cmd = shell_quote(agent_bin) + " config --config="
                    + shell_quote(options.agent_config_path) + " --format=json";
    CommandResult result = run_command_with_stdin(cmd, "", 2000);
    if (result.timed_out) {
        if (error) {
            *error = "agent config timed out";
        }
        return false;
    }
    if (result.exit_code != 0) {
        if (error) {
            *error = "agent config load failed";
        }
        std::cerr << "[config] agent config exited " << result.exit_code
                  << ": " << result.output << "\n";
        return false;
    }

    cJSON* resolved = cJSON_Parse(result.output.c_str());
    if (!resolved) {
        if (error) {
            *error = "agent config returned an unexpected response";
        }
        std::cerr << "[config] agent config returned invalid JSON: "
                  << result.output << "\n";
        return false;
    }

    std::string schema_error;
    if (!validate_agent_config_json(resolved, &schema_error)) {
        if (error) {
            *error = schema_error.empty() ? "agent config invalid schema" : schema_error;
        }
        std::cerr << "[config] agent config returned invalid schema: "
                  << (schema_error.empty() ? "unknown schema error" : schema_error) << "\n"
                  << result.output << "\n";
        cJSON_Delete(resolved);
        return false;
    }

    *config = aiden::AgentToml();
    update_config_from_json(resolved, config);
    cJSON_Delete(resolved);
    return true;
}

void load_current_agent_config(const Options& options,
                               aiden::AgentToml* config,
                               std::string* load_error) {
    if (load_error) {
        load_error->clear();
    }
    if (!config) {
        return;
    }

    std::string config_error;
    if (!load_agent_config_via_cli(options, config, &config_error)) {
        if (load_error) {
            *load_error = config_error;
        }
        *config = aiden::AgentToml();
    }
}

void preserve_redacted_agent_secrets(const Options& options, aiden::AgentToml* config) {
    if (!config || !config->search.api_key.empty() || !config->search.has_api_key) {
        return;
    }

    aiden::AgentToml stored;
    std::string load_error;
    if (!aiden::load_agent_toml(options.agent_config_path.c_str(), stored, &load_error)) {
        return;
    }
    if (!stored.search.api_key.empty()) {
        config->search.api_key = stored.search.api_key;
        config->search.has_api_key = true;
    }
}

bool json_bool_value(cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    return json_is_bool(item) && json_is_type(item, cJSON_True);
}

bool json_has_nonempty_string(cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    return json_is_string(item) && !trim_copy(item->valuestring).empty();
}

bool json_secret_present(cJSON* obj, const char* key) {
    if (json_has_nonempty_string(obj, key)) {
        return true;
    }
    const std::string has_key = std::string("has_") + key;
    return json_bool_value(obj, has_key.c_str());
}

void load_current_wifi_config(const Options& options,
                              aiden::WifiNetworkConfig* config,
                              std::string* load_error) {
    if (load_error) {
        load_error->clear();
    }
    if (!config) {
        return;
    }

    *config = aiden::WifiNetworkConfig();
    if (file_exists(options.wifi_config_path.c_str())) {
        aiden::load_wifi_config(options.wifi_config_path.c_str(), *config, load_error);
    }
}

cJSON* config_to_json(const aiden::AgentToml& config, bool include_secrets = false) {
    cJSON* root = cJSON_CreateObject();

    cJSON* model = add_object(root, "model");
    cJSON_AddStringToObject(model, "provider", config.model.provider.c_str());
    cJSON_AddStringToObject(model, "api_key", config.model.api_key.c_str());
    cJSON_AddStringToObject(model, "model", config.model.model.c_str());
    cJSON_AddStringToObject(model, "base_url", config.model.base_url.c_str());
    cJSON_AddStringToObject(model, "token_env", config.model.token_env.c_str());
    cJSON_AddNumberToObject(model, "temperature", config.model.temperature);
    cJSON_AddNumberToObject(model, "max_response_tokens", config.model.max_response_tokens);
    cJSON_AddNumberToObject(model, "context_window", config.model.context_window);
    cJSON_AddNumberToObject(model, "model_max_output_tokens", config.model.model_max_output_tokens);

    cJSON* model_text = add_object(root, "model_text");
    cJSON_AddStringToObject(model_text, "provider", config.model_text.provider.c_str());
    cJSON_AddStringToObject(model_text, "api_key", config.model_text.api_key.c_str());
    cJSON_AddStringToObject(model_text, "model", config.model_text.model.c_str());
    cJSON_AddStringToObject(model_text, "base_url", config.model_text.base_url.c_str());
    cJSON_AddStringToObject(model_text, "token_env", config.model_text.token_env.c_str());
    cJSON_AddNumberToObject(model_text, "temperature", config.model_text.temperature);
    cJSON_AddNumberToObject(model_text, "max_response_tokens", config.model_text.max_response_tokens);
    cJSON_AddNumberToObject(model_text, "context_window", config.model_text.context_window);
    cJSON_AddNumberToObject(model_text, "model_max_output_tokens", config.model_text.model_max_output_tokens);

    cJSON* tts = add_object(root, "tts");
    cJSON_AddStringToObject(tts, "provider", config.tts.provider.c_str());
    cJSON_AddStringToObject(tts, "api_key", config.tts.api_key.c_str());
    cJSON_AddStringToObject(tts, "model", config.tts.model.c_str());
    cJSON_AddStringToObject(tts, "voice_id", config.tts.voice_id.c_str());
    cJSON_AddStringToObject(tts, "emotion", config.tts.emotion.c_str());
    cJSON_AddNumberToObject(tts, "speed", config.tts.speed);

    cJSON* stt = add_object(root, "stt");
    cJSON_AddStringToObject(stt, "provider", config.stt.provider.c_str());
    cJSON_AddStringToObject(stt, "api_key", config.stt.api_key.c_str());
    cJSON_AddStringToObject(stt, "model", config.stt.model.c_str());
    cJSON_AddStringToObject(stt, "base_url", config.stt.base_url.c_str());
    cJSON_AddStringToObject(stt, "secret_id", config.stt.secret_id.c_str());
    cJSON_AddStringToObject(stt, "secret_key", config.stt.secret_key.c_str());
    cJSON_AddStringToObject(stt, "region", config.stt.region.c_str());
    cJSON_AddStringToObject(stt, "engine_model_type", config.stt.engine_model_type.c_str());

    cJSON* audio = add_object(root, "audio");
    cJSON_AddStringToObject(audio, "socket", config.audio.socket.c_str());
    cJSON_AddNumberToObject(audio, "sample_rate", config.audio.sample_rate);
    cJSON_AddNumberToObject(audio, "channels", config.audio.channels);
    cJSON_AddNumberToObject(audio, "bit_width", config.audio.bit_width);

    cJSON* audio_archive = add_object(root, "audio_archive");
    cJSON_AddBoolToObject(audio_archive, "enabled", config.audio_archive.enabled ? 1 : 0);
    cJSON_AddStringToObject(audio_archive, "storage_path", config.audio_archive.storage_path.c_str());
    cJSON_AddNumberToObject(audio_archive, "max_files", config.audio_archive.max_files);
    cJSON_AddNumberToObject(audio_archive, "max_size_mb", config.audio_archive.max_size_mb);

    cJSON* benchmark = add_object(root, "benchmark");
    cJSON_AddStringToObject(benchmark, "judge_model", config.benchmark.judge_model.c_str());
    if (include_secrets) {
        cJSON_AddStringToObject(benchmark, "api_key", config.benchmark.api_key.c_str());
    } else {
        cJSON_AddBoolToObject(benchmark, "has_api_key", !config.benchmark.api_key.empty());
    }
    cJSON_AddStringToObject(benchmark, "benchmark_dir", config.benchmark.benchmark_dir.c_str());

    cJSON* hid = add_object(root, "hid");
    cJSON_AddStringToObject(hid, "keyboard_device", config.hid.keyboard_device.c_str());
    cJSON_AddStringToObject(hid, "mouse_device", config.hid.mouse_device.c_str());
    cJSON_AddStringToObject(hid, "frame_socket", config.hid.frame_socket.c_str());
    cJSON_AddStringToObject(hid, "pointer_mode", normalize_pointer_mode(config.hid.pointer_mode).c_str());

    cJSON* search = add_object(root, "search");
    cJSON_AddStringToObject(search, "provider", config.search.provider.c_str());
    cJSON_AddBoolToObject(search, "has_api_key",
                          (config.search.has_api_key || !config.search.api_key.empty()) ? 1 : 0);

    cJSON* telemetry = add_object(root, "telemetry");
    cJSON_AddBoolToObject(telemetry, "enabled", config.telemetry.enabled ? 1 : 0);
    cJSON_AddStringToObject(telemetry, "provider", config.telemetry.provider.c_str());
    cJSON_AddStringToObject(telemetry, "base_url", config.telemetry.base_url.c_str());
    cJSON_AddStringToObject(telemetry, "public_key", config.telemetry.public_key.c_str());
    cJSON_AddStringToObject(telemetry, "secret_key", config.telemetry.secret_key.c_str());
    cJSON_AddBoolToObject(telemetry, "upload_screenshots", config.telemetry.upload_screenshots ? 1 : 0);
    cJSON_AddNumberToObject(telemetry, "upload_timeout_sec", config.telemetry.upload_timeout_sec);
    cJSON_AddNumberToObject(telemetry, "max_retry", config.telemetry.max_retry);
    add_string_array_to_object(telemetry, "tags", config.telemetry.tags);
    cJSON_AddStringToObject(telemetry, "environment", config.telemetry.environment.c_str());

    cJSON* agent = add_object(root, "agent");
    cJSON_AddStringToObject(agent, "custom_instruction", config.custom_instruction.c_str());
    cJSON_AddStringToObject(agent, "additional_prompt", config.additional_prompt.c_str());
    cJSON_AddStringToObject(agent, "input_mode", config.input_mode.c_str());
    cJSON_AddStringToObject(agent, "trigger_mode", config.trigger_mode.c_str());
    cJSON_AddStringToObject(agent, "vad_backend", config.vad_backend.c_str());
    cJSON_AddStringToObject(agent, "vad_model_path", config.vad_model_path.c_str());
    cJSON_AddStringToObject(agent, "vad_helper_path", config.vad_helper_path.c_str());
    cJSON_AddNumberToObject(agent, "vad_speech_threshold", config.vad_speech_threshold);
    cJSON_AddNumberToObject(agent, "silence_ms", config.silence_ms);
    cJSON_AddNumberToObject(agent, "min_speech_ms", config.min_speech_ms);
    cJSON_AddBoolToObject(agent, "voice_followup_enabled", config.voice_followup_enabled ? 1 : 0);
    cJSON_AddNumberToObject(agent, "voice_followup_timeout_ms", config.voice_followup_timeout_ms);
    cJSON_AddNumberToObject(agent, "voice_first_turn_timeout_ms", config.voice_first_turn_timeout_ms);
    cJSON_AddNumberToObject(agent, "voice_max_turns", config.voice_max_turns);
    cJSON_AddBoolToObject(agent, "voice_interrupt_on_wakeup", config.voice_interrupt_on_wakeup ? 1 : 0);
    cJSON_AddBoolToObject(agent, "voice_streaming_tts_enabled", config.voice_streaming_tts_enabled ? 1 : 0);
    cJSON_AddBoolToObject(agent, "voice_tool_call_speech", config.voice_tool_call_speech ? 1 : 0);
    cJSON_AddBoolToObject(agent, "voice_progress_speech_enabled", config.voice_progress_speech_enabled ? 1 : 0);
    cJSON_AddBoolToObject(agent, "voice_speech_summary_enabled", config.voice_speech_summary_enabled ? 1 : 0);
    cJSON_AddNumberToObject(agent, "voice_speech_max_runes", config.voice_speech_max_runes);
    cJSON_AddNumberToObject(agent, "voice_max_response_tokens", config.voice_max_response_tokens);
    cJSON_AddNumberToObject(agent, "max_iterations", config.max_iterations);
    cJSON_AddBoolToObject(agent, "force_simple_loop", config.force_simple_loop ? 1 : 0);
    cJSON_AddNumberToObject(agent, "screenshot_keep_n", config.screenshot_keep_n);
    cJSON_AddNumberToObject(agent, "screenshot_prune_interval", config.screenshot_prune_interval);
    cJSON_AddNumberToObject(agent, "screen_stable_timeout_ms", config.screen_stable_timeout_ms);
    cJSON_AddNumberToObject(agent, "screen_stable_ms", config.screen_stable_ms);
    cJSON_AddNumberToObject(agent, "screen_stable_diff_threshold", config.screen_stable_diff_threshold);

    return root;
}

cJSON* wifi_to_json(const aiden::WifiNetworkConfig& wifi) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "ssid", wifi.ssid.c_str());
    cJSON_AddStringToObject(root, "psk", wifi.psk.c_str());
    cJSON_AddStringToObject(root, "country", wifi.country.c_str());
    cJSON* networks = add_array(root, "networks");
    for (size_t i = 0; i < wifi.networks.size(); ++i) {
        const aiden::WifiNetwork& network = wifi.networks[i];
        cJSON* item = cJSON_CreateObject();
        cJSON_AddStringToObject(item, "ssid", network.ssid.c_str());
        cJSON_AddStringToObject(item, "psk", network.psk.c_str());
        cJSON_AddNumberToObject(item, "priority", network.priority);
        cJSON_AddBoolToObject(item, "scan_ssid", network.scan_ssid ? 1 : 0);
        cJSON_AddBoolToObject(item, "disabled", network.disabled ? 1 : 0);
        cJSON_AddItemToArray(networks, item);
    }
    return root;
}

cJSON* wifi_status_to_json(const WifiRuntimeStatus& status) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "connected", status.connected ? 1 : 0);
    cJSON_AddStringToObject(root, "ssid", status.ssid.c_str());
    cJSON_AddStringToObject(root, "ip_address", status.ip_address.c_str());
    cJSON_AddStringToObject(root, "state", status.state.c_str());
    cJSON_AddStringToObject(root, "detail", status.detail.c_str());
    return root;
}

std::string cjson_to_string(cJSON* json) {
    char* printed = cJSON_PrintUnformatted(json);
    if (!printed) {
        return "{}";
    }
    std::string text = printed;
    free(printed);
    return text;
}

ApiResponse make_json_error(int status_code, const std::string& message) {
    ApiResponse response;
    response.status_code = status_code;
    switch (status_code) {
        case 400: response.status_text = "Bad Request"; break;
        case 404: response.status_text = "Not Found"; break;
        case 503: response.status_text = "Service Unavailable"; break;
        default:  response.status_text = "Internal Server Error"; break;
    }

    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 0);
    cJSON_AddStringToObject(root, "error", message.c_str());
    response.body = cjson_to_string(root);
    cJSON_Delete(root);
    return response;
}

ApiResponse make_json_ok(cJSON* root) {
    ApiResponse response;
    response.body = cjson_to_string(root);
    cJSON_Delete(root);
    return response;
}

void set_json_str(std::string* dst, cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    if (dst && json_is_string(item)) {
        *dst = item->valuestring;
    }
}

void set_json_int(int* dst, cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    if (dst && json_is_number(item)) {
        *dst = item->valueint;
    }
}

void set_json_double(double* dst, cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    if (dst && json_is_number(item)) {
        *dst = item->valuedouble;
    }
}

void set_json_bool(bool* dst, cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    if (dst && json_is_bool(item)) {
        *dst = json_is_type(item, cJSON_True);
    }
}

void set_json_string_vector(std::vector<std::string>* dst, cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    if (!dst) {
        return;
    }
    if (json_is_array(item)) {
        std::vector<std::string> values;
        int count = cJSON_GetArraySize(item);
        for (int i = 0; i < count; ++i) {
            cJSON* child = cJSON_GetArrayItem(item, i);
            if (json_is_string(child)) {
                std::string value = trim_copy(child->valuestring);
                if (!value.empty()) {
                    values.push_back(value);
                }
            }
        }
        *dst = values;
        return;
    }
    if (json_is_string(item)) {
        std::vector<std::string> values;
        std::string text = item->valuestring;
        size_t start = 0;
        while (start <= text.size()) {
            size_t comma = text.find(',', start);
            std::string part = trim_copy(text.substr(start, comma == std::string::npos ? std::string::npos : comma - start));
            if (!part.empty()) {
                values.push_back(part);
            }
            if (comma == std::string::npos) {
                break;
            }
            start = comma + 1;
        }
        *dst = values;
    }
}

void update_model_from_json(cJSON* obj, aiden::ModelToml* m) {
    if (!json_is_object(obj) || !m) return;
    set_json_str(&m->provider, obj, "provider");
    set_json_str(&m->model, obj, "model");
    set_json_str(&m->base_url, obj, "base_url");
    set_json_str(&m->api_key, obj, "api_key");
    set_json_str(&m->token_env, obj, "token_env");
    set_json_double(&m->temperature, obj, "temperature");
    set_json_int(&m->max_response_tokens, obj, "max_response_tokens");
    set_json_int(&m->context_window, obj, "context_window");
    set_json_int(&m->model_max_output_tokens, obj, "model_max_output_tokens");
}

void update_config_from_json(cJSON* root, aiden::AgentToml* config) {
    if (!root || !config) {
        return;
    }

    update_model_from_json(cJSON_GetObjectItem(root, "model"), &config->model);
    update_model_from_json(cJSON_GetObjectItem(root, "model_text"), &config->model_text);

    cJSON* tts = cJSON_GetObjectItem(root, "tts");
    if (json_is_object(tts)) {
        set_json_str(&config->tts.provider, tts, "provider");
        set_json_str(&config->tts.api_key, tts, "api_key");
        set_json_str(&config->tts.model, tts, "model");
        set_json_str(&config->tts.voice_id, tts, "voice_id");
        set_json_str(&config->tts.emotion, tts, "emotion");
        set_json_double(&config->tts.speed, tts, "speed");
    }

    cJSON* stt = cJSON_GetObjectItem(root, "stt");
    if (json_is_object(stt)) {
        set_json_str(&config->stt.provider, stt, "provider");
        set_json_str(&config->stt.api_key, stt, "api_key");
        set_json_str(&config->stt.model, stt, "model");
        set_json_str(&config->stt.base_url, stt, "base_url");
        set_json_str(&config->stt.secret_id, stt, "secret_id");
        set_json_str(&config->stt.secret_key, stt, "secret_key");
        set_json_str(&config->stt.region, stt, "region");
        set_json_str(&config->stt.engine_model_type, stt, "engine_model_type");
    }

    cJSON* audio = cJSON_GetObjectItem(root, "audio");
    if (json_is_object(audio)) {
        set_json_str(&config->audio.socket, audio, "socket");
        set_json_int(&config->audio.sample_rate, audio, "sample_rate");
        set_json_int(&config->audio.channels, audio, "channels");
        set_json_int(&config->audio.bit_width, audio, "bit_width");
    }

    cJSON* audio_archive = cJSON_GetObjectItem(root, "audio_archive");
    if (json_is_object(audio_archive)) {
        set_json_bool(&config->audio_archive.enabled, audio_archive, "enabled");
        set_json_str(&config->audio_archive.storage_path, audio_archive, "storage_path");
        set_json_int(&config->audio_archive.max_files, audio_archive, "max_files");
        set_json_int(&config->audio_archive.max_size_mb, audio_archive, "max_size_mb");
    }

    cJSON* benchmark = cJSON_GetObjectItem(root, "benchmark");
    if (json_is_object(benchmark)) {
        set_json_str(&config->benchmark.judge_model, benchmark, "judge_model");
        cJSON* key_item = cJSON_GetObjectItem(benchmark, "api_key");
        if (json_is_string(key_item)) {
            std::string api_key = trim_copy(key_item->valuestring);
            if (!api_key.empty()) {
                config->benchmark.api_key = api_key;
            }
        }
        set_json_str(&config->benchmark.benchmark_dir, benchmark, "benchmark_dir");
    }

    cJSON* hid = cJSON_GetObjectItem(root, "hid");
    if (json_is_object(hid)) {
        set_json_str(&config->hid.keyboard_device, hid, "keyboard_device");
        set_json_str(&config->hid.mouse_device, hid, "mouse_device");
        set_json_str(&config->hid.frame_socket, hid, "frame_socket");
        set_json_str(&config->hid.pointer_mode, hid, "pointer_mode");
    }

    cJSON* search = cJSON_GetObjectItem(root, "search");
    if (json_is_object(search)) {
        set_json_str(&config->search.provider, search, "provider");
        set_json_bool(&config->search.has_api_key, search, "has_api_key");
        cJSON* key_item = cJSON_GetObjectItem(search, "api_key");
        if (json_is_string(key_item) && strlen(key_item->valuestring) > 0) {
            config->search.api_key = key_item->valuestring;
            config->search.has_api_key = true;
        }
    }

    cJSON* telemetry = cJSON_GetObjectItem(root, "telemetry");
    if (json_is_object(telemetry)) {
        set_json_bool(&config->telemetry.enabled, telemetry, "enabled");
        set_json_str(&config->telemetry.provider, telemetry, "provider");
        set_json_str(&config->telemetry.base_url, telemetry, "base_url");
        set_json_str(&config->telemetry.public_key, telemetry, "public_key");
        set_json_str(&config->telemetry.secret_key, telemetry, "secret_key");
        set_json_bool(&config->telemetry.upload_screenshots, telemetry, "upload_screenshots");
        set_json_int(&config->telemetry.upload_timeout_sec, telemetry, "upload_timeout_sec");
        set_json_int(&config->telemetry.max_retry, telemetry, "max_retry");
        set_json_string_vector(&config->telemetry.tags, telemetry, "tags");
        set_json_str(&config->telemetry.environment, telemetry, "environment");
    }

    cJSON* agent = cJSON_GetObjectItem(root, "agent");
    if (json_is_object(agent)) {
        set_json_str(&config->custom_instruction, agent, "custom_instruction");
        set_json_str(&config->additional_prompt, agent, "additional_prompt");
        set_json_str(&config->input_mode, agent, "input_mode");
        set_json_str(&config->trigger_mode, agent, "trigger_mode");
        set_json_str(&config->vad_backend, agent, "vad_backend");
        set_json_str(&config->vad_model_path, agent, "vad_model_path");
        set_json_str(&config->vad_helper_path, agent, "vad_helper_path");
        set_json_double(&config->vad_speech_threshold, agent, "vad_speech_threshold");
        set_json_int(&config->silence_ms, agent, "silence_ms");
        set_json_int(&config->min_speech_ms, agent, "min_speech_ms");
        set_json_bool(&config->voice_followup_enabled, agent, "voice_followup_enabled");
        set_json_int(&config->voice_followup_timeout_ms, agent, "voice_followup_timeout_ms");
        set_json_int(&config->voice_first_turn_timeout_ms, agent, "voice_first_turn_timeout_ms");
        set_json_int(&config->voice_max_turns, agent, "voice_max_turns");
        set_json_bool(&config->voice_interrupt_on_wakeup, agent, "voice_interrupt_on_wakeup");
        set_json_bool(&config->voice_streaming_tts_enabled, agent, "voice_streaming_tts_enabled");
        set_json_bool(&config->voice_tool_call_speech, agent, "voice_tool_call_speech");
        set_json_bool(&config->voice_progress_speech_enabled, agent, "voice_progress_speech_enabled");
        set_json_bool(&config->voice_speech_summary_enabled, agent, "voice_speech_summary_enabled");
        set_json_int(&config->voice_speech_max_runes, agent, "voice_speech_max_runes");
        set_json_int(&config->voice_max_response_tokens, agent, "voice_max_response_tokens");
        set_json_int(&config->max_iterations, agent, "max_iterations");
        set_json_bool(&config->force_simple_loop, agent, "force_simple_loop");
        set_json_int(&config->screenshot_keep_n, agent, "screenshot_keep_n");
        set_json_int(&config->screenshot_prune_interval, agent, "screenshot_prune_interval");
        set_json_int(&config->screen_stable_timeout_ms, agent, "screen_stable_timeout_ms");
        set_json_int(&config->screen_stable_ms, agent, "screen_stable_ms");
        set_json_double(&config->screen_stable_diff_threshold, agent, "screen_stable_diff_threshold");
    }
}

ValidationResult validate_agent_config_via_cli(const aiden::AgentToml& config, const char* agent_bin_path) {
    // Convert config to JSON
    cJSON* config_json = config_to_json(config, true);
    char* json_str = cJSON_PrintUnformatted(config_json);
    cJSON_Delete(config_json);

    if (!json_str) {
        // Server-side serialization failure: not the user's fault, treat as
        // unavailable so the UI surfaces it as a server problem.
        return ValidationResult::unavailable("failed to serialize config to JSON");
    }

    std::string input(json_str);
    free(json_str);

    // Call agent config-check CLI
    std::string cmd = std::string(agent_bin_path) + " config-check --stdin --format=json";
    CommandResult result = run_command_with_stdin(cmd, input, 2000);

    if (result.timed_out) {
        return ValidationResult::unavailable("config validation timed out");
    }

    cJSON* response = cJSON_Parse(result.output.c_str());
    if (!response) {
        // CLI produced unparseable output. We cannot tell if the config is
        // valid; fail closed but as UNAVAILABLE so the UI distinguishes a
        // broken validator from a rejected user input.
        std::cerr << "[config] agent config-check returned invalid JSON: " << result.output << "\n";
        return ValidationResult::unavailable(
                "config validation failed: validator returned an unexpected response");
    }

    cJSON* valid = cJSON_GetObjectItem(response, "valid");
    if (json_is_bool(valid) && json_is_type(valid, cJSON_True)) {
        cJSON_Delete(response);
        return ValidationResult::ok();
    }

    // Extract error messages
    std::string error_msg;
    cJSON* errors = cJSON_GetObjectItem(response, "errors");
    if (json_is_type(errors, cJSON_Array)) {
        int error_count = cJSON_GetArraySize(errors);
        for (int i = 0; i < error_count; ++i) {
            cJSON* error = cJSON_GetArrayItem(errors, i);
            cJSON* message = cJSON_GetObjectItem(error, "message");
            if (json_is_string(message)) {
                if (!error_msg.empty()) {
                    error_msg += "; ";
                }
                error_msg += message->valuestring;
            }
        }
    }

    bool has_valid_field = json_is_bool(valid);
    cJSON_Delete(response);

    if (!has_valid_field) {
        // Response parsed but did not include the required `valid` field. We
        // cannot trust this validator's verdict; treat as unavailable.
        return ValidationResult::unavailable(
                "config validation failed: validator returned an unexpected response");
    }

    if (error_msg.empty()) {
        // CLI said valid:false with no errors array — distinct from the well
        // formed "rejected with reasons" path. Treat as unavailable rather
        // than fabricate a user-facing reason.
        return ValidationResult::unavailable("config validation failed without details");
    }

    return ValidationResult::invalid(error_msg);
}

ValidationResult validate_agent_config_for_save(const aiden::AgentToml& config) {
    // The agent CLI's `config-check` is the single source of truth for config
    // validation. If the binary is unavailable, fail closed: reject the save
    // rather than persist a config we cannot validate.
    const char* agent_bin = agent_bin_path();
    if (!file_exists(agent_bin)) {
        std::cerr << "[config] agent binary not found at " << agent_bin
                  << ", refusing to save unvalidated config\n";
        return ValidationResult::unavailable(
                "config validation unavailable: agent binary not found");
    }
    return validate_agent_config_via_cli(config, agent_bin);
}

void sync_wifi_legacy_fields_from_networks(aiden::WifiNetworkConfig* wifi) {
    if (!wifi) {
        return;
    }
    if (wifi->networks.empty()) {
        wifi->ssid.clear();
        wifi->psk.clear();
        return;
    }

    size_t best = 0;
    for (size_t i = 1; i < wifi->networks.size(); ++i) {
        if (wifi->networks[i].priority > wifi->networks[best].priority) {
            best = i;
        }
    }
    wifi->ssid = wifi->networks[best].ssid;
    wifi->psk = wifi->networks[best].psk;
}

void update_wifi_from_json(cJSON* root, aiden::WifiNetworkConfig* wifi) {
    if (!root || !wifi) {
        return;
    }
    cJSON* legacy_ssid = cJSON_GetObjectItem(root, "ssid");
    cJSON* legacy_psk = cJSON_GetObjectItem(root, "psk");
    bool has_legacy_ssid = json_is_string(legacy_ssid);
    bool has_legacy_psk = json_is_string(legacy_psk);
    set_json_str(&wifi->ssid, root, "ssid");
    set_json_str(&wifi->psk, root, "psk");
    set_json_str(&wifi->country, root, "country");
    cJSON* networks = cJSON_GetObjectItem(root, "networks");
    if (json_is_array(networks)) {
        wifi->networks.clear();
        int network_count = cJSON_GetArraySize(networks);
        for (int i = 0; i < network_count; ++i) {
            cJSON* child = cJSON_GetArrayItem(networks, i);
            if (!json_is_object(child)) {
                continue;
            }
            aiden::WifiNetwork network;
            set_json_str(&network.ssid, child, "ssid");
            set_json_str(&network.psk, child, "psk");
            set_json_int(&network.priority, child, "priority");
            set_json_bool(&network.scan_ssid, child, "scan_ssid");
            set_json_bool(&network.disabled, child, "disabled");
            if (!network.ssid.empty()) {
                wifi->networks.push_back(network);
            }
        }
        aiden::normalize_wifi_priorities(wifi);
        sync_wifi_legacy_fields_from_networks(wifi);
    } else if ((has_legacy_ssid || has_legacy_psk) && !wifi->ssid.empty()) {
        aiden::WifiNetwork network;
        int existing_index = aiden::find_wifi_network_index(*wifi, wifi->ssid);
        if (existing_index >= 0) {
            network = wifi->networks[static_cast<size_t>(existing_index)];
        }
        network.ssid = wifi->ssid;
        if (has_legacy_psk) {
            network.psk = wifi->psk;
        } else if (existing_index < 0) {
            network.psk.clear();
        }
        aiden::upsert_wifi_network(wifi, network);
        aiden::promote_wifi_network(wifi, wifi->ssid);
    } else if (!wifi->ssid.empty() && wifi->networks.empty()) {
        aiden::WifiNetwork network;
        network.ssid = wifi->ssid;
        network.psk = wifi->psk;
        network.priority = 1;
        aiden::upsert_wifi_network(wifi, network);
    }
    if (wifi->country.empty()) {
        wifi->country = "CN";
    }
}

std::vector<std::string> parse_wifi_scan_output(const std::string& text) {
    std::set<std::string> seen;
    std::vector<std::string> result;
    std::istringstream input(text);
    std::string line;
    while (std::getline(input, line)) {
        std::string trimmed = trim_copy(line);
        std::string name;

        if (starts_with(trimmed, "SSID:")) {
            name = trim_copy(trimmed.substr(5));
        } else {
            size_t pos = trimmed.find("ESSID:\"");
            if (pos != std::string::npos) {
                size_t start = pos + 8;
                size_t end = trimmed.find('"', start);
                if (end != std::string::npos) {
                    name = trimmed.substr(start, end - start);
                }
            }
        }

        if (!name.empty() && seen.insert(name).second) {
            result.push_back(name);
        }
    }
    return result;
}

CommandResult scan_wifi(const Options& options) {
    CommandResult result;
    std::ostringstream log;

    if (command_exists("ifconfig")) {
        CommandResult up = run_shell_command("ifconfig " + shell_quote(options.wifi_interface) + " up 2>&1");
        if (!trim_trailing_newlines(up.output).empty()) {
            log << "$ ifconfig " << options.wifi_interface << " up\n" << up.output << "\n";
        }
    }

    if (command_exists("iw")) {
        result = run_shell_command("iw dev " + shell_quote(options.wifi_interface) + " scan 2>&1");
        log << "$ iw dev " << options.wifi_interface << " scan\n" << result.output;
        if (result.exit_code == 0) {
            result.output = log.str();
            return result;
        }
    }

    if (command_exists("iwlist")) {
        result = run_shell_command("iwlist " + shell_quote(options.wifi_interface) + " scan 2>&1");
        log << "$ iwlist " << options.wifi_interface << " scan\n" << result.output;
        result.output = log.str();
        return result;
    }

    result.exit_code = 127;
    result.output = log.str() + "No supported scan command found (need iw or iwlist).\n";
    return result;
}

bool wait_for_wpa_completed(const Options& options, std::ostringstream& log, int max_seconds) {
    for (int i = 0; i < max_seconds; ++i) {
        sleep(1);
        CommandResult status = run_shell_command(
            "wpa_cli -i " + shell_quote(options.wifi_interface) + " status 2>&1");
        if (status.exit_code == 0 &&
            status.output.find("wpa_state=COMPLETED") != std::string::npos) {
            log << "$ wpa_cli status -> COMPLETED after " << (i + 1) << "s\n";
            return true;
        }
    }
    log << "$ wpa_cli status -> did not reach COMPLETED within "
        << max_seconds << "s\n";
    return false;
}

void restart_wpa_supplicant(const Options& options, std::ostringstream& log) {
    CommandResult killed = run_shell_command("killall wpa_supplicant 2>&1");
    log << "$ killall wpa_supplicant\n" << killed.output;
    sleep(1);
    run_shell_command("rm -f /var/run/wpa_supplicant/" + shell_quote(options.wifi_interface) + " 2>/dev/null");
    CommandResult start = run_shell_command(
        "wpa_supplicant -B -i " + shell_quote(options.wifi_interface) +
        " -c " + shell_quote(options.wifi_config_path) + " 2>&1");
    log << "$ wpa_supplicant -B -i " << options.wifi_interface
        << " -c " << options.wifi_config_path << "\n" << start.output;
}

void schedule_init_script_restart(const char* init_script) {
    if (!init_script || init_script[0] == '\0') {
        return;
    }
    std::string cmd = shell_quote(init_script) + " restart >/dev/null 2>&1 &";
    int rc = system(cmd.c_str());
    (void)rc;
}

void schedule_agent_restart() {
    schedule_init_script_restart(kAgentInitScript);
}

int acquire_ota_update_launch_lock(std::string* error) {
    std::string lock_path = ota_update_lock_path();
    int lock_fd = open(lock_path.c_str(), O_CREAT | O_RDWR, 0600);
    if (lock_fd < 0) {
        if (error) {
            *error = std::string("open ota update launch lock: ") + strerror(errno);
        }
        return -1;
    }
    if (!set_fd_cloexec(lock_fd, error)) {
        close(lock_fd);
        return -1;
    }
    if (flock(lock_fd, LOCK_EX | LOCK_NB) != 0) {
        if (error) {
            if (errno == EWOULDBLOCK || errno == EAGAIN) {
                *error = "ota update already running";
            } else {
                *error = std::string("lock ota update launch: ") + strerror(errno);
            }
        }
        close(lock_fd);
        return -1;
    }
    return lock_fd;
}

bool set_fd_cloexec(int fd, std::string* error) {
    int flags = fcntl(fd, F_GETFD, 0);
    if (flags < 0) {
        if (error) *error = std::string("get fd flags: ") + strerror(errno);
        return false;
    }
    if (fcntl(fd, F_SETFD, flags | FD_CLOEXEC) != 0) {
        if (error) *error = std::string("set fd close-on-exec: ") + strerror(errno);
        return false;
    }
    return true;
}

std::string build_ota_update_command(const std::string& log_path) {
    std::string ota_bin = ota_bin_path();
    std::string env_run_bin = env_run_bin_path();
    std::string cmd =
        "("
        "if [ -x " + shell_quote(env_run_bin) + " ]; then "
        + shell_quote(env_run_bin) + " " + shell_quote(ota_bin) + " update; "
        "else "
        + shell_quote(ota_bin) + " update; "
        "fi; "
        "rc=$?; echo \"[config_web] ota update exited rc=$rc\"; exit $rc"
        ") >> " + shell_quote(log_path) + " 2>&1";
    return cmd;
}

bool launch_ota_update_supervisor(int lock_fd, const std::string& log_path, std::string* error) {
    pid_t launcher_pid = fork();
    if (launcher_pid < 0) {
        if (error) {
            *error = std::string("fork ota update launcher: ") + strerror(errno);
        }
        return false;
    }

    if (launcher_pid == 0) {
        pid_t supervisor_pid = fork();
        if (supervisor_pid < 0) {
            _exit(127);
        }
        if (supervisor_pid > 0) {
            _exit(0);
        }

        setsid();
        set_fd_cloexec(lock_fd, NULL);
        // Keep the launch lock in this supervisor while `ota update` runs,
        // but keep it out of the ota update exec tree.
        std::string cmd = build_ota_update_command(log_path);
        int rc = system(cmd.c_str());
        (void)rc;
        close(lock_fd);
        _exit(0);
    }

    int status = 0;
    while (waitpid(launcher_pid, &status, 0) < 0) {
        if (errno != EINTR) {
            if (error) {
                *error = std::string("wait ota update launcher: ") + strerror(errno);
            }
            return false;
        }
    }

    if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
        if (error) {
            *error = "failed to start ota update supervisor";
        }
        return false;
    }

    return true;
}

bool schedule_ota_update(std::string* error) {
    int lock_fd = acquire_ota_update_launch_lock(error);
    if (lock_fd < 0) {
        return false;
    }
    std::string log_path = ota_update_log_path();
    if (!prepare_ota_update_log_file(log_path, error)) {
        close(lock_fd);
        return false;
    }
    if (!launch_ota_update_supervisor(lock_fd, log_path, error)) {
        close(lock_fd);
        return false;
    }
    close(lock_fd);
    return true;
}

void schedule_poweroff() {
    int rc = system("(PATH=/sbin:/bin:/usr/sbin:/usr/bin; sync; sleep 1; poweroff) >/dev/null 2>&1 &");
    (void)rc;
}

CommandResult apply_wifi_config(const Options& options, bool force_restart = false) {
    CommandResult result;
    result.exit_code = 0;
    std::ostringstream log;

    if (command_exists("ifconfig")) {
        CommandResult up = run_shell_command("ifconfig " + shell_quote(options.wifi_interface) + " up 2>&1");
        log << "$ ifconfig " << options.wifi_interface << " up\n" << up.output;
        if (up.exit_code != 0) {
            result.exit_code = up.exit_code;
        }
    }

    bool supplicant_ready = false;
    bool associated = false;

    if (!force_restart && command_exists("wpa_cli")) {
        CommandResult ping = run_shell_command("wpa_cli -i " + shell_quote(options.wifi_interface) + " ping 2>&1");
        log << "$ wpa_cli -i " << options.wifi_interface << " ping\n" << ping.output;
        supplicant_ready = ping.exit_code == 0 && ping.output.find("PONG") != std::string::npos;
        if (supplicant_ready) {
            CommandResult reconfigure = run_shell_command("wpa_cli -i " + shell_quote(options.wifi_interface) + " reconfigure 2>&1");
            log << "$ wpa_cli -i " << options.wifi_interface << " reconfigure\n" << reconfigure.output;
            if (reconfigure.exit_code == 0 && reconfigure.output.find("FAIL") == std::string::npos) {
                associated = wait_for_wpa_completed(options, log, 8);
            } else {
                supplicant_ready = false;
            }
        }
    }

    if (!associated && command_exists("wpa_supplicant")) {
        restart_wpa_supplicant(options, log);
        supplicant_ready = true;
        associated = wait_for_wpa_completed(options, log, 10);
    }

    bool dhcp_ok = false;
    if (associated) {
        if (command_exists("dhcpcd")) {
            CommandResult dhcp = run_shell_command("dhcpcd -n " + shell_quote(options.wifi_interface) + " 2>&1");
            log << "$ dhcpcd -n " << options.wifi_interface << "\n" << dhcp.output;
            dhcp_ok = dhcp.exit_code == 0;
            if (!dhcp_ok) {
                result.exit_code = dhcp.exit_code;
            }
        } else if (command_exists("dhclient")) {
            CommandResult dhcp = run_shell_command("dhclient " + shell_quote(options.wifi_interface) + " 2>&1");
            log << "$ dhclient " << options.wifi_interface << "\n" << dhcp.output;
            dhcp_ok = dhcp.exit_code == 0;
            if (!dhcp_ok) {
                result.exit_code = dhcp.exit_code;
            }
        } else {
            log << "No supported DHCP client found (need dhcpcd or dhclient).\n";
            if (result.exit_code == 0) {
                result.exit_code = 127;
            }
        }
    } else {
        log << "wpa_supplicant never reached COMPLETED; skipping DHCP.\n";
        if (result.exit_code == 0) {
            result.exit_code = 1;
        }
    }

    if (command_exists("ifconfig")) {
        CommandResult status = run_shell_command("ifconfig " + shell_quote(options.wifi_interface) + " 2>&1");
        log << "$ ifconfig " << options.wifi_interface << "\n" << status.output;
    }

    if (associated && dhcp_ok) {
        result.exit_code = 0;
    }
    result.output = log.str();
    return result;
}

WifiRuntimeStatus query_wifi_status(const Options& options) {
    WifiRuntimeStatus status;

    if (command_exists("wpa_cli")) {
        CommandResult result = run_shell_command("wpa_cli -i " + shell_quote(options.wifi_interface) + " status 2>&1");
        if (result.exit_code == 0) {
            status.detail = trim_trailing_newlines(result.output);
            status.state = find_key_value_line(result.output, "wpa_state");
            status.ssid = find_key_value_line(result.output, "ssid");
            status.ip_address = find_key_value_line(result.output, "ip_address");
            status.connected = status.state == "COMPLETED" && !status.ssid.empty();
            if (!status.state.empty() || !status.ssid.empty()) {
                return status;
            }
        }
    }

    if (command_exists("iw")) {
        CommandResult result = run_shell_command("iw dev " + shell_quote(options.wifi_interface) + " link 2>&1");
        status.detail = trim_trailing_newlines(result.output);
        if (result.output.find("Not connected") != std::string::npos) {
            status.state = "DISCONNECTED";
            return status;
        }

        std::string ssid = extract_iw_ssid(result.output);
        if (!ssid.empty()) {
            status.ssid = ssid;
            status.connected = true;
            status.state = "COMPLETED";
            return status;
        }
    }

    return status;
}

std::string read_file_contents(const char* path, size_t max_size = 64 * 1024) {
    FILE* f = fopen(path, "r");
    if (!f) {
        return "";
    }
    fseek(f, 0, SEEK_END);
    long file_size = ftell(f);
    fseek(f, 0, SEEK_SET);
    if (file_size < 0 || static_cast<size_t>(file_size) > max_size) {
        fclose(f);
        return "";
    }
    std::string contents;
    contents.reserve(static_cast<size_t>(file_size));
    char buf[1024];
    while (size_t n = fread(buf, 1, sizeof(buf), f)) {
        contents.append(buf, n);
    }
    fclose(f);
    return contents;
}

bool mkdir_p(const std::string& dir, std::string* error) {
    if (dir.empty()) {
        return true;
    }
    std::string current;
    size_t index = 0;
    if (dir[0] == '/') {
        current = "/";
        index = 1;
    }
    while (index <= dir.size()) {
        size_t next = dir.find('/', index);
        std::string part = dir.substr(index, next == std::string::npos ? std::string::npos : next - index);
        if (!part.empty()) {
            if (current.empty() || current == "/") {
                current += part;
            } else {
                current += "/" + part;
            }
            if (mkdir(current.c_str(), 0755) != 0 && errno != EEXIST) {
                if (error) *error = "mkdir " + current + ": " + strerror(errno);
                return false;
            }
        }
        if (next == std::string::npos) {
            break;
        }
        index = next + 1;
    }
    return true;
}

std::string parent_dir(const std::string& path) {
    size_t pos = path.find_last_of('/');
    if (pos == std::string::npos) {
        return "";
    }
    if (pos == 0) {
        return "/";
    }
    return path.substr(0, pos);
}

bool prepare_ota_update_log_file(const std::string& path, std::string* error) {
    if (!mkdir_p(parent_dir(path), error)) {
        return false;
    }
    int fd = open(path.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
    if (fd < 0) {
        if (error) *error = "open " + path + ": " + strerror(errno);
        return false;
    }
    if (close(fd) != 0) {
        if (error) *error = "close " + path + ": " + strerror(errno);
        return false;
    }
    return true;
}

bool validate_system_proxy_values(const SystemProxy& proxy, std::string* error) {
    struct Field {
        const char* name;
        const std::string* value;
    };
    const Field fields[] = {
        {"http_proxy", &proxy.http_proxy},
        {"https_proxy", &proxy.https_proxy},
        {"all_proxy", &proxy.all_proxy},
    };
    for (size_t i = 0; i < sizeof(fields) / sizeof(fields[0]); ++i) {
        std::string value = trim_copy(*fields[i].value);
        std::string validation = validate_proxy_url(value);
        if (!validation.empty()) {
            if (error) *error = std::string(fields[i].name) + ": " + validation + ": " + value;
            return false;
        }
    }
    return true;
}

bool validate_system_env_content(const std::string& content, std::string* error) {
    std::vector<aiden::EnvAssignment> assignments;
    SystemProxy proxy;
    if (!aiden::parse_system_env_content(content, &assignments, &proxy, error)) {
        return false;
    }
    return validate_system_proxy_values(proxy, error);
}

bool atomic_write_file(const std::string& path,
                       const std::string& content,
                       mode_t mode,
                       std::string* error) {
    if (!mkdir_p(parent_dir(path), error)) {
        return false;
    }

    std::string tmp = path + ".tmp";
    int fd = open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC, mode);
    if (fd < 0) {
        if (error) *error = "open " + tmp + ": " + strerror(errno);
        return false;
    }
    if (fchmod(fd, mode) != 0) {
        if (error) *error = "chmod " + tmp + ": " + strerror(errno);
        close(fd);
        unlink(tmp.c_str());
        return false;
    }
    if (!write_all(fd, content.data(), content.size())) {
        if (error) *error = "write " + tmp + ": " + strerror(errno);
        close(fd);
        unlink(tmp.c_str());
        return false;
    }
    fsync(fd);
    if (close(fd) != 0) {
        if (error) *error = "close " + tmp + ": " + strerror(errno);
        unlink(tmp.c_str());
        return false;
    }
    if (rename(tmp.c_str(), path.c_str()) != 0) {
        if (error) *error = "rename " + tmp + " -> " + path + ": " + strerror(errno);
        unlink(tmp.c_str());
        return false;
    }
    return true;
}

bool save_system_env_content(const std::string& path,
                             const std::string& content,
                             std::string* error) {
    if (path.empty()) {
        if (error) *error = "system env path is empty";
        return false;
    }
    if (content.size() > kMaxSystemEnvSize) {
        if (error) *error = "system env is too large";
        return false;
    }
    if (!validate_system_env_content(content, error)) {
        return false;
    }
    return atomic_write_file(path, content, 0600, error);
}

std::string read_file_tail(const char* path, size_t max_size) {
    FILE* f = fopen(path, "r");
    if (!f) {
        return "";
    }

    if (fseek(f, 0, SEEK_END) != 0) {
        fclose(f);
        return "";
    }
    long file_size = ftell(f);
    if (file_size <= 0) {
        fclose(f);
        return "";
    }

    long offset = 0;
    if (static_cast<size_t>(file_size) > max_size) {
        offset = file_size - static_cast<long>(max_size);
    }
    if (fseek(f, offset, SEEK_SET) != 0) {
        fclose(f);
        return "";
    }

    std::string contents;
    contents.reserve(static_cast<size_t>(file_size - offset));
    char buf[1024];
    while (size_t n = fread(buf, 1, sizeof(buf), f)) {
        contents.append(buf, n);
    }
    fclose(f);

    if (offset > 0) {
        size_t first_newline = contents.find('\n');
        if (first_newline != std::string::npos) {
            contents.erase(0, first_newline + 1);
        }
    }
    return contents;
}

std::string trim_for_display(const std::string& text, size_t max_size) {
    std::string trimmed = trim_trailing_newlines(text);
    if (trimmed.size() <= max_size) {
        return trimmed;
    }
    size_t start = trimmed.size() - max_size;
    size_t newline = trimmed.find('\n', start);
    if (newline != std::string::npos && newline + 1 < trimmed.size()) {
        start = newline + 1;
    }
    return trimmed.substr(start);
}

AgentLogSnapshot read_log_snapshot(const char* path, size_t read_size, size_t display_size) {
    AgentLogSnapshot snapshot;
    snapshot.path = path ? path : "";

    FILE* f = fopen(snapshot.path.c_str(), "r");
    if (!f) {
        snapshot.error = strerror(errno);
        return snapshot;
    }
    snapshot.exists = true;

    if (fseek(f, 0, SEEK_END) != 0) {
        snapshot.error = std::string("seek failed: ") + strerror(errno);
        fclose(f);
        return snapshot;
    }

    long file_size = ftell(f);
    if (file_size < 0) {
        snapshot.error = std::string("stat failed: ") + strerror(errno);
        fclose(f);
        return snapshot;
    }

    snapshot.size_bytes = file_size;
    long offset = 0;
    if (static_cast<size_t>(file_size) > read_size) {
        offset = file_size - static_cast<long>(read_size);
        snapshot.truncated = true;
    }

    if (fseek(f, offset, SEEK_SET) != 0) {
        snapshot.error = std::string("seek failed: ") + strerror(errno);
        fclose(f);
        return snapshot;
    }

    std::string contents;
    contents.reserve(static_cast<size_t>(file_size - offset));
    char buf[1024];
    while (size_t n = fread(buf, 1, sizeof(buf), f)) {
        contents.append(buf, n);
    }

    if (ferror(f)) {
        snapshot.error = std::string("read failed: ") + strerror(errno);
    }
    fclose(f);

    if (offset > 0) {
        size_t first_newline = contents.find('\n');
        if (first_newline != std::string::npos) {
            contents.erase(0, first_newline + 1);
        }
    }

    if (contents.size() > display_size) {
        snapshot.truncated = true;
    }
    snapshot.log = trim_for_display(contents, display_size);
    return snapshot;
}

AgentLogSnapshot read_agent_log_snapshot() {
    return read_log_snapshot(kAgentLogPath, kAgentLogReadSize, kAgentLogDisplaySize);
}

AgentLogSnapshot read_ota_log_snapshot() {
    std::string update_log_path = ota_update_log_path();
    return read_log_snapshot(update_log_path.c_str(), kOtaLogReadSize, kOtaLogDisplaySize);
}

AgentLogSnapshot read_ota_health_log_snapshot() {
    std::string health_log_path = ota_health_log_path();
    return read_log_snapshot(health_log_path.c_str(), kOtaLogReadSize, kOtaLogDisplaySize);
}

std::string lowercase_copy(const std::string& text) {
    std::string out = text;
    for (size_t i = 0; i < out.size(); ++i) {
        out[i] = static_cast<char>(tolower(static_cast<unsigned char>(out[i])));
    }
    return out;
}

bool parse_decimal(const std::string& text, int min_value, int max_value, int* value) {
    if (!value) {
        return false;
    }
    std::string trimmed = trim_copy(text);
    if (trimmed.empty()) {
        return false;
    }

    errno = 0;
    char* end = NULL;
    long parsed = strtol(trimmed.c_str(), &end, 10);
    if (errno != 0 || !end || *end != '\0' || parsed < min_value || parsed > max_value) {
        return false;
    }

    *value = static_cast<int>(parsed);
    return true;
}

int parse_status_pid(const std::string& line) {
    size_t pos = line.find("pid=");
    if (pos == std::string::npos) {
        return 0;
    }
    pos += 4;
    size_t end = pos;
    while (end < line.size() && isdigit(static_cast<unsigned char>(line[end]))) {
        ++end;
    }
    int pid = 0;
    if (end > pos && parse_decimal(line.substr(pos, end - pos), 1, 999999, &pid)) {
        return pid;
    }
    return 0;
}

std::string parse_status_addr(const std::string& line) {
    size_t pos = line.find("addr=");
    if (pos == std::string::npos) {
        return "";
    }
    pos += 5;
    size_t end = line.find_first_of(" \t\r\n", pos);
    return line.substr(pos, end == std::string::npos ? std::string::npos : end - pos);
}

std::string parse_agent_host(const std::string& addr) {
    std::string candidate = trim_copy(addr);
    if (candidate.empty()) {
        return kAgentPortHost;
    }

    size_t colon = candidate.rfind(':');
    if (colon == std::string::npos || colon == 0) {
        return kAgentPortHost;
    }

    std::string host = candidate.substr(0, colon);
    if (host.size() >= 2 && host[0] == '[' && host[host.size() - 1] == ']') {
        host = host.substr(1, host.size() - 2);
    }
    return host.empty() ? kAgentPortHost : host;
}

int parse_agent_port(const std::string& addr) {
    if (addr.empty()) {
        return kDefaultAgentPort;
    }

    std::string candidate = addr;
    size_t colon = candidate.rfind(':');
    if (colon != std::string::npos) {
        candidate = candidate.substr(colon + 1);
    }

    int port = 0;
    if (parse_decimal(candidate, 1, 65535, &port)) {
        return port;
    }
    return kDefaultAgentPort;
}

TcpPortStatus check_tcp_port(const std::string& host, int port) {
    TcpPortStatus status;
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) {
        status.detail = std::string("socket failed: ") + strerror(errno);
        return status;
    }

    int flags = fcntl(fd, F_GETFL, 0);
    if (flags >= 0) {
        fcntl(fd, F_SETFL, flags | O_NONBLOCK);
    }

    sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    if (inet_pton(AF_INET, host.c_str(), &addr.sin_addr) != 1) {
        status.detail = "invalid host: " + host;
        close(fd);
        return status;
    }

    int rc = connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr));
    if (rc == 0) {
        status.reachable = true;
        status.detail = "connected";
        close(fd);
        return status;
    }

    if (errno != EINPROGRESS) {
        status.detail = strerror(errno);
        close(fd);
        return status;
    }

    fd_set write_fds;
    FD_ZERO(&write_fds);
    FD_SET(fd, &write_fds);
    struct timeval timeout;
    timeout.tv_sec = 0;
    timeout.tv_usec = 700 * 1000;

    rc = select(fd + 1, NULL, &write_fds, NULL, &timeout);
    if (rc > 0 && FD_ISSET(fd, &write_fds)) {
        int socket_error = 0;
        socklen_t len = sizeof(socket_error);
        if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &socket_error, &len) == 0 && socket_error == 0) {
            status.reachable = true;
            status.detail = "connected";
        } else {
            status.detail = strerror(socket_error == 0 ? errno : socket_error);
        }
    } else if (rc == 0) {
        status.detail = "connect timed out";
    } else {
        status.detail = std::string("select failed: ") + strerror(errno);
    }

    close(fd);
    return status;
}

std::string recent_agent_startup_log() {
    std::string log = read_file_tail(kAgentLogPath, kAgentStatusLogReadSize);
    if (log.empty()) {
        return "";
    }

    size_t marker = log.rfind("[agent] starting ");
    if (marker == std::string::npos) {
        marker = log.rfind("[agent] waiting for ");
    }
    if (marker != std::string::npos) {
        log = log.substr(marker);
    }
    return trim_for_display(log, kAgentStatusLogDisplaySize);
}

bool log_excerpt_looks_like_startup_failure(const std::string& log) {
    std::string lower = lowercase_copy(log);
    return lower.find("exited with status") != std::string::npos ||
           lower.find("error") != std::string::npos ||
           lower.find("failed") != std::string::npos ||
           lower.find("not found") != std::string::npos ||
           lower.find("panic") != std::string::npos;
}

AgentRuntimeStatus query_agent_status() {
    AgentRuntimeStatus status;
    status.addr = ":8080";
    status.port_host = kAgentPortHost;
    status.port = kDefaultAgentPort;

    if (!file_exists(kAgentInitScript)) {
        status.state = "unknown";
        status.detail = std::string("agent init script not found: ") + kAgentInitScript;
        status.startup_error = status.detail;
    } else {
        CommandResult script_status = run_shell_command_with_timeout(
            std::string(kAgentInitScript) + " status 2>&1", kAgentStatusCommandTimeoutMs);
        status.detail = trim_trailing_newlines(script_status.output);

        if (script_status.timed_out) {
            status.state = "unknown";
            status.startup_error = status.detail;
        } else {
            std::istringstream lines(script_status.output);
            std::string line;
            while (std::getline(lines, line)) {
                line = trim_copy(line);
                if (starts_with(line, "watchdog=running")) {
                    status.watchdog_running = true;
                    status.watchdog_pid = parse_status_pid(line);
                } else if (starts_with(line, "agent=running")) {
                    status.process_running = true;
                    status.pid = parse_status_pid(line);
                    std::string addr = parse_status_addr(line);
                    if (!addr.empty()) {
                        status.addr = addr;
                        status.port_host = parse_agent_host(addr);
                        status.port = parse_agent_port(addr);
                    }
                } else if (starts_with(line, "agent=stopped")) {
                    status.process_running = false;
                }
            }

            if (script_status.exit_code != 0 && status.detail.empty()) {
                status.detail = "agent status command failed";
            }
        }
    }

    TcpPortStatus port_status = check_tcp_port(status.port_host, status.port);
    status.port_reachable = port_status.reachable;
    status.port_detail = port_status.detail;

    if (status.process_running) {
        status.state = status.port_reachable ? "running" : "port_unreachable";
    } else if (status.watchdog_running) {
        std::string log = recent_agent_startup_log();
        if (!log.empty()) {
            status.startup_error = log;
        }
        status.state = log_excerpt_looks_like_startup_failure(log) ? "error" : "starting";
    } else if (status.state == "unknown") {
        // Keep the unknown state set above.
    } else {
        status.state = "stopped";
    }

    return status;
}

cJSON* agent_status_to_json(const AgentRuntimeStatus& status) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "state", status.state.c_str());
    cJSON_AddBoolToObject(root, "watchdog_running", status.watchdog_running ? 1 : 0);
    cJSON_AddNumberToObject(root, "watchdog_pid", status.watchdog_pid);
    cJSON_AddBoolToObject(root, "process_running", status.process_running ? 1 : 0);
    cJSON_AddNumberToObject(root, "pid", status.pid);
    cJSON_AddStringToObject(root, "addr", status.addr.c_str());
    cJSON_AddStringToObject(root, "port_host", status.port_host.c_str());
    cJSON_AddNumberToObject(root, "port", status.port);
    cJSON_AddBoolToObject(root, "port_reachable", status.port_reachable ? 1 : 0);
    cJSON_AddStringToObject(root, "port_detail", status.port_detail.c_str());
    cJSON_AddStringToObject(root, "detail", status.detail.c_str());
    cJSON_AddStringToObject(root, "startup_error", status.startup_error.c_str());
    return root;
}

cJSON* log_snapshot_to_json(const AgentLogSnapshot& snapshot) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "path", snapshot.path.c_str());
    cJSON_AddBoolToObject(root, "exists", snapshot.exists ? 1 : 0);
    cJSON_AddNumberToObject(root, "size_bytes", snapshot.size_bytes);
    cJSON_AddBoolToObject(root, "truncated", snapshot.truncated ? 1 : 0);
    cJSON_AddStringToObject(root, "log", snapshot.log.c_str());
    cJSON_AddStringToObject(root, "error", snapshot.error.c_str());
    return root;
}

cJSON* agent_log_to_json(const AgentLogSnapshot& snapshot) {
    return log_snapshot_to_json(snapshot);
}

cJSON* ota_log_to_json(const AgentLogSnapshot& snapshot) {
    return log_snapshot_to_json(snapshot);
}

std::string json_string_value(cJSON* obj, const char* key) {
    cJSON* item = cJSON_GetObjectItem(obj, key);
    return json_is_string(item) ? item->valuestring : "";
}

std::string normalize_slot_name(const std::string& raw) {
    std::string value = lowercase_copy(trim_copy(raw));
    if (value == "a" || value == "_a" || value == "slot_a" || value == "0") {
        return "a";
    }
    if (value == "b" || value == "_b" || value == "slot_b" || value == "1") {
        return "b";
    }
    return "";
}

std::string slot_name_from_json(cJSON* item) {
    if (json_is_number(item)) {
        if (item->valueint == 0) {
            return "a";
        }
        if (item->valueint == 1) {
            return "b";
        }
        return "";
    }
    if (json_is_string(item)) {
        return normalize_slot_name(item->valuestring);
    }
    return "";
}

bool string_has_suffix(const std::string& text, const std::string& suffix) {
    return text.size() >= suffix.size()
        && text.compare(text.size() - suffix.size(), suffix.size(), suffix) == 0;
}

std::string slot_from_root_value(const std::string& root) {
    std::string value = lowercase_copy(trim_copy(root));
    if (value == "partlabel=rootfs_a" || value == "rootfs_a"
        || value == "/dev/mmcblk0p9" || string_has_suffix(value, "/rootfs_a")) {
        return "a";
    }
    if (value == "partlabel=rootfs_b" || value == "rootfs_b"
        || value == "/dev/mmcblk0p10" || string_has_suffix(value, "/rootfs_b")) {
        return "b";
    }
    return "";
}

std::string current_slot_from_cmdline(const std::string& cmdline) {
    std::istringstream in(cmdline);
    std::string field;
    std::string root_slot;
    while (in >> field) {
        const std::string slot_prefix = "aiden.slot_suffix=";
        if (field.compare(0, slot_prefix.size(), slot_prefix) == 0) {
            std::string slot = normalize_slot_name(field.substr(slot_prefix.size()));
            if (!slot.empty()) {
                return slot;
            }
        }
        const std::string root_prefix = "root=";
        if (field.compare(0, root_prefix.size(), root_prefix) == 0) {
            root_slot = slot_from_root_value(field.substr(root_prefix.size()));
        }
    }
    return root_slot;
}

bool running_slot_matches_target(const std::string& running_slot,
                                 const std::string& target_slot) {
    return !running_slot.empty() && !target_slot.empty() && running_slot == target_slot;
}

std::string firmware_health_status(const std::string& phase,
                                   const std::string& last_error,
                                   bool showing_running_target) {
    if (phase == "health" && !last_error.empty()) {
        return "failed";
    }
    if (phase == "pending-reboot" && showing_running_target) {
        return "pending";
    }
    if (phase == "committed") {
        return "success";
    }
    if (phase == "rolled-back") {
        return "rolled_back";
    }
    return "";
}

cJSON* firmware_info_to_json(const Options& options) {
    cJSON* fw = cJSON_CreateObject();
    if (!file_exists(options.ota_state_path.c_str())) {
        cJSON_AddStringToObject(fw, "version", "");
        cJSON_AddStringToObject(fw, "build_time", "");
        cJSON_AddStringToObject(fw, "phase", "");
        cJSON_AddStringToObject(fw, "health_status", "");
        cJSON_AddStringToObject(fw, "health_error", "");
        cJSON_AddStringToObject(fw, "previous_version", "");
        cJSON_AddStringToObject(fw, "previous_build_time", "");
        cJSON_AddStringToObject(fw, "running_slot", "");
        cJSON_AddStringToObject(fw, "target_slot", "");
        return fw;
    }
    std::string contents = read_file_contents(options.ota_state_path.c_str());
    cJSON* state = cJSON_Parse(contents.c_str());
    if (!state) {
        cJSON_AddStringToObject(fw, "version", "");
        cJSON_AddStringToObject(fw, "build_time", "");
        cJSON_AddStringToObject(fw, "phase", "");
        cJSON_AddStringToObject(fw, "health_status", "");
        cJSON_AddStringToObject(fw, "health_error", "");
        cJSON_AddStringToObject(fw, "previous_version", "");
        cJSON_AddStringToObject(fw, "previous_build_time", "");
        cJSON_AddStringToObject(fw, "running_slot", "");
        cJSON_AddStringToObject(fw, "target_slot", "");
        return fw;
    }

    const std::string current_version = json_string_value(state, "current_version");
    const std::string current_build_time = json_string_value(state, "current_build_time");
    const std::string target_version = json_string_value(state, "target_version");
    const std::string target_build_time = json_string_value(state, "target_build_time");
    const std::string phase = json_string_value(state, "phase");
    const std::string last_error = json_string_value(state, "last_error");
    const std::string target_slot = slot_name_from_json(cJSON_GetObjectItem(state, "target_slot"));
    const std::string running_slot = current_slot_from_cmdline(
        read_file_contents(options.cmdline_path.c_str(), 16 * 1024));
    const bool show_target =
        !target_version.empty()
        && (phase == "pending-reboot" || phase == "health")
        && running_slot_matches_target(running_slot, target_slot);

    cJSON_AddStringToObject(fw, "version", show_target ? target_version.c_str() : current_version.c_str());
    cJSON_AddStringToObject(fw, "build_time", show_target ? target_build_time.c_str() : current_build_time.c_str());
    cJSON_AddStringToObject(fw, "phase", phase.c_str());
    cJSON_AddStringToObject(fw, "current_version", current_version.c_str());
    cJSON_AddStringToObject(fw, "current_build_time", current_build_time.c_str());
    cJSON_AddStringToObject(fw, "target_version", target_version.c_str());
    cJSON_AddStringToObject(fw, "target_build_time", target_build_time.c_str());
    cJSON_AddStringToObject(fw, "previous_version", show_target ? current_version.c_str() : "");
    cJSON_AddStringToObject(fw, "previous_build_time", show_target ? current_build_time.c_str() : "");
    cJSON_AddStringToObject(fw, "running_slot", running_slot.c_str());
    cJSON_AddStringToObject(fw, "target_slot", target_slot.c_str());
    std::string health_status = firmware_health_status(phase, last_error, show_target);
    cJSON_AddStringToObject(fw, "health_status", health_status.c_str());
    cJSON_AddStringToObject(fw, "health_error",
                            health_status == "failed" ? last_error.c_str() : "");
    cJSON_Delete(state);
    return fw;
}

ApiResponse handle_get_config(const Options& options) {
    aiden::AgentToml config;
    aiden::WifiNetworkConfig wifi;
    WifiRuntimeStatus wifi_status = query_wifi_status(options);
    AgentRuntimeStatus agent_status = query_agent_status();
    std::string config_error;
    std::string wifi_error;
    load_current_agent_config(options, &config, &config_error);
    load_current_wifi_config(options, &wifi, &wifi_error);

    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddItemToObject(root, "config", config_to_json(config));
    cJSON_AddItemToObject(root, "wifi", wifi_to_json(wifi));
    cJSON_AddItemToObject(root, "wifi_status", wifi_status_to_json(wifi_status));
    cJSON_AddItemToObject(root, "agent_status", agent_status_to_json(agent_status));
    cJSON_AddItemToObject(root, "firmware", firmware_info_to_json(options));
    cJSON_AddStringToObject(root, "system_env",
                            read_file_contents(options.system_env_path.c_str(), kMaxSystemEnvSize).c_str());

    cJSON* paths = add_object(root, "paths");
    cJSON_AddStringToObject(paths, "agent_config", options.agent_config_path.c_str());
    cJSON_AddStringToObject(paths, "wifi_config", options.wifi_config_path.c_str());
    cJSON_AddStringToObject(paths, "wifi_interface", options.wifi_interface.c_str());
    cJSON_AddStringToObject(paths, "system_env", options.system_env_path.c_str());

    if (!config_error.empty()) {
        cJSON_AddStringToObject(root, "config_error", config_error.c_str());
    }
    if (!wifi_error.empty()) {
        cJSON_AddStringToObject(root, "wifi_error", wifi_error.c_str());
    }

    return make_json_ok(root);
}

// handle_get_config_meta serves the config field metadata produced by the agent
// CLI's `config-meta` subcommand. The agent binary is the single source of
// truth for field widgets, enums, ranges, defaults, secret flags and
// visibility rules; the UI consumes this to render the config form. Fails
// closed (503) when the binary is missing or returns unparseable output rather
// than letting the UI fall back to stale hard-coded metadata.
ApiResponse handle_get_config_meta() {
    const char* agent_bin = agent_bin_path();
    if (!file_exists(agent_bin)) {
        std::cerr << "[config] agent binary not found at " << agent_bin
                  << ", cannot serve config metadata\n";
        return make_json_error(503, "config metadata unavailable: agent binary not found");
    }

    std::string cmd = std::string(agent_bin) + " config-meta --format=json";
    CommandResult result = run_command_with_stdin(cmd, "", 2000);

    if (result.timed_out) {
        return make_json_error(503, "config metadata timed out");
    }
    if (result.exit_code != 0) {
        std::cerr << "[config] agent config-meta exited " << result.exit_code
                  << ": " << result.output << "\n";
        return make_json_error(503, "config metadata generation failed");
    }

    // Validate the payload is well-formed JSON before forwarding it verbatim.
    cJSON* parsed = cJSON_Parse(result.output.c_str());
    if (!parsed) {
        std::cerr << "[config] agent config-meta returned invalid JSON: " << result.output << "\n";
        return make_json_error(503, "config metadata returned an unexpected response");
    }
    cJSON_Delete(parsed);

    ApiResponse response;
    response.body = result.output;
    return response;
}

ApiResponse handle_get_agent_status() {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddItemToObject(root, "agent_status", agent_status_to_json(query_agent_status()));
    return make_json_ok(root);
}

ApiResponse handle_get_agent_log() {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddItemToObject(root, "agent_log", agent_log_to_json(read_agent_log_snapshot()));
    return make_json_ok(root);
}

ApiResponse handle_get_ota_log() {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddItemToObject(root, "ota_log", ota_log_to_json(read_ota_log_snapshot()));
    cJSON_AddItemToObject(root, "ota_health_log", ota_log_to_json(read_ota_health_log_snapshot()));
    return make_json_ok(root);
}

ApiResponse handle_post_ota_update() {
    std::string error;
    if (!schedule_ota_update(&error)) {
        return make_json_error(500, error.empty() ? "failed to start ota update" : error);
    }

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddBoolToObject(response, "ota_update_started", 1);
    cJSON_AddStringToObject(response, "message", "ota update started");
    cJSON_AddNumberToObject(response, "ota_log_start_size_bytes", 0);
    return make_json_ok(response);
}

ApiResponse handle_post_system_env(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    cJSON* env_item = cJSON_GetObjectItem(root, "system_env");
    if (!json_is_string(env_item)) {
        cJSON_Delete(root);
        return make_json_error(400, "missing system_env string");
    }

    std::string system_env = env_item->valuestring;
    cJSON_Delete(root);

    std::string save_error;
    if (!save_system_env_content(options.system_env_path, system_env, &save_error)) {
        return make_json_error(400, save_error);
    }

    schedule_agent_restart();

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "message", "system env saved; agent restarting");
    cJSON_AddBoolToObject(response, "agent_restart_scheduled", 1);
    cJSON_AddBoolToObject(response, "ota_restart_scheduled", 0);
    cJSON_AddStringToObject(response, "system_env",
                            read_file_contents(options.system_env_path.c_str(), kMaxSystemEnvSize).c_str());
    cJSON* paths = add_object(response, "paths");
    cJSON_AddStringToObject(paths, "system_env", options.system_env_path.c_str());
    return make_json_ok(response);
}

ApiResponse handle_post_config(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    aiden::AgentToml config;
    aiden::WifiNetworkConfig wifi;
    std::string ignore_error;
    load_current_agent_config(options, &config, &ignore_error);
    load_current_wifi_config(options, &wifi, &ignore_error);
    std::string original_pointer_mode = normalize_pointer_mode(config.hid.pointer_mode);

    cJSON* config_json = cJSON_GetObjectItem(root, "config");
    if (json_is_object(config_json)) {
        update_config_from_json(config_json, &config);
        preserve_redacted_agent_secrets(options, &config);
    }

    cJSON* wifi_json = cJSON_GetObjectItem(root, "wifi");
    if (json_is_object(wifi_json)) {
        update_wifi_from_json(wifi_json, &wifi);
    }

    bool apply_wifi = false;
    cJSON* apply_wifi_json = cJSON_GetObjectItem(root, "apply_wifi");
    if (json_is_bool(apply_wifi_json)) {
        apply_wifi = json_is_type(apply_wifi_json, cJSON_True);
    }

    ValidationResult validation = validate_agent_config_for_save(config);
    if (validation.outcome != ValidationOutcome::OK) {
        std::cerr << "Refusing to save agent config ("
                  << (validation.outcome == ValidationOutcome::UNAVAILABLE ? "unavailable" : "invalid")
                  << "): " << validation.message << "\n";
        cJSON_Delete(root);
        // 503 when the validator itself is missing/broken: a server-side
        // dependency outage, not a user input error. 400 only when the
        // validator ran and rejected the user's config.
        int status = validation.outcome == ValidationOutcome::UNAVAILABLE ? 503 : 400;
        return make_json_error(status, validation.message);
    }

    cJSON_Delete(root);
    config.hid.pointer_mode = normalize_pointer_mode(config.hid.pointer_mode);
    bool usbhid_restart_scheduled = original_pointer_mode != config.hid.pointer_mode;

    std::string save_error;
    if (!aiden::save_agent_toml(options.agent_config_path.c_str(), config, &save_error)) {
        return make_json_error(500, save_error);
    }

    bool should_save_wifi = !wifi.ssid.empty() || !wifi.networks.empty() || file_exists(options.wifi_config_path.c_str());
    if (should_save_wifi && !aiden::save_wifi_config(options.wifi_config_path.c_str(), wifi, &save_error)) {
        return make_json_error(500, save_error);
    }

    bool agent_restart_scheduled = !usbhid_restart_scheduled;
    if (agent_restart_scheduled) {
        schedule_agent_restart();
    }

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "message",
                            usbhid_restart_scheduled
                                ? "config saved; reboot required for usb hid pointer_mode"
                                : "config saved; agent restarting");
    cJSON_AddBoolToObject(response, "agent_restart_scheduled", agent_restart_scheduled ? 1 : 0);
    cJSON_AddBoolToObject(response, "usbhid_restart_scheduled", 0);
    cJSON_AddBoolToObject(response, "usbhid_restart_required", usbhid_restart_scheduled ? 1 : 0);
    cJSON_AddBoolToObject(response, "reboot_required", usbhid_restart_scheduled ? 1 : 0);
    cJSON_AddBoolToObject(response, "ota_restart_scheduled", 0);
    cJSON_AddItemToObject(response, "config", config_to_json(config));

    cJSON* paths = add_object(response, "paths");
    cJSON_AddStringToObject(paths, "agent_config", options.agent_config_path.c_str());
    cJSON_AddStringToObject(paths, "wifi_config", options.wifi_config_path.c_str());
    cJSON_AddStringToObject(paths, "system_env", options.system_env_path.c_str());

    if (apply_wifi && should_save_wifi) {
        CommandResult wifi_apply = apply_wifi_config(options);
        cJSON* apply = add_object(response, "wifi_apply");
        cJSON_AddBoolToObject(apply, "ok", wifi_apply.exit_code == 0);
        cJSON_AddNumberToObject(apply, "exit_code", wifi_apply.exit_code);
        cJSON_AddStringToObject(apply, "output", trim_trailing_newlines(wifi_apply.output).c_str());
        if (wifi_apply.exit_code != 0) {
            cJSON_AddStringToObject(apply, "error", "failed to apply wifi config");
        }
        cJSON_AddItemToObject(response, "wifi_status", wifi_status_to_json(query_wifi_status(options)));
    }

    return make_json_ok(response);
}

ApiResponse handle_post_poweroff() {
    schedule_poweroff();

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "message", "poweroff scheduled");
    cJSON_AddBoolToObject(response, "poweroff_scheduled", 1);
    return make_json_ok(response);
}

ApiResponse handle_wifi_connect(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    cJSON* ssid_item = cJSON_GetObjectItem(root, "ssid");
    if (!json_is_string(ssid_item) || trim_copy(ssid_item->valuestring).empty()) {
        cJSON_Delete(root);
        return make_json_error(400, "ssid is required");
    }

    const std::string target_ssid = trim_copy(ssid_item->valuestring);
    cJSON* psk_item = cJSON_GetObjectItem(root, "psk");
    const bool has_psk = json_is_string(psk_item);
    std::string requested_psk = has_psk ? std::string(psk_item->valuestring) : std::string();
    cJSON* country_item = cJSON_GetObjectItem(root, "country");
    std::string requested_country = json_is_string(country_item) ? trim_copy(country_item->valuestring) : std::string();
    cJSON_Delete(root);

    aiden::WifiNetworkConfig original;
    std::string ignore_error;
    const bool had_original_config = file_exists(options.wifi_config_path.c_str());
    const std::string original_wifi_config_text =
        had_original_config ? read_file_contents(options.wifi_config_path.c_str()) : std::string();
    load_current_wifi_config(options, &original, &ignore_error);

    aiden::WifiNetworkConfig attempt = original;
    if (!requested_country.empty()) {
        attempt.country = requested_country;
    }
    if (attempt.country.empty()) {
        attempt.country = "CN";
    }

    aiden::WifiNetwork network;
    int existing_index = aiden::find_wifi_network_index(attempt, target_ssid);
    if (existing_index >= 0) {
        network = attempt.networks[static_cast<size_t>(existing_index)];
    }
    network.ssid = target_ssid;
    if (has_psk || existing_index < 0) {
        network.psk = requested_psk;
    }

    aiden::upsert_wifi_network(&attempt, network);
    aiden::promote_wifi_network(&attempt, target_ssid);

    std::string save_error;
    Options attempt_options = options;
    attempt_options.wifi_config_path = options.wifi_config_path + ".candidate";
    if (!aiden::save_wifi_config(attempt_options.wifi_config_path.c_str(), attempt, &save_error)) {
        return make_json_error(500, save_error);
    }

    CommandResult wifi_apply = apply_wifi_config(attempt_options, true);
    WifiRuntimeStatus wifi_status = query_wifi_status(options);
    auto is_connected_to_target = [&](const WifiRuntimeStatus& status) {
        return status.connected &&
               status.state == "COMPLETED" &&
               status.ssid == target_ssid &&
               !status.ip_address.empty();
    };
    bool connected_to_target =
        wifi_apply.exit_code == 0 &&
        is_connected_to_target(wifi_status);

    aiden::WifiNetworkConfig response_wifi = attempt;
    if (connected_to_target) {
        aiden::promote_wifi_network(&attempt, target_ssid);
        if (!aiden::save_wifi_config(options.wifi_config_path.c_str(), attempt, &save_error)) {
            unlink(attempt_options.wifi_config_path.c_str());
            return make_json_error(500, save_error);
        }
        CommandResult final_apply = apply_wifi_config(options, true);
        if (!trim_trailing_newlines(final_apply.output).empty()) {
            wifi_apply.output = trim_trailing_newlines(wifi_apply.output) +
                "\n[config_web] final apply output:\n" +
                trim_trailing_newlines(final_apply.output);
        }
        wifi_status = query_wifi_status(options);
        if (final_apply.exit_code != 0 || !is_connected_to_target(wifi_status)) {
            connected_to_target = false;
            if (wifi_apply.exit_code == 0) {
                wifi_apply.exit_code = final_apply.exit_code == 0 ? 1 : final_apply.exit_code;
            }

            if (had_original_config) {
                if (!atomic_write_file(options.wifi_config_path,
                                       original_wifi_config_text,
                                       0600,
                                       &save_error)) {
                    unlink(attempt_options.wifi_config_path.c_str());
                    return make_json_error(500, "failed to restore previous wifi config: " + save_error);
                }
            } else if (unlink(options.wifi_config_path.c_str()) != 0 && errno != ENOENT) {
                unlink(attempt_options.wifi_config_path.c_str());
                return make_json_error(500,
                                       std::string("failed to remove wifi config during restore: ") +
                                           strerror(errno));
            }

            CommandResult restore_apply;
            if (had_original_config && (!original.networks.empty() || !original.ssid.empty())) {
                restore_apply = apply_wifi_config(options, true);
            } else {
                restore_apply = run_shell_command("wpa_cli -i " + shell_quote(options.wifi_interface) +
                                                  " disconnect 2>&1");
            }
            std::ostringstream restored_output;
            restored_output << trim_trailing_newlines(wifi_apply.output);
            restored_output << "\n[config_web] saved Wi-Fi was not confirmed; restored previous Wi-Fi config.";
            if (!trim_trailing_newlines(restore_apply.output).empty()) {
                restored_output << "\n[config_web] restore apply output:\n"
                                << trim_trailing_newlines(restore_apply.output);
            }
            wifi_apply.output = restored_output.str();
            response_wifi = original;
            wifi_status = query_wifi_status(options);
        } else {
            response_wifi = attempt;
        }
    } else {
        CommandResult restore_apply;
        if (had_original_config && (!original.networks.empty() || !original.ssid.empty())) {
            restore_apply = apply_wifi_config(options, true);
        } else {
            restore_apply = run_shell_command("wpa_cli -i " + shell_quote(options.wifi_interface) + " disconnect 2>&1");
        }
        std::ostringstream restored_output;
        restored_output << trim_trailing_newlines(wifi_apply.output);
        restored_output << "\n[config_web] target Wi-Fi was not confirmed; restored previous Wi-Fi config.";
        if (!trim_trailing_newlines(restore_apply.output).empty()) {
            restored_output << "\n[config_web] restore apply output:\n"
                            << trim_trailing_newlines(restore_apply.output);
        }
        wifi_apply.output = restored_output.str();
        if (wifi_apply.exit_code == 0) {
            wifi_apply.exit_code = 1;
        }
        response_wifi = original;
        wifi_status = query_wifi_status(options);
    }
    unlink(attempt_options.wifi_config_path.c_str());

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", connected_to_target ? 1 : 0);
    cJSON_AddItemToObject(response, "wifi", wifi_to_json(response_wifi));
    cJSON_AddItemToObject(response, "wifi_status", wifi_status_to_json(wifi_status));
    cJSON_AddStringToObject(response,
                            "message",
                            connected_to_target ? "wifi connected and saved" : "wifi connect failed; config restored");

    cJSON* apply = add_object(response, "wifi_apply");
    cJSON_AddBoolToObject(apply, "ok", connected_to_target ? 1 : 0);
    cJSON_AddNumberToObject(apply, "exit_code", wifi_apply.exit_code);
    cJSON_AddStringToObject(apply, "output", trim_trailing_newlines(wifi_apply.output).c_str());
    if (!connected_to_target) {
        cJSON_AddStringToObject(apply, "error", "failed to apply wifi config");
    }

    return make_json_ok(response);
}

ApiResponse handle_wifi_forget(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    cJSON* ssid_item = cJSON_GetObjectItem(root, "ssid");
    if (!json_is_string(ssid_item) || trim_copy(ssid_item->valuestring).empty()) {
        cJSON_Delete(root);
        return make_json_error(400, "ssid is required");
    }
    const std::string ssid = trim_copy(ssid_item->valuestring);
    cJSON_Delete(root);

    aiden::WifiNetworkConfig wifi;
    std::string load_error;
    load_current_wifi_config(options, &wifi, &load_error);
    if (!load_error.empty()) {
        return make_json_error(500, load_error);
    }
    if (!aiden::remove_wifi_network(&wifi, ssid)) {
        return make_json_error(404, "wifi network not found");
    }

    std::string save_error;
    if (!aiden::save_wifi_config(options.wifi_config_path.c_str(), wifi, &save_error)) {
        return make_json_error(500, save_error);
    }

    CommandResult wifi_apply;
    wifi_apply.exit_code = 0;
    if (!wifi.networks.empty()) {
        wifi_apply = apply_wifi_config(options);
    } else {
        wifi_apply = run_shell_command("wpa_cli -i " + shell_quote(options.wifi_interface) + " disconnect 2>&1");
    }

    cJSON* response = cJSON_CreateObject();
    const bool applied = wifi_apply.exit_code == 0;
    cJSON_AddBoolToObject(response, "ok", applied ? 1 : 0);
    cJSON_AddItemToObject(response, "wifi", wifi_to_json(wifi));
    cJSON_AddItemToObject(response, "wifi_status", wifi_status_to_json(query_wifi_status(options)));
    cJSON_AddStringToObject(response,
                            "message",
                            applied ? "wifi network forgotten"
                                    : "wifi network forgotten but failed to apply runtime changes");
    cJSON* apply = add_object(response, "wifi_apply");
    cJSON_AddBoolToObject(apply, "ok", applied ? 1 : 0);
    cJSON_AddNumberToObject(apply, "exit_code", wifi_apply.exit_code);
    cJSON_AddStringToObject(apply, "output", trim_trailing_newlines(wifi_apply.output).c_str());
    if (!applied) {
        cJSON_AddStringToObject(apply, "error", "failed to apply wifi config");
    }
    return make_json_ok(response);
}

ApiResponse handle_wifi_scan(const Options& options) {
    CommandResult scan = scan_wifi(options);
    std::vector<std::string> networks = parse_wifi_scan_output(scan.output);

    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", scan.exit_code == 0);
    cJSON_AddNumberToObject(root, "exit_code", scan.exit_code);
    cJSON_AddStringToObject(root, "output", trim_trailing_newlines(scan.output).c_str());
    cJSON* array = add_array(root, "networks");
    for (size_t i = 0; i < networks.size(); ++i) {
        cJSON_AddItemToArray(array, cJSON_CreateString(networks[i].c_str()));
    }
    return make_json_ok(root);
}

std::string validate_proxy_url(const std::string& url) {
    return aiden::validate_system_proxy_url(url);
}

static bool is_tencent_asr_provider(const std::string& provider) {
    return provider == "tencent" || provider == "tencent_asr";
}

std::string provider_default_url(const std::string& provider) {
    if (provider == "openrouter") return "https://openrouter.ai/api/v1";
    if (provider == "openai" || provider == "openai-whisper") return "https://api.openai.com/v1";
    if (provider == "minimax" || provider == "minimax-ws") return "https://api.minimax.chat";
    if (is_tencent_asr_provider(provider)) return "https://asr.tencentcloudapi.com";
    return "";
}

std::string build_curl_proxy_arg(const SystemProxy& proxy) {
    std::string url = trim_copy(proxy.https_proxy);
    if (url.empty()) url = trim_copy(proxy.http_proxy);
    if (url.empty()) url = trim_copy(proxy.all_proxy);
    if (url.empty()) return "";
    std::string arg = " -x " + shell_quote(url) + " ";
    std::string no_proxy = trim_copy(proxy.no_proxy);
    if (no_proxy.empty()) {
        no_proxy = kDefaultNoProxy;
    }
    arg += "--noproxy " + shell_quote(no_proxy) + " ";
    return arg;
}

std::string build_proxy_env_exports(const SystemProxy& proxy) {
    std::string http_proxy = trim_copy(proxy.http_proxy);
    std::string https_proxy = trim_copy(proxy.https_proxy);
    std::string all_proxy = trim_copy(proxy.all_proxy);
    std::string no_proxy = trim_copy(proxy.no_proxy);
    if (http_proxy.empty() && https_proxy.empty() && all_proxy.empty()) {
        return "";
    }
    if (no_proxy.empty()) {
        no_proxy = kDefaultNoProxy;
    }

    std::string exports = "unset http_proxy https_proxy all_proxy no_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY && ";
    auto append_export = [&exports](const char* key, const std::string& value) {
        if (!value.empty()) {
            exports += "export ";
            exports += key;
            exports += "=";
            exports += shell_quote(value);
            exports += " && ";
        }
    };

    append_export("http_proxy", http_proxy);
    append_export("HTTP_PROXY", http_proxy);
    append_export("https_proxy", https_proxy);
    append_export("HTTPS_PROXY", https_proxy);
    append_export("all_proxy", all_proxy);
    append_export("ALL_PROXY", all_proxy);
    append_export("no_proxy", no_proxy);
    append_export("NO_PROXY", no_proxy);
    return exports;
}

ApiResponse handle_config_test(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    cJSON* section_item = cJSON_GetObjectItem(root, "section");
    if (!json_is_string(section_item)) {
        cJSON_Delete(root);
        return make_json_error(400, "missing 'section' field");
    }
    std::string section = section_item->valuestring;

    cJSON* values = cJSON_GetObjectItem(root, "values");
    if (!json_is_object(values)) {
        cJSON_Delete(root);
        return make_json_error(400, "missing 'values' object");
    }

    cJSON* response = cJSON_CreateObject();
    cJSON* results = add_array(response, "results");
    bool all_passed = true;

    SystemProxy current_proxy;
    load_system_env_proxy(options.system_env_path, &current_proxy);
    std::string curl_proxy_arg = build_curl_proxy_arg(current_proxy);
    std::string curl_env_exports = build_proxy_env_exports(current_proxy);

    if (section == "model" || section == "tts" || section == "stt") {
        cJSON* provider_item = cJSON_GetObjectItem(values, "provider");
        cJSON* base_url_item = cJSON_GetObjectItem(values, "base_url");
        std::string provider = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
        std::string base_url = json_is_string(base_url_item) ? trim_copy(base_url_item->valuestring) : "";
        std::string url = base_url.empty() ? provider_default_url(provider) : base_url;
        const bool tencent_stt = section == "stt" && is_tencent_asr_provider(provider);
        if (tencent_stt && url.empty()) {
            url = "https://asr.tencentcloudapi.com";
        }

        cJSON* api_key_item = cJSON_GetObjectItem(values, "api_key");
        std::string api_key = json_is_string(api_key_item) ? trim_copy(api_key_item->valuestring) : "";
        bool api_key_present = json_secret_present(values, "api_key");

        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "endpoint_reachable");
        if (url.empty()) {
            cJSON_AddBoolToObject(r, "passed", 0);
            cJSON_AddStringToObject(r, "detail", "no URL to test (provider unknown and base_url empty)");
            all_passed = false;
        } else {
            std::string cmd = curl_env_exports + "curl -sI --max-time 6 " + curl_proxy_arg + shell_quote(url) + " 2>&1 | head -1";
            CommandResult cr = run_shell_command(cmd);
            bool reachable = cr.exit_code == 0 && cr.output.find("HTTP") != std::string::npos;
            cJSON_AddBoolToObject(r, "passed", reachable ? 1 : 0);
            std::string detail = url + " -> " + trim_trailing_newlines(cr.output);
            cJSON_AddStringToObject(r, "detail", detail.c_str());
            if (!reachable) all_passed = false;
        }
        cJSON_AddItemToArray(results, r);

        if (tencent_stt) {
            bool secret_id_present = json_secret_present(values, "secret_id");
            bool secret_key_present = json_secret_present(values, "secret_key");

            cJSON* r_sid = cJSON_CreateObject();
            cJSON_AddStringToObject(r_sid, "check", "secret_id_present");
            cJSON_AddBoolToObject(r_sid, "passed", secret_id_present ? 1 : 0);
            cJSON_AddStringToObject(r_sid, "detail", secret_id_present ? "secret_id is set" : "secret_id is empty");
            if (!secret_id_present) all_passed = false;
            cJSON_AddItemToArray(results, r_sid);

            cJSON* r_skey = cJSON_CreateObject();
            cJSON_AddStringToObject(r_skey, "check", "secret_key_present");
            cJSON_AddBoolToObject(r_skey, "passed", secret_key_present ? 1 : 0);
            cJSON_AddStringToObject(r_skey, "detail", secret_key_present ? "secret_key is set" : "secret_key is empty");
            if (!secret_key_present) all_passed = false;
            cJSON_AddItemToArray(results, r_skey);
        } else {
            cJSON* r2 = cJSON_CreateObject();
            cJSON_AddStringToObject(r2, "check", "api_key_present");
            cJSON_AddBoolToObject(r2, "passed", api_key_present ? 1 : 0);
            cJSON_AddStringToObject(r2, "detail", api_key_present ? "api_key is set" : "api_key is empty");
            if (!api_key_present) all_passed = false;
            cJSON_AddItemToArray(results, r2);
        }

        bool endpoint_ok = false;
        {
            cJSON* first = cJSON_GetArrayItem(results, 0);
            cJSON* p = cJSON_GetObjectItem(first, "passed");
            endpoint_ok = json_is_type(p, cJSON_True);
        }

        if (section == "model" && endpoint_ok && !api_key.empty()) {
            cJSON* model_item = cJSON_GetObjectItem(values, "model");
            std::string model_name = json_is_string(model_item) ? trim_copy(model_item->valuestring) : "";

            cJSON* r3 = cJSON_CreateObject();
            cJSON_AddStringToObject(r3, "check", "api_key_valid");

            if (model_name.empty()) {
                cJSON_AddBoolToObject(r3, "passed", 0);
                cJSON_AddStringToObject(r3, "detail", "model name is empty; cannot send hello request");
                all_passed = false;
            } else {
                std::string chat_url = url;
                while (!chat_url.empty() && chat_url[chat_url.size() - 1] == '/') {
                    chat_url.erase(chat_url.size() - 1);
                }
                chat_url += "/chat/completions";

                cJSON* req = cJSON_CreateObject();
                cJSON_AddStringToObject(req, "model", model_name.c_str());
                cJSON_AddNumberToObject(req, "max_tokens", 4);
                cJSON_AddNumberToObject(req, "temperature", 0);
                cJSON* msgs = add_array(req, "messages");
                cJSON* msg = cJSON_CreateObject();
                cJSON_AddStringToObject(msg, "role", "user");
                cJSON_AddStringToObject(msg, "content", "hello");
                cJSON_AddItemToArray(msgs, msg);
                std::string req_body = cjson_to_string(req);
                cJSON_Delete(req);

                std::string auth_header = "Authorization: Bearer " + api_key;
                char response_path_template[] = "/tmp/config_test_body.XXXXXX";
                int response_fd = mkstemp(response_path_template);
                if (response_fd < 0) {
                    cJSON_AddBoolToObject(r3, "passed", 0);
                    cJSON_AddStringToObject(r3, "detail", "failed to create temporary response file");
                    all_passed = false;
                } else {
                    close(response_fd);
                    std::string response_path = response_path_template;

                    std::string cmd =
                        curl_env_exports +
                        std::string("curl -sS --max-time 12 ") +
                        curl_proxy_arg +
                        "-o " + shell_quote(response_path) + " -w '%{http_code}' " +
                        "-H " + shell_quote("Content-Type: application/json") + " " +
                        "-H " + shell_quote(auth_header) + " " +
                        "-d " + shell_quote(req_body) + " " +
                        shell_quote(chat_url) + " 2>&1";

                    CommandResult cr = run_shell_command(cmd);
                    std::string http_code = trim_copy(cr.output);
                    std::string resp_body = read_file_contents(response_path.c_str(), 4096);
                    resp_body = trim_trailing_newlines(resp_body);
                    if (resp_body.size() > 400) {
                        resp_body = resp_body.substr(0, 400) + "...";
                    }
                    unlink(response_path.c_str());

                    bool key_ok = (http_code == "200");
                    cJSON_AddBoolToObject(r3, "passed", key_ok ? 1 : 0);

                    std::string detail;
                    if (cr.exit_code != 0 && http_code.empty()) {
                        detail = "request failed: " + cr.output;
                    } else if (key_ok) {
                        detail = "HTTP 200 - key works (model: " + model_name + ")";
                    } else {
                        detail = "HTTP " + http_code + " - " + resp_body;
                    }
                    if (!key_ok) all_passed = false;
                    cJSON_AddStringToObject(r3, "detail", detail.c_str());
                }
            }
            cJSON_AddItemToArray(results, r3);
        }
    } else if (section == "audio") {
        cJSON* socket_item = cJSON_GetObjectItem(values, "socket");
        std::string socket_path = json_is_string(socket_item) ? trim_copy(socket_item->valuestring) : "";
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "socket_exists");
        if (socket_path.empty()) {
            cJSON_AddBoolToObject(r, "passed", 0);
            cJSON_AddStringToObject(r, "detail", "socket path is empty");
            all_passed = false;
        } else {
            std::string cmd = "test -S " + shell_quote(socket_path) + " && echo OK || echo MISSING";
            CommandResult cr = run_shell_command(cmd);
            bool exists = cr.output.find("OK") != std::string::npos;
            cJSON_AddBoolToObject(r, "passed", exists ? 1 : 0);
            std::string detail = socket_path + (exists ? " exists" : " not found");
            cJSON_AddStringToObject(r, "detail", detail.c_str());
            if (!exists) all_passed = false;
        }
        cJSON_AddItemToArray(results, r);
    } else if (section == "benchmark") {
        cJSON* judge_item = cJSON_GetObjectItem(values, "judge_model");
        std::string judge_model = json_is_string(judge_item) ? trim_copy(judge_item->valuestring) : "";
        cJSON* judge_r = cJSON_CreateObject();
        cJSON_AddStringToObject(judge_r, "check", "judge_model");
        cJSON_AddBoolToObject(judge_r, "passed", 1);
        cJSON_AddStringToObject(judge_r, "detail", judge_model.empty() ? "empty; defaults to bytedance-seed/seed-2.0-lite" : judge_model.c_str());
        cJSON_AddItemToArray(results, judge_r);

        cJSON* dir_item = cJSON_GetObjectItem(values, "benchmark_dir");
        std::string benchmark_dir = json_is_string(dir_item) ? trim_copy(dir_item->valuestring) : "";
        cJSON* dir_r = cJSON_CreateObject();
        cJSON_AddStringToObject(dir_r, "check", "benchmark_dir");
        if (benchmark_dir.empty()) {
            cJSON_AddBoolToObject(dir_r, "passed", 1);
            cJSON_AddStringToObject(dir_r, "detail", "empty; agent will auto-detect benchmark root");
        } else {
            std::string cmd = "test -d " + shell_quote(benchmark_dir) + " && echo OK || echo MISSING";
            CommandResult cr = run_shell_command(cmd);
            bool exists = cr.output.find("OK") != std::string::npos;
            cJSON_AddBoolToObject(dir_r, "passed", exists ? 1 : 0);
            std::string detail = benchmark_dir + (exists ? " exists" : " not found");
            cJSON_AddStringToObject(dir_r, "detail", detail.c_str());
            if (!exists) all_passed = false;
        }
        cJSON_AddItemToArray(results, dir_r);
    } else if (section == "hid") {
        const char* dev_keys[] = {"keyboard_device", "mouse_device", NULL};
        for (int i = 0; dev_keys[i]; ++i) {
            cJSON* item = cJSON_GetObjectItem(values, dev_keys[i]);
            std::string path = json_is_string(item) ? trim_copy(item->valuestring) : "";
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", dev_keys[i]);
            if (path.empty()) {
                cJSON_AddBoolToObject(r, "passed", 0);
                cJSON_AddStringToObject(r, "detail", "path is empty");
                all_passed = false;
            } else {
                std::string cmd = "test -c " + shell_quote(path) + " && echo OK || echo MISSING";
                CommandResult cr = run_shell_command(cmd);
                bool exists = cr.output.find("OK") != std::string::npos;
                cJSON_AddBoolToObject(r, "passed", exists ? 1 : 0);
                std::string detail = path + (exists ? " exists" : " not found");
                cJSON_AddStringToObject(r, "detail", detail.c_str());
                if (!exists) all_passed = false;
            }
            cJSON_AddItemToArray(results, r);
        }
        cJSON* pointer_item = cJSON_GetObjectItem(values, "pointer_mode");
        std::string pointer_mode = json_is_string(pointer_item) ? normalize_pointer_mode(pointer_item->valuestring) : "absolute";
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "pointer_mode");
        bool valid = pointer_mode == "absolute" || pointer_mode == "touchscreen";
        cJSON_AddBoolToObject(r, "passed", valid ? 1 : 0);
        std::string detail = valid ? "effective mode: " + pointer_mode : "must be absolute or touchscreen";
        cJSON_AddStringToObject(r, "detail", detail.c_str());
        if (!valid) all_passed = false;
        cJSON_AddItemToArray(results, r);
    } else if (section == "agent") {
        struct Check {
            const char* key;
            const char* allowed[4];
        };
        Check enums[] = {
            {"input_mode", {"text", "stt", "audio", NULL}},
            {"trigger_mode", {"manual", "wakeup", NULL, NULL}},
        };
        for (size_t i = 0; i < sizeof(enums) / sizeof(enums[0]); ++i) {
            cJSON* item = cJSON_GetObjectItem(values, enums[i].key);
            std::string val = json_is_string(item) ? trim_copy(item->valuestring) : "";
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", enums[i].key);
            bool ok = false;
            std::string allowed_list;
            for (int j = 0; enums[i].allowed[j]; ++j) {
                if (j) allowed_list += "/";
                allowed_list += enums[i].allowed[j];
                if (val == enums[i].allowed[j]) ok = true;
            }
            if (val.empty()) {
                cJSON_AddBoolToObject(r, "passed", 0);
                cJSON_AddStringToObject(r, "detail", "empty");
                all_passed = false;
            } else if (ok) {
                cJSON_AddBoolToObject(r, "passed", 1);
                cJSON_AddStringToObject(r, "detail", val.c_str());
            } else {
                cJSON_AddBoolToObject(r, "passed", 0);
                std::string msg = "got '" + val + "', allowed: " + allowed_list;
                cJSON_AddStringToObject(r, "detail", msg.c_str());
                all_passed = false;
            }
            cJSON_AddItemToArray(results, r);
        }

        cJSON* vad_item = cJSON_GetObjectItem(values, "vad_speech_threshold");
        cJSON* vad_r = cJSON_CreateObject();
        cJSON_AddStringToObject(vad_r, "check", "vad_speech_threshold");
        if (json_is_number(vad_item)) {
            double n = vad_item->valuedouble;
            if (n < 0.0 || n > 1.0) {
                cJSON_AddBoolToObject(vad_r, "passed", 0);
                std::string msg = "must be in range [0.0, 1.0], got " + std::to_string(n);
                cJSON_AddStringToObject(vad_r, "detail", msg.c_str());
                all_passed = false;
            } else {
                cJSON_AddBoolToObject(vad_r, "passed", 1);
                cJSON_AddStringToObject(vad_r, "detail", std::to_string(n).c_str());
            }
        } else {
            cJSON_AddBoolToObject(vad_r, "passed", 0);
            cJSON_AddStringToObject(vad_r, "detail", "not a number");
            all_passed = false;
        }
        cJSON_AddItemToArray(results, vad_r);

        cJSON* diff_item = cJSON_GetObjectItem(values, "screen_stable_diff_threshold");
        cJSON* diff_r = cJSON_CreateObject();
        cJSON_AddStringToObject(diff_r, "check", "screen_stable_diff_threshold");
        if (json_is_number(diff_item)) {
            double n = diff_item->valuedouble;
            if (n < 0.0) {
                cJSON_AddBoolToObject(diff_r, "passed", 0);
                std::string msg = "must be >= 0, got " + std::to_string(n);
                cJSON_AddStringToObject(diff_r, "detail", msg.c_str());
                all_passed = false;
            } else {
                cJSON_AddBoolToObject(diff_r, "passed", 1);
                cJSON_AddStringToObject(diff_r, "detail", std::to_string(n).c_str());
            }
        } else {
            cJSON_AddBoolToObject(diff_r, "passed", 0);
            cJSON_AddStringToObject(diff_r, "detail", "not a number");
            all_passed = false;
        }
        cJSON_AddItemToArray(results, diff_r);

        const char* numeric_keys[] = {
            "silence_ms", "min_speech_ms",
            "voice_followup_timeout_ms", "voice_first_turn_timeout_ms",
            "voice_max_turns", "voice_speech_max_runes", "voice_max_response_tokens",
            "screenshot_keep_n", "screenshot_prune_interval",
            "screen_stable_timeout_ms", "screen_stable_ms", NULL
        };
        for (int i = 0; numeric_keys[i]; ++i) {
            cJSON* item = cJSON_GetObjectItem(values, numeric_keys[i]);
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", numeric_keys[i]);
            if (json_is_number(item)) {
                int n = item->valueint;
                if (n < 0) {
                    cJSON_AddBoolToObject(r, "passed", 0);
                    std::string msg = "must be >= 0, got ";
                    msg += std::to_string(n);
                    cJSON_AddStringToObject(r, "detail", msg.c_str());
                    all_passed = false;
                } else {
                    cJSON_AddBoolToObject(r, "passed", 1);
                    cJSON_AddStringToObject(r, "detail", std::to_string(n).c_str());
                }
            } else {
                cJSON_AddBoolToObject(r, "passed", 0);
                cJSON_AddStringToObject(r, "detail", "not a number");
                all_passed = false;
            }
            cJSON_AddItemToArray(results, r);
        }

        cJSON* iter_item = cJSON_GetObjectItem(values, "max_iterations");
        cJSON* iter_r = cJSON_CreateObject();
        cJSON_AddStringToObject(iter_r, "check", "max_iterations");
        if (json_is_number(iter_item)) {
            int n = iter_item->valueint;
            if (n < -1) {
                cJSON_AddBoolToObject(iter_r, "passed", 0);
                std::string msg = "must be >= -1, got " + std::to_string(n);
                cJSON_AddStringToObject(iter_r, "detail", msg.c_str());
                all_passed = false;
            } else {
                cJSON_AddBoolToObject(iter_r, "passed", 1);
                std::string detail = std::to_string(n) + (n == -1 ? " (unlimited)" : "");
                cJSON_AddStringToObject(iter_r, "detail", detail.c_str());
            }
        } else {
            cJSON_AddBoolToObject(iter_r, "passed", 0);
            cJSON_AddStringToObject(iter_r, "detail", "not a number");
            all_passed = false;
        }
        cJSON_AddItemToArray(results, iter_r);
    } else if (section == "telemetry") {
        cJSON* enabled_item = cJSON_GetObjectItem(values, "enabled");
        bool enabled = json_is_bool(enabled_item) && json_is_type(enabled_item, cJSON_True);

        cJSON* provider_item = cJSON_GetObjectItem(values, "provider");
        std::string provider_original = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
        std::string provider = lowercase_copy(provider_original);
        cJSON* provider_r = cJSON_CreateObject();
        cJSON_AddStringToObject(provider_r, "check", "provider");
        if (provider.empty() || provider == "langfuse") {
            cJSON_AddBoolToObject(provider_r, "passed", 1);
            cJSON_AddStringToObject(provider_r, "detail", provider_original.empty() ? "empty; defaults to langfuse" : provider_original.c_str());
        } else {
            cJSON_AddBoolToObject(provider_r, "passed", 0);
            std::string msg = "got '" + provider_original + "', allowed: langfuse";
            cJSON_AddStringToObject(provider_r, "detail", msg.c_str());
            all_passed = false;
        }
        cJSON_AddItemToArray(results, provider_r);

        cJSON* base_url_item = cJSON_GetObjectItem(values, "base_url");
        std::string base_url = json_is_string(base_url_item) ? trim_copy(base_url_item->valuestring) : "";
        cJSON* base_r = cJSON_CreateObject();
        cJSON_AddStringToObject(base_r, "check", "base_url");
        if (enabled && base_url.empty()) {
            cJSON_AddBoolToObject(base_r, "passed", 0);
            cJSON_AddStringToObject(base_r, "detail", "required when telemetry.enabled is true");
            all_passed = false;
        } else {
            cJSON_AddBoolToObject(base_r, "passed", 1);
            cJSON_AddStringToObject(base_r, "detail", base_url.empty() ? "empty; telemetry disabled or not configured" : base_url.c_str());
        }
        cJSON_AddItemToArray(results, base_r);

        if (!base_url.empty()) {
            cJSON* endpoint_r = cJSON_CreateObject();
            cJSON_AddStringToObject(endpoint_r, "check", "endpoint_reachable");
            std::string cmd = curl_env_exports + "curl -sI --max-time 6 " + curl_proxy_arg + shell_quote(base_url) + " 2>&1 | head -1";
            CommandResult cr = run_shell_command(cmd);
            bool reachable = cr.exit_code == 0 && cr.output.find("HTTP") != std::string::npos;
            cJSON_AddBoolToObject(endpoint_r, "passed", reachable ? 1 : 0);
            std::string detail = base_url + " -> " + trim_trailing_newlines(cr.output);
            cJSON_AddStringToObject(endpoint_r, "detail", detail.c_str());
            if (!reachable) all_passed = false;
            cJSON_AddItemToArray(results, endpoint_r);
        }

        const char* key_fields[] = {"public_key", "secret_key", NULL};
        for (int i = 0; key_fields[i]; ++i) {
            bool key_present = json_secret_present(values, key_fields[i]);
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", key_fields[i]);
            if (enabled && !key_present) {
                cJSON_AddBoolToObject(r, "passed", 0);
                cJSON_AddStringToObject(r, "detail", "required when telemetry.enabled is true");
                all_passed = false;
            } else {
                cJSON_AddBoolToObject(r, "passed", 1);
                cJSON_AddStringToObject(r, "detail", key_present ? "set" : "empty; telemetry disabled or not configured");
            }
            cJSON_AddItemToArray(results, r);
        }

        const char* numeric_keys[] = {"upload_timeout_sec", "max_retry", NULL};
        for (int i = 0; numeric_keys[i]; ++i) {
            cJSON* item = cJSON_GetObjectItem(values, numeric_keys[i]);
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", numeric_keys[i]);
            if (json_is_number(item)) {
                int n = item->valueint;
                if (n < 0) {
                    cJSON_AddBoolToObject(r, "passed", 0);
                    std::string msg = "must be >= 0, got ";
                    msg += std::to_string(n);
                    cJSON_AddStringToObject(r, "detail", msg.c_str());
                    all_passed = false;
                } else {
                    cJSON_AddBoolToObject(r, "passed", 1);
                    cJSON_AddStringToObject(r, "detail", std::to_string(n).c_str());
                }
            } else {
                cJSON_AddBoolToObject(r, "passed", 0);
                cJSON_AddStringToObject(r, "detail", "not a number");
                all_passed = false;
            }
            cJSON_AddItemToArray(results, r);
        }
    } else if (section == "search") {
        cJSON* provider_item = cJSON_GetObjectItem(values, "provider");
        std::string provider = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "provider");
        const char* allowed[] = {"duckduckgo", "brave", "brave-free", "tavily", NULL};
        bool ok = false;
        std::string allowed_list;
        for (int i = 0; allowed[i]; ++i) {
            if (i) allowed_list += "/";
            allowed_list += allowed[i];
            if (provider == allowed[i]) ok = true;
        }
        if (provider.empty()) {
            cJSON_AddBoolToObject(r, "passed", 0);
            cJSON_AddStringToObject(r, "detail", "provider is empty");
            all_passed = false;
        } else if (ok) {
            cJSON_AddBoolToObject(r, "passed", 1);
            cJSON_AddStringToObject(r, "detail", provider.c_str());
        } else {
            cJSON_AddBoolToObject(r, "passed", 0);
            std::string msg = "unknown provider '" + provider + "', known: " + allowed_list;
            cJSON_AddStringToObject(r, "detail", msg.c_str());
            all_passed = false;
        }
        cJSON_AddItemToArray(results, r);

        bool api_key_present = json_secret_present(values, "api_key");
        cJSON* r2 = cJSON_CreateObject();
        cJSON_AddStringToObject(r2, "check", "api_key");
        if (provider == "duckduckgo") {
            cJSON_AddBoolToObject(r2, "passed", 1);
            cJSON_AddStringToObject(r2, "detail", api_key_present ? "set (not required for duckduckgo)" : "not required for duckduckgo");
        } else if (provider == "brave" || provider == "brave-free" || provider == "tavily") {
            if (!api_key_present) {
                cJSON_AddBoolToObject(r2, "passed", 0);
                std::string msg = "required for " + provider;
                cJSON_AddStringToObject(r2, "detail", msg.c_str());
                all_passed = false;
            } else {
                cJSON_AddBoolToObject(r2, "passed", 1);
                cJSON_AddStringToObject(r2, "detail", "set");
            }
        } else {
            cJSON_AddBoolToObject(r2, "passed", 1);
            cJSON_AddStringToObject(r2, "detail", api_key_present ? "set" : "empty (may be required for paid providers)");
        }
        cJSON_AddItemToArray(results, r2);
    } else {
        cJSON_Delete(root);
        cJSON_Delete(response);
        return make_json_error(400, "unsupported section: " + section);
    }

    cJSON_Delete(root);
    cJSON_AddBoolToObject(response, "ok", all_passed ? 1 : 0);
    return make_json_ok(response);
}

ApiResponse handle_request(const Options& options, const HttpRequest& request) {
    if (request.error_status_code != 0) {
        ApiResponse response;
        response.status_code = request.error_status_code;
        response.status_text = request.error_status_text;
        response.content_type = "text/plain; charset=utf-8";
        response.body = request.error_body;
        return response;
    }

    if (request.method == "GET" && request.path == "/") {
        ApiResponse response;
        response.content_type = "text/html; charset=utf-8";
        response.body = CONFIG_WEB_HTML;
        return response;
    }

    if (request.method == "GET" && request.path == "/api/agent/status") {
        return handle_get_agent_status();
    }

    if (request.method == "GET" && request.path == "/api/agent/logs") {
        return handle_get_agent_log();
    }

    if (request.method == "GET" && request.path == "/api/ota/logs") {
        return handle_get_ota_log();
    }

    if (request.method == "POST" && (request.path == "/api/ota/update" || request.path == "/api/ota/check-now")) {
        return handle_post_ota_update();
    }

    if (request.method == "GET" && request.path == "/api/config") {
        return handle_get_config(options);
    }

    if (request.method == "GET" && request.path == "/api/config/meta") {
        return handle_get_config_meta();
    }

    if (request.method == "POST" && request.path == "/api/config") {
        return handle_post_config(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/system/env") {
        return handle_post_system_env(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/poweroff") {
        return handle_post_poweroff();
    }

    if (request.method == "POST" && request.path == "/api/wifi/scan") {
        return handle_wifi_scan(options);
    }

    if (request.method == "POST" && request.path == "/api/wifi/connect") {
        return handle_wifi_connect(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/wifi/forget") {
        return handle_wifi_forget(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/config/test") {
        return handle_config_test(options, request.body);
    }

    ApiResponse response;
    response.status_code = 404;
    response.status_text = "Not Found";
    response.content_type = "text/plain; charset=utf-8";
    response.body = "Not Found";
    return response;
}

bool parse_int(const char* text, int min_value, int max_value, int* value) {
    if (!text || !value || text[0] == '\0') {
        return false;
    }

    errno = 0;
    char* end = NULL;
    long parsed = strtol(text, &end, 10);
    if (errno != 0 || !end || *end != '\0' || parsed < min_value || parsed > max_value) {
        return false;
    }

    *value = static_cast<int>(parsed);
    return true;
}

void print_usage(const char* argv0) {
    std::cerr << "Usage: " << argv0 << " [--bind=IP] [--port=PORT]"
              << " [--config=PATH] [--wifi-config=PATH] [--wifi-iface=NAME]"
              << " [--ota-state=PATH] [--cmdline=PATH] [--system-env=PATH]" << std::endl;
}

bool parse_args(int argc, char** argv, Options* options) {
    if (!options) {
        return false;
    }

    for (int i = 1; i < argc; ++i) {
        const char* arg = argv[i];
        std::string value;
        if (consume_prefix(arg, "--bind=", &options->bind_address)) {
        } else if (consume_prefix(arg, "--port=", &value)) {
            if (!parse_int(value.c_str(), 1, 65535, &options->port)) {
                return false;
            }
        } else if (consume_prefix(arg, "--config=", &options->agent_config_path)) {
        } else if (consume_prefix(arg, "--wifi-config=", &options->wifi_config_path)) {
        } else if (consume_prefix(arg, "--wifi-iface=", &options->wifi_interface)) {
        } else if (consume_prefix(arg, "--ota-state=", &options->ota_state_path)) {
        } else if (consume_prefix(arg, "--cmdline=", &options->cmdline_path)) {
        } else if (consume_prefix(arg, "--system-env=", &options->system_env_path)) {
        } else {
            return false;
        }
    }
    return true;
}

}

// AIDEN_CONFIG_WEB_NO_MAIN is defined by the host test build so this file can
// be compiled into the test binary alongside the doctest main(), catching
// signature/header regressions that would otherwise only surface during the
// firmware cross-compile.
#ifndef AIDEN_CONFIG_WEB_NO_MAIN
int main(int argc, char** argv) {
    Options options;
    if (!parse_args(argc, argv, &options)) {
        print_usage(argv[0]);
        return 1;
    }

    if (!is_allowed_bind_address(options.bind_address)) {
        std::cerr << "Refusing to bind config_web to " << options.bind_address
                  << "; allowed addresses are " << kAnyBindAddress
                  << ", " << kUsbBindAddress << " and " << kLoopbackBindAddress << std::endl;
        return 1;
    }

    signal(SIGPIPE, SIG_IGN);

    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = on_signal;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = 0;
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);

    int server_fd = socket(AF_INET, SOCK_STREAM, 0);
    if (server_fd < 0) {
        std::cerr << "Failed to create socket" << std::endl;
        return 1;
    }

    fcntl(server_fd, F_SETFD, FD_CLOEXEC);

    int opt = 1;
    setsockopt(server_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(options.port));
    if (inet_pton(AF_INET, options.bind_address.c_str(), &addr.sin_addr) != 1) {
        std::cerr << "Invalid bind address: " << options.bind_address << std::endl;
        close(server_fd);
        return 1;
    }

    if (bind(server_fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "Failed to bind to " << options.bind_address << ":" << options.port
                  << ": " << strerror(errno) << std::endl;
        close(server_fd);
        return 1;
    }

    if (listen(server_fd, 10) < 0) {
        std::cerr << "Failed to listen: " << strerror(errno) << std::endl;
        close(server_fd);
        return 1;
    }

    std::cout << "Config server running on http://" << options.bind_address << ":"
              << options.port << std::endl;
    std::cout << "agent.toml -> " << options.agent_config_path << std::endl;
    std::cout << "wifi.conf  -> " << options.wifi_config_path << std::endl;
    std::cout << "system env -> " << options.system_env_path << std::endl;

    while (!g_should_stop) {
        sockaddr_in client_addr;
        socklen_t client_len = sizeof(client_addr);
        int client_fd = accept(server_fd, reinterpret_cast<sockaddr*>(&client_addr), &client_len);
        if (client_fd < 0) {
            if (errno == EINTR) {
                continue;
            }
            break;
        }

        if (!set_socket_recv_timeout(client_fd, kClientReadTimeoutSeconds)) {
            close(client_fd);
            continue;
        }

        HttpRequest request = parse_request(client_fd);
        ApiResponse response = handle_request(options, request);
        send_response(client_fd,
                      response.status_code,
                      response.status_text,
                      response.content_type,
                      response.body);
        close(client_fd);
    }

    close(server_fd);
    return 0;
}
#endif  // AIDEN_CONFIG_WEB_NO_MAIN
