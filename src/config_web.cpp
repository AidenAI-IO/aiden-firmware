#include "agent_toml.h"
#include "aiden_log.h"
#include "config_web_static_assets.h"
#include "system_env_parser.h"
#include "wifi_config.h"

#include "cJSON/cJSON.h"

#include <arpa/inet.h>
#include <errno.h>
#include <dirent.h>
#include <fcntl.h>
#include <ctype.h>
#include <netdb.h>
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
#include <algorithm>
#include <cmath>
#include <functional>
#include <limits>
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
    std::string storage_state_path = "/run/aiden/storage.state";
    std::string web_root = "/oem/usr/share/aiden/config-web";
};

struct HttpRequest {
    std::string method;
    // path holds the request target with any query string stripped, so routes
    // can compare it for equality. The raw query lives in `query`.
    std::string path;
    std::string query;
    std::string body;
    std::string body_file_path;
    size_t body_size = 0;
    int error_status_code = 0;
    std::string error_status_text;
    std::string error_body;
};

struct ApiResponse {
    int status_code = 200;
    std::string status_text = "OK";
    std::string content_type = "application/json; charset=utf-8";
    std::string body = "{}";
    int body_fd = -1;
    unsigned long long body_fd_size = 0;
    std::vector<std::pair<std::string, std::string> > headers;
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

struct AgentHttpTarget {
    std::string host = "127.0.0.1";
    int port = 8080;
    std::string base_path;
};

struct AgentHttpResponse {
    int status_code = 0;
    std::string status_text;
    std::string content_type = "application/json; charset=utf-8";
    std::string body;
    std::string error;
};

volatile sig_atomic_t g_should_stop = 0;
pid_t g_agent_restart_pid = -1;
bool g_agent_restart_readiness_pending = false;
bool g_agent_restart_deferred = false;
bool g_stt_live_test_active = false;

const char* kAnyBindAddress = "0.0.0.0";
const char* kUsbBindAddress = "192.168.42.1";
const char* kLoopbackBindAddress = "127.0.0.1";
const char* kAgentInitScript = "/etc/init.d/S53agent";
const char* kLocaleSimplifiedChinese = "zh-CN";
const char* kLocaleEnglishUS = "en-US";
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

// Tests replace the init script with a deterministic fixture. Production uses
// the fixed on-device path.
const char* agent_init_script_path() {
    static const char* cached = []() -> const char* {
        const char* override_path = std::getenv("AIDEN_AGENT_INIT_SCRIPT");
        if (override_path && override_path[0] != '\0') {
            return override_path;
        }
        return kAgentInitScript;
    }();
    return cached;
}
const char* kOtaBin = "/oem/usr/bin/ota";
const char* kEnvRunBin = "/oem/usr/bin/aiden-env-run";
const char* kOtaHealthLogPath = "/var/log/ota/ota.log";
const char* kOtaWebUpdateLockPath = "/tmp/config_web_ota_update.lock";
const char* kOtaWebUpdateLogPath = "/userdata/ota/config_web_ota_update.log";
const char* kAgentPortHost = "127.0.0.1";
const int kDefaultAgentPort = 8080;
const int kAgentStatusCommandTimeoutMs = 1500;
const int kAgentRestartWaitTimeoutMs = 15000;
const int kAgentRestartPollIntervalMs = 50;
const size_t kAgentStatusLogReadSize = 16 * 1024;
const size_t kAgentStatusLogDisplaySize = 4096;
const size_t kAgentLogReadSize = 64 * 1024;
const size_t kAgentLogDisplaySize = 48 * 1024;
const size_t kOtaLogReadSize = 128 * 1024;
const size_t kOtaLogDisplaySize = 96 * 1024;
const size_t kOtaWebUpdateLogMaxBytes = 1024 * 1024;
const int kSupportLogExportTimeoutMs = 30000;
const char* kSupportLogArchiveName = "aiden-logs.tar.gz";
const char* kSupportLogStagePrefix = "aiden-logs-stage-";
const char* kSupportLogArchivePrefix = "aiden-logs-";
const char* kSupportLogLangfuseName = "langfuse.yaml";
const char* kSupportLogAgentName = "agent.log";
const char* kSupportLogHttpName = "http.log";
const size_t kSupportLogLangfuseMaxBytes = 1024 * 1024;
const size_t kSupportLogAgentMaxBytes = 1024 * 1024;
const size_t kSupportLogHttpMaxBytes = 4 * 1024 * 1024;
// LLM raw HTTP logs live next to the agent config: <dirname(config)>/log.
// Filenames are llm-http-YYYYMMDDHHMMSSmmm.log.
const char* kLlmLogPrefix = "llm-http-";
const char* kLlmLogSuffix = ".log";
const size_t kMaxHttpHeaderSize = 8 * 1024;
const size_t kMaxHttpBodySize = 64 * 1024;
const int kClientReadTimeoutSeconds = 5;
const char* kDefaultNoProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16";
const int kSystemEnvCommandTimeoutMs = 1500;
const size_t kMaxSystemEnvSize = 64 * 1024;
const size_t kMaxAgentConfigSize = 1024 * 1024;

std::string read_file_contents(const char* path, size_t max_size);
bool read_file_contents_checked(const char* path, size_t max_size, std::string* contents, std::string* error);
std::string shell_quote(const std::string& text);
std::string validate_proxy_url(const std::string& url);
std::string lowercase_copy(const std::string& text);
bool parse_decimal(const std::string& text, int min_value, int max_value, int* value);
std::string parent_dir(const std::string& path);
std::string llm_log_dir(const Options& options);
std::string agent_log_path(const Options& options);
bool mkdir_p(const std::string& dir, std::string* error);
bool prepare_ota_update_log_file(const std::string& path,
                                 size_t max_size,
                                 unsigned long long* start_size_bytes,
                                 std::string* error);
bool set_fd_cloexec(int fd, std::string* error);
bool create_temp_file_in_dir(const std::string& dir,
                             const std::string& prefix,
                             mode_t mode,
                             int* fd,
                             std::string* path,
                             std::string* error);
bool create_temp_dir_in_dir(const std::string& dir,
                            const std::string& prefix,
                            std::string* path,
                            std::string* error);
bool is_llm_log_import_request(const std::string& method, const std::string& path);
void cleanup_request_temp_file(HttpRequest* request);
void close_response_stream(ApiResponse* response);
CommandResult run_command_with_stdin(const std::string& command, const std::string& input, int timeout_ms);
void update_config_from_json(cJSON* root, aiden::AgentToml* config);
bool atomic_write_file(const std::string& path, const std::string& content, mode_t mode, std::string* error);
std::string cjson_to_string(cJSON* json);
ApiResponse make_json_error(int status_code, const std::string& message);
ApiResponse make_json_ok(cJSON* root);
bool parse_stt_test_values_request(cJSON* root, cJSON** stt_values, std::string* error);
AgentRuntimeStatus query_agent_status(const Options& options);
bool parse_http_base_url(const std::string& base_url,
                         AgentHttpTarget* target,
                         std::string* error);
std::string join_url_path(const std::string& base_path, const std::string& path);
int connect_tcp_host(const std::string& host, int port, int timeout_ms, std::string* error);
bool send_agent_http_request(const AgentHttpTarget& target,
                             const char* method,
                             const std::string& path,
                             const std::string& body,
                             bool send_body,
                             AgentHttpResponse* response);
bool post_agent_http_json(const AgentHttpTarget& target,
                          const std::string& path,
                          const std::string& body,
                          AgentHttpResponse* response);
bool resolve_agent_http_target(const Options& options,
                               AgentHttpTarget* target,
                               std::string* error);
ApiResponse proxy_agent_request(const Options& options,
                                const char* method,
                                const std::string& agent_path,
                                const std::string& body,
                                bool send_body);
ApiResponse proxy_agent_json_request(const Options& options,
                                         const std::string& agent_path,
                                         const std::string& body);
ApiResponse proxy_agent_get_request(const Options& options,
                                     const std::string& agent_path_with_query);
ApiResponse handle_api_models(const Options& options, const std::string& query_string);
ApiResponse handle_usb_reenumerate(const Options& options);
void preserve_redacted_agent_secrets(const Options& options, aiden::AgentToml* config);
long long monotonic_millis();
bool reap_agent_restart_process(bool wait, std::string* error);
void start_deferred_agent_restart_if_idle();
ApiResponse handle_stt_test_start(const Options& options, const std::string& body);
ApiResponse handle_stt_test_stop(const Options& options, const std::string& body);

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
ValidationResult validate_agent_config_path_via_cli(const std::string& config_path,
                                                     const char* agent_bin_path);

enum ReadStatus {
    READ_STATUS_OK,
    READ_STATUS_EOF,
    READ_STATUS_ERROR,
    READ_STATUS_TIMEOUT,
    READ_STATUS_LIMIT_EXCEEDED,
};

ReadStatus read_to_eof(int fd, std::string* out, size_t max_size);

enum LlmLogOpenStatus {
    LLM_LOG_OPEN_OK,
    LLM_LOG_OPEN_INVALID_NAME,
    LLM_LOG_OPEN_NOT_FOUND,
    LLM_LOG_OPEN_ERROR,
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

std::string normalize_device_type(const std::string& value) {
    std::string device_type = trim_copy(value);
    for (size_t i = 0; i < device_type.size(); ++i) {
        device_type[i] = static_cast<char>(tolower(static_cast<unsigned char>(device_type[i])));
    }
    if (device_type.empty() || device_type == "ios") return "iOS";
    if (device_type == "android") return "Android";
    if (device_type == "macos" || device_type == "mac" || device_type == "darwin") return "macOS";
    if (device_type == "windows" || device_type == "win") return "windows";
    if (device_type == "linux") return "linux";
    return device_type;
}

std::string pointer_mode_for_device_type(const std::string& device_type) {
    return normalize_device_type(device_type) == "Android" ? "touchscreen" : "absolute";
}

std::string device_type_from_platform(const std::string& value) {
    std::string platform = trim_copy(value);
    for (size_t i = 0; i < platform.size(); ++i) {
        platform[i] = static_cast<char>(tolower(static_cast<unsigned char>(platform[i])));
    }
    if (platform == "ios" || platform == "iphone" || platform == "ipad" || platform == "ipados") return "iOS";
    if (platform == "android") return "Android";
    if (platform == "macos" || platform == "mac" || platform == "darwin") return "macOS";
    if (platform == "windows" || platform == "win") return "windows";
    if (platform == "linux") return "linux";
    return "";
}

std::string effective_device_type(const aiden::AgentToml& config) {
    std::string configured = trim_copy(config.device.device_type);
    if (!configured.empty()) {
        return normalize_device_type(configured);
    }
    std::string platform_device_type = device_type_from_platform(config.default_platform);
    if (!platform_device_type.empty()) {
        return platform_device_type;
    }
    return normalize_pointer_mode(config.hid.pointer_mode) == "touchscreen" ? "Android" : "iOS";
}

std::string normalize_input_backend(const std::string& value) {
    std::string backend = trim_copy(value);
    for (size_t i = 0; i < backend.size(); ++i) {
        backend[i] = static_cast<char>(tolower(static_cast<unsigned char>(backend[i])));
    }
    return backend.empty() ? "hid" : backend;
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

cJSON* parse_config_test_result(const std::string& output) {
    size_t offset = 0;
    while ((offset = output.find('{', offset)) != std::string::npos) {
        cJSON* root = cJSON_ParseWithOpts(output.c_str() + offset, NULL, 0);
        if (json_is_object(root)) {
            cJSON* ok = cJSON_GetObjectItem(root, "ok");
            cJSON* results = cJSON_GetObjectItem(root, "results");
            if (json_is_bool(ok) && json_is_array(results)) {
                return root;
            }
        }
        if (root) {
            cJSON_Delete(root);
        }
        ++offset;
    }
    return NULL;
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

bool is_valid_bare_toml_key(const std::string& key) {
    if (key.empty()) return false;
    for (char c : key) {
        bool valid = (c >= 'a' && c <= 'z') ||
                     (c >= 'A' && c <= 'Z') ||
                     (c >= '0' && c <= '9') ||
                     c == '_' || c == '-';
        if (!valid) return false;
    }
    return true;
}

bool validate_non_negative_json_integer(cJSON* item,
                                        const std::string& path,
                                        std::string* error) {
    if (!item) return true;
    double value = item->valuedouble;
    if (!json_is_number(item) || !std::isfinite(value) || value < 0.0 ||
        std::floor(value) != value || value > std::numeric_limits<int>::max()) {
        if (error) {
            *error = "agent config invalid field " + path + ": expected non-negative integer";
        }
        return false;
    }
    return true;
}

std::string config_field_path(const char* section, const char* key) {
    return std::string(section) + "." + key;
}

enum ConfigFieldKind {
    CONFIG_FIELD_STRING,
    CONFIG_FIELD_NUMBER,
    CONFIG_FIELD_BOOL,
    CONFIG_FIELD_ARRAY,
    CONFIG_FIELD_OBJECT,
};

struct ConfigMetadataSchema {
    std::string json;
    std::map<std::string, std::map<std::string, ConfigFieldKind> > field_types;
    struct StringInRule {
        std::string field;
        std::vector<std::string> values;
    };
    std::map<std::string, std::map<std::string, StringInRule> > string_in_rules;
};

bool parse_config_string_in_rule(cJSON* visible_when,
                                 ConfigMetadataSchema::StringInRule* rule) {
    if (!json_is_object(visible_when) || !rule) return false;
    // This is deliberately a narrow parser for one metadata-backed capability
    // rule, not a second implementation of the browser's visibility engine.
    // Mixed rule groups are ambiguous here and must fail closed.
    if (cJSON_GetObjectItem(visible_when, "any")) return false;
    cJSON* all = cJSON_GetObjectItem(visible_when, "all");
    if (!json_is_array(all) || cJSON_GetArraySize(all) != 1) return false;

    cJSON* condition = cJSON_GetArrayItem(all, 0);
    cJSON* field = cJSON_GetObjectItem(condition, "field");
    cJSON* op = cJSON_GetObjectItem(condition, "op");
    cJSON* values = cJSON_GetObjectItem(condition, "values");
    if (!json_is_object(condition) ||
        !json_is_string(field) || !field->valuestring || !field->valuestring[0] ||
        !json_is_string(op) || std::string(op->valuestring) != "in" ||
        !json_is_array(values)) {
        return false;
    }

    ConfigMetadataSchema::StringInRule parsed;
    parsed.field = field->valuestring;
    for (cJSON* value = values->child; value; value = value->next) {
        if (!json_is_string(value) || !value->valuestring) return false;
        parsed.values.push_back(value->valuestring);
    }
    *rule = parsed;
    return true;
}

bool config_field_kind_from_metadata(cJSON* field,
                                     ConfigFieldKind* kind,
                                     std::string* error) {
    cJSON* widget_item = cJSON_GetObjectItem(field, "widget");
    if (!json_is_string(widget_item) || !widget_item->valuestring) {
        if (error) *error = "config metadata field is missing a string widget";
        return false;
    }

    const std::string widget = widget_item->valuestring;
    cJSON* range = cJSON_GetObjectItem(field, "range");
    if (range && !json_is_object(range)) {
        if (error) *error = "config metadata field range must be an object";
        return false;
    }
    if (widget == "select" && range) {
        *kind = CONFIG_FIELD_NUMBER;
        return true;
    }
    if (widget == "text" || widget == "textarea" || widget == "select") {
        *kind = CONFIG_FIELD_STRING;
        return true;
    }
    if (widget == "number") {
        *kind = CONFIG_FIELD_NUMBER;
        return true;
    }
    if (widget == "boolean") {
        *kind = CONFIG_FIELD_BOOL;
        return true;
    }
    if (widget == "list") {
        *kind = CONFIG_FIELD_ARRAY;
        return true;
    }

    if (error) *error = "config metadata field has unsupported widget " + widget;
    return false;
}

bool parse_config_metadata_schema(const std::string& json,
                                  ConfigMetadataSchema* schema,
                                  std::string* error) {
    cJSON* root = cJSON_Parse(json.c_str());
    if (!json_is_object(root)) {
        if (root) cJSON_Delete(root);
        if (error) *error = "config metadata returned an unexpected response";
        return false;
    }

    cJSON* sections = cJSON_GetObjectItem(root, "sections");
    if (!json_is_array(sections)) {
        cJSON_Delete(root);
        if (error) *error = "config metadata returned an unexpected response";
        return false;
    }

    ConfigMetadataSchema parsed;
    parsed.json = json;
    for (cJSON* section = sections->child; section; section = section->next) {
        cJSON* name_item = cJSON_GetObjectItem(section, "name");
        cJSON* fields = cJSON_GetObjectItem(section, "fields");
        if (!json_is_object(section) || !json_is_string(name_item) ||
            !name_item->valuestring || !name_item->valuestring[0] ||
            !json_is_array(fields)) {
            cJSON_Delete(root);
            if (error) *error = "config metadata returned an unexpected response";
            return false;
        }

        const std::string section_name = name_item->valuestring;
        if (parsed.field_types.count(section_name)) {
            cJSON_Delete(root);
            if (error) *error = "config metadata contains duplicate section " + section_name;
            return false;
        }

        std::map<std::string, ConfigFieldKind>& section_fields = parsed.field_types[section_name];
        for (cJSON* field = fields->child; field; field = field->next) {
            cJSON* key_item = cJSON_GetObjectItem(field, "key");
            if (!json_is_object(field) || !json_is_string(key_item) ||
                !key_item->valuestring || !key_item->valuestring[0]) {
                cJSON_Delete(root);
                if (error) *error = "config metadata returned an unexpected response";
                return false;
            }

            const std::string key = key_item->valuestring;
            if (section_fields.count(key)) {
                cJSON_Delete(root);
                if (error) *error = "config metadata contains duplicate field " +
                                    section_name + "." + key;
                return false;
            }

            ConfigFieldKind kind;
            std::string kind_error;
            if (!config_field_kind_from_metadata(field, &kind, &kind_error)) {
                cJSON_Delete(root);
                if (error) *error = "config metadata field " + section_name + "." + key +
                                    " is invalid: " + kind_error;
                return false;
            }
            section_fields[key] = kind;

            cJSON* visible_when = cJSON_GetObjectItem(field, "visibleWhen");
            if (visible_when) {
                ConfigMetadataSchema::StringInRule rule;
                if (parse_config_string_in_rule(visible_when, &rule)) {
                    parsed.string_in_rules[section_name][key] = rule;
                }
            }
        }
    }
    cJSON_Delete(root);
    *schema = parsed;
    return true;
}

bool load_agent_config_metadata(const ConfigMetadataSchema** schema,
                                std::string* error) {
    static ConfigMetadataSchema cached;
    static bool loaded = false;
    if (loaded) {
        if (schema) *schema = &cached;
        return true;
    }

    const char* agent_bin = agent_bin_path();
    if (!file_exists(agent_bin)) {
        AIDEN_LOG_ERROR("agent_config", "binary_not_found",
                        "path=%s operation=config_metadata", agent_bin);
        if (error) *error = "config metadata unavailable: agent binary not found";
        return false;
    }

    const std::string cmd = shell_quote(agent_bin) + " config-meta --format=json";
    CommandResult result = run_command_with_stdin(cmd, "", 2000);
    if (result.timed_out) {
        if (error) *error = "config metadata timed out";
        return false;
    }
    if (result.exit_code != 0) {
        AIDEN_LOG_ERROR("agent_config", "metadata_generation_failed",
                        "exit_code=%d output=%s", result.exit_code, result.output.c_str());
        if (error) *error = "config metadata generation failed";
        return false;
    }

    ConfigMetadataSchema parsed;
    std::string parse_error;
    if (!parse_config_metadata_schema(result.output, &parsed, &parse_error)) {
        AIDEN_LOG_ERROR("agent_config", "metadata_invalid_payload", "output=%s",
                        result.output.c_str());
        if (error) *error = parse_error.empty()
            ? "config metadata returned an unexpected response"
            : parse_error;
        return false;
    }

    cached = parsed;
    loaded = true;
    if (schema) *schema = &cached;
    return true;
}

bool validate_config_value_type(cJSON* item,
                                const std::string& path,
                                ConfigFieldKind kind,
                                std::string* error) {
    if (!item) return true;
    switch (kind) {
        case CONFIG_FIELD_STRING:
            return json_is_string(item) || config_schema_error(error, path, "string", item);
        case CONFIG_FIELD_NUMBER:
            return json_is_number(item) || config_schema_error(error, path, "number", item);
        case CONFIG_FIELD_BOOL:
            return json_is_bool(item) || config_schema_error(error, path, "bool", item);
        case CONFIG_FIELD_ARRAY:
            return json_is_array(item) || config_schema_error(error, path, "array", item);
        case CONFIG_FIELD_OBJECT:
            return json_is_object(item) || config_schema_error(error, path, "object", item);
    }
    return config_schema_error(error, path, "known field kind", item);
}

bool validate_config_field_type(cJSON* obj,
                                const char* section,
                                const char* key,
                                ConfigFieldKind kind,
                                std::string* error) {
    cJSON* item = obj ? cJSON_GetObjectItem(obj, key) : NULL;
    if (!item) {
        return true;
    }
    return validate_config_value_type(item, config_field_path(section, key), kind, error);
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

bool validate_ota_github_proxy_url(cJSON* root, std::string* error) {
    cJSON* ota = cJSON_GetObjectItem(root, "ota");
    if (!ota) {
        return true;
    }
    if (!json_is_object(ota)) {
        return config_schema_error(error, "ota", "object", ota);
    }

    cJSON* proxy_url_item = cJSON_GetObjectItem(ota, "github_proxy_url");
    if (!proxy_url_item || !json_is_string(proxy_url_item)) {
        return true;
    }

    std::string url = trim_copy(proxy_url_item->valuestring);
    if (url.empty()) {
        return true; // Empty is valid (no proxy)
    }

    // Check for https:// prefix (case-insensitive)
    std::string lower_url = lowercase_copy(url);
    if (lower_url.find("https://") != 0) {
        if (error) {
            *error = "ota.github_proxy_url: must use https (got: " + url + ")";
        }
        return false;
    }

    // Check that there's content after https://
    if (url.length() <= 8) { // "https://" is 8 chars
        if (error) {
            *error = "ota.github_proxy_url: missing host";
        }
        return false;
    }

    return true;
}

bool is_provider_record_section(const std::string& section) {
    return section == "model_providers" || section == "tts_providers" ||
           section == "stt_providers";
}

bool validate_metadata_field_types(cJSON* root,
                                   const ConfigMetadataSchema& metadata,
                                   std::string* error) {
    for (std::map<std::string, std::map<std::string, ConfigFieldKind> >::const_iterator
             section_it = metadata.field_types.begin();
         section_it != metadata.field_types.end(); ++section_it) {
        cJSON* section = cJSON_GetObjectItem(root, section_it->first.c_str());
        if (!section) continue;
        if (!json_is_object(section)) {
            return config_schema_error(error, section_it->first, "object", section);
        }

        if (is_provider_record_section(section_it->first)) {
            for (cJSON* record = section->child; record; record = record->next) {
                if (!json_is_object(record)) continue;
                const std::string record_name = record->string ? record->string : "";
                for (std::map<std::string, ConfigFieldKind>::const_iterator
                         field_it = section_it->second.begin();
                     field_it != section_it->second.end(); ++field_it) {
                    if (!validate_config_value_type(
                            cJSON_GetObjectItem(record, field_it->first.c_str()),
                            section_it->first + "." + record_name + "." + field_it->first,
                            field_it->second,
                            error)) {
                        return false;
                    }
                }

                // Older clients used "provider" for the record type. Keep the
                // alias, but derive its type from the canonical metadata field.
                std::map<std::string, ConfigFieldKind>::const_iterator type_it =
                    section_it->second.find("type");
                if (type_it != section_it->second.end() &&
                    !validate_config_value_type(
                        cJSON_GetObjectItem(record, "provider"),
                        section_it->first + "." + record_name + ".provider",
                        type_it->second,
                        error)) {
                    return false;
                }
            }
            continue;
        }

        for (std::map<std::string, ConfigFieldKind>::const_iterator
                 field_it = section_it->second.begin();
             field_it != section_it->second.end(); ++field_it) {
            if (!validate_config_value_type(
                    cJSON_GetObjectItem(section, field_it->first.c_str()),
                    section_it->first + "." + field_it->first,
                    field_it->second,
                    error)) {
                return false;
            }
        }
    }
    return true;
}

bool validate_flat_provider_compatibility_fields(
        cJSON* root,
        const ConfigMetadataSchema& metadata,
        const char* flat_section,
        const char* record_section,
        const char* const* fields,
        std::string* error) {
    cJSON* flat = cJSON_GetObjectItem(root, flat_section);
    if (!json_is_object(flat)) return true;
    std::map<std::string, std::map<std::string, ConfigFieldKind> >::const_iterator record_it =
        metadata.field_types.find(record_section);
    if (record_it == metadata.field_types.end()) return true;

    for (int i = 0; fields[i]; ++i) {
        std::map<std::string, ConfigFieldKind>::const_iterator field_it =
            record_it->second.find(fields[i]);
        if (field_it == record_it->second.end()) continue;
        if (!validate_config_value_type(
                cJSON_GetObjectItem(flat, fields[i]),
                std::string(flat_section) + "." + fields[i],
                field_it->second,
                error)) {
            return false;
        }
    }
    return true;
}

bool validate_compatibility_config_field_types(cJSON* root, std::string* error) {
    struct FieldSpec {
        const char* section;
        const char* key;
        ConfigFieldKind kind;
    };
    // These fields remain part of the config-web compatibility contract but
    // are intentionally absent from the UI metadata. Keep this list small;
    // ordinary fields belong in ConfigMeta.
    const FieldSpec fields[] = {
        {"device", "backend", CONFIG_FIELD_STRING},
        {"search", "has_api_key", CONFIG_FIELD_BOOL},
        {"termination_policy", "enabled", CONFIG_FIELD_BOOL},
        {"termination_policy", "max_seconds", CONFIG_FIELD_NUMBER},
        {"termination_policy", "repeat_action_limit", CONFIG_FIELD_NUMBER},
        {"termination_policy", "same_result_limit", CONFIG_FIELD_NUMBER},
        {"termination_policy", "screen_unchanged_limit", CONFIG_FIELD_NUMBER},
        {"termination_policy", "soft_notice_stall_score", CONFIG_FIELD_NUMBER},
        {"termination_policy", "restrict_tools_stall_score", CONFIG_FIELD_NUMBER},
        {"termination_policy", "terminate_stall_score", CONFIG_FIELD_NUMBER},
        {"termination_policy", "parse_failure_limit", CONFIG_FIELD_NUMBER},
        {"live_activity", "enabled", CONFIG_FIELD_BOOL},
        {NULL, NULL, CONFIG_FIELD_STRING},
    };

    for (int i = 0; fields[i].section; ++i) {
        cJSON* section = cJSON_GetObjectItem(root, fields[i].section);
        if (!validate_config_field_type(section, fields[i].section, fields[i].key,
                                        fields[i].kind, error)) {
            return false;
        }
    }
    return true;
}

bool validate_voice_notification_config(cJSON* root, std::string* error) {
    cJSON* voice_notifications = cJSON_GetObjectItem(root, "voice_notifications");
    if (json_is_object(voice_notifications)) {
        if (!validate_config_field_type(voice_notifications, "voice_notifications", "enabled", CONFIG_FIELD_BOOL, error) ||
            !validate_config_field_type(voice_notifications, "voice_notifications", "max_pending", CONFIG_FIELD_NUMBER, error) ||
            !validate_config_field_type(voice_notifications, "voice_notifications", "response_tail", CONFIG_FIELD_OBJECT, error) ||
            !validate_config_field_type(voice_notifications, "voice_notifications", "expiration", CONFIG_FIELD_OBJECT, error)) {
            return false;
        }
        if (!validate_non_negative_json_integer(
                cJSON_GetObjectItem(voice_notifications, "max_pending"),
                "voice_notifications.max_pending",
                error)) {
            return false;
        }
        cJSON* response_tail = cJSON_GetObjectItem(voice_notifications, "response_tail");
        if (json_is_object(response_tail)) {
            if (!validate_config_field_type(response_tail, "voice_notifications.response_tail", "enabled", CONFIG_FIELD_BOOL, error) ||
                !validate_config_field_type(response_tail, "voice_notifications.response_tail", "max_items", CONFIG_FIELD_NUMBER, error) ||
                !validate_config_field_type(response_tail, "voice_notifications.response_tail", "max_text_chars", CONFIG_FIELD_NUMBER, error)) {
                return false;
            }
            if (!validate_non_negative_json_integer(
                    cJSON_GetObjectItem(response_tail, "max_items"),
                    "voice_notifications.response_tail.max_items",
                    error) ||
                !validate_non_negative_json_integer(
                    cJSON_GetObjectItem(response_tail, "max_text_chars"),
                    "voice_notifications.response_tail.max_text_chars",
                    error)) {
                return false;
            }
        }

        cJSON* expiration = cJSON_GetObjectItem(voice_notifications, "expiration");
        if (json_is_object(expiration)) {
            if (!validate_config_field_type(expiration, "voice_notifications.expiration", "default_ttl_seconds", CONFIG_FIELD_NUMBER, error) ||
                !validate_config_field_type(expiration, "voice_notifications.expiration", "code_ttl_seconds", CONFIG_FIELD_OBJECT, error)) {
                return false;
            }
            if (!validate_non_negative_json_integer(
                    cJSON_GetObjectItem(expiration, "default_ttl_seconds"),
                    "voice_notifications.expiration.default_ttl_seconds",
                    error)) {
                return false;
            }
            cJSON* code_ttl_seconds = cJSON_GetObjectItem(expiration, "code_ttl_seconds");
            if (json_is_object(code_ttl_seconds)) {
                for (cJSON* item = code_ttl_seconds->child; item; item = item->next) {
                    const std::string code = item->string ? item->string : "";
                    if (!is_valid_bare_toml_key(code)) {
                        if (error) {
                            *error = "agent config invalid field voice_notifications.expiration.code_ttl_seconds."
                                   + code + ": expected bare TOML key";
                        }
                        return false;
                    }
                    if (!json_is_number(item)) {
                        return config_schema_error(
                            error,
                            std::string("voice_notifications.expiration.code_ttl_seconds.") + code,
                            "number",
                            item);
                    }
                    if (!validate_non_negative_json_integer(
                            item,
                            std::string("voice_notifications.expiration.code_ttl_seconds.") + code,
                            error)) {
                        return false;
                    }
                }
            }
        }
    }
    return true;
}

bool validate_config_field_types_from_metadata(cJSON* root,
                                               const ConfigMetadataSchema& metadata,
                                               std::string* error) {
    static const char* const model_compat[] = {"api_key", NULL};
    static const char* const tts_compat[] = {
        "api_key", "model", "voice_id", "reference_id", "emotion", NULL,
    };
    static const char* const stt_compat[] = {
        "api_key", "model", "base_url", "app_id", "secret_id", "secret_key",
        "region", "engine_model_type", NULL,
    };

    return validate_metadata_field_types(root, metadata, error) &&
           validate_flat_provider_compatibility_fields(
               root, metadata, "model", "model_providers", model_compat, error) &&
           validate_flat_provider_compatibility_fields(
               root, metadata, "tts", "tts_providers", tts_compat, error) &&
           validate_flat_provider_compatibility_fields(
               root, metadata, "stt", "stt_providers", stt_compat, error) &&
           validate_compatibility_config_field_types(root, error) &&
           validate_voice_notification_config(root, error);
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

std::string mask_secret(const std::string& secret) {
    if (secret.empty()) {
        return "";
    }
    if (secret.length() <= 8) {
        return "***";
    }
    // Show first 4 and last 4 characters, mask the middle
    return secret.substr(0, 4) + "***" + secret.substr(secret.length() - 4);
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

// Extract the IPv4 address from `ifconfig` output, supporting both the
// BusyBox ("inet addr:192.168.1.2") and iproute/glibc ("inet 192.168.1.2")
// formats. Returns an empty string when no IPv4 address is present.
std::string extract_ifconfig_ipv4(const std::string& text) {
    std::istringstream input(text);
    std::string line;
    while (std::getline(input, line)) {
        std::string trimmed = trim_copy(line);
        std::string value;
        if (starts_with(trimmed, "inet addr:")) {
            value = trimmed.substr(std::string("inet addr:").size());
        } else if (starts_with(trimmed, "inet ") && !starts_with(trimmed, "inet6")) {
            value = trimmed.substr(std::string("inet ").size());
        } else {
            continue;
        }
        // The address is the first whitespace-delimited token.
        size_t end = value.find_first_of(" \t");
        if (end != std::string::npos) {
            value = value.substr(0, end);
        }
        value = trim_copy(value);
        if (!value.empty()) {
            return value;
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

ReadStatus read_exact_to_fd(int in_fd, int out_fd, size_t length) {
    char buf[16 * 1024];
    size_t remaining = length;
    while (remaining > 0) {
        size_t chunk = remaining < sizeof(buf) ? remaining : sizeof(buf);
        ssize_t n = read(in_fd, buf, chunk);
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
        if (!write_all(out_fd, buf, static_cast<size_t>(n))) {
            return READ_STATUS_ERROR;
        }
        remaining -= static_cast<size_t>(n);
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

bool is_llm_log_import_request(const std::string& method, const std::string& path) {
    static const std::string prefix = "/api/llm-logs/import/";
    return method == "POST" && path.compare(0, prefix.size(), prefix) == 0;
}

void cleanup_request_temp_file(HttpRequest* request) {
    if (!request || request->body_file_path.empty()) {
        return;
    }
    if (unlink(request->body_file_path.c_str()) != 0 && errno != ENOENT) {
        // Best-effort cleanup only.
    }
    request->body_file_path.clear();
}

void close_response_stream(ApiResponse* response) {
    if (!response || response->body_fd < 0) {
        return;
    }
    close(response->body_fd);
    response->body_fd = -1;
    response->body_fd_size = 0;
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

HttpRequest parse_request(int client_fd, const Options& options) {
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
            req.query = req.path.substr(query_pos + 1);
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
        req.body_size = content_length;
        if (is_llm_log_import_request(req.method, req.path)) {
            int temp_fd = -1;
            std::string temp_path;
            std::string temp_error;
            if (!create_temp_file_in_dir(llm_log_dir(options), "llm-log-upload-", 0644, &temp_fd, &temp_path, &temp_error)) {
                set_request_error(&req, 500, "Internal Server Error",
                                  temp_error.empty() ? "failed to prepare import body file" : temp_error.c_str());
                return req;
            }
            req.body_file_path = temp_path;

            ReadStatus body_status = read_exact_to_fd(client_fd, temp_fd, content_length);
            if (body_status == READ_STATUS_OK && fsync(temp_fd) != 0) {
                body_status = READ_STATUS_ERROR;
            }
            if (close(temp_fd) != 0 && body_status == READ_STATUS_OK) {
                body_status = READ_STATUS_ERROR;
            }

            if (body_status == READ_STATUS_TIMEOUT) {
                cleanup_request_temp_file(&req);
                set_request_error(&req, 408, "Request Timeout", "request body read timed out");
                return req;
            }
            if (body_status != READ_STATUS_OK) {
                cleanup_request_temp_file(&req);
                set_request_error(&req, 400, "Bad Request", "truncated request body");
                return req;
            }
        } else {
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
    }

    return req;
}

void send_response(int client_fd, const ApiResponse& response) {
    std::ostringstream header;
    header << "HTTP/1.1 " << response.status_code << " " << response.status_text << "\r\n";
    header << "Content-Type: " << response.content_type << "\r\n";
    for (size_t i = 0; i < response.headers.size(); ++i) {
        header << response.headers[i].first << ": " << response.headers[i].second << "\r\n";
    }
    if (response.body_fd >= 0) {
        header << "Content-Length: " << response.body_fd_size << "\r\n";
    } else {
        header << "Content-Length: " << response.body.size() << "\r\n";
    }
    header << "Connection: close\r\n\r\n";

    std::string header_text = header.str();
    write_all(client_fd, header_text.data(), header_text.size());
    if (response.body_fd >= 0) {
        if (lseek(response.body_fd, 0, SEEK_SET) == static_cast<off_t>(-1)) {
            return;
        }
        char buf[16 * 1024];
        while (true) {
            ssize_t n = read(response.body_fd, buf, sizeof(buf));
            if (n < 0) {
                if (errno == EINTR) {
                    continue;
                }
                return;
            }
            if (n == 0) {
                break;
            }
            if (!write_all(client_fd, buf, static_cast<size_t>(n))) {
                return;
            }
        }
        return;
    }
    write_all(client_fd, response.body.data(), response.body.size());
}

bool validate_agent_config_patch_json(cJSON* root, std::string* error = NULL) {
    if (!json_is_object(root)) {
        return config_schema_error(error, "root", "object", root);
    }

    if (cJSON_GetObjectItem(root, "providers")) {
        if (error) {
            *error = "agent config field providers is unsupported; use model_providers";
        }
        return false;
    }

    const ConfigMetadataSchema* metadata = NULL;
    if (!load_agent_config_metadata(&metadata, error)) {
        return false;
    }

    const char* sections[] = {
        "model_providers", "tts_providers", "stt_providers", "model",
        "tts", "stt", "audio", "audio_archive", "storage",
        "voice_notifications", "log", "ota", "device", "hid", "search", "telemetry",
        "termination_policy", "live_activity", "agent", NULL,
    };
    for (int i = 0; sections[i]; ++i) {
        cJSON* section = cJSON_GetObjectItem(root, sections[i]);
        if (section && !json_is_object(section)) {
            return config_schema_error(error, sections[i], "object", section);
        }
    }
    for (std::map<std::string, std::map<std::string, ConfigFieldKind> >::const_iterator
             section_it = metadata->field_types.begin();
         section_it != metadata->field_types.end(); ++section_it) {
        cJSON* section = cJSON_GetObjectItem(root, section_it->first.c_str());
        if (section && !json_is_object(section)) {
            return config_schema_error(error, section_it->first, "object", section);
        }
    }

    // The three provider sections are maps of name -> object rather than flat
    // sections, so their entries need their own type check: a non-object entry
    // would otherwise be skipped silently and the record dropped on save. The
    // name check mirrors save_agent_toml, which refuses to write a name that is
    // not a bare TOML key -- accepting one here would produce a config that
    // loads cleanly but fails every later save.
    const char* provider_maps[] = {
        "model_providers", "tts_providers", "stt_providers", NULL,
    };
    for (int i = 0; provider_maps[i]; ++i) {
        cJSON* records = cJSON_GetObjectItem(root, provider_maps[i]);
        if (!json_is_object(records)) continue;
        for (cJSON* item = records->child; item; item = item->next) {
            const std::string name = item->string ? item->string : "";
            if (!json_is_object(item)) {
                return config_schema_error(error, std::string(provider_maps[i]) + "." + name,
                                          "object", item);
            }
            if (!is_valid_bare_toml_key(name)) {
                if (error) {
                    *error = std::string("agent config invalid ") + provider_maps[i]
                           + " name \"" + name
                           + "\": expected letters, digits, '-' or '_'";
                }
                return false;
            }
        }
    }

    // A patch may omit any field, but it may not blank model.provider: the agent
    // requires it (Config.Validate: "model.provider is required"), so accepting
    // an empty value writes an agent.toml that loads here but refuses to start.
    // The provider select leads with an empty placeholder, which is how a save
    // can carry one.
    cJSON* model_section = cJSON_GetObjectItem(root, "model");
    if (json_is_object(model_section) && cJSON_GetObjectItem(model_section, "provider")
        && !validate_required_string(model_section, "model", "provider", false, error)) {
        return false;
    }

    return validate_config_field_types_from_metadata(root, *metadata, error);
}

bool validate_agent_config_json(cJSON* root, std::string* error = NULL) {
    if (!validate_agent_config_patch_json(root, error)) {
        return false;
    }
    if (!validate_model_section(root, error) ||
        !validate_search_secret_presence(root, error) ||
        !validate_ota_github_proxy_url(root, error)) {
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
        AIDEN_LOG_ERROR("agent_config", "binary_not_found", "path=%s operation=load",
                        agent_bin);
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
        AIDEN_LOG_ERROR("agent_config", "load_failed", "exit_code=%d output=%s",
                        result.exit_code, result.output.c_str());
        return false;
    }

    cJSON* resolved = cJSON_Parse(result.output.c_str());
    if (!resolved) {
        if (error) {
            *error = "agent config returned an unexpected response";
        }
        AIDEN_LOG_ERROR("agent_config", "invalid_json", "operation=load output=%s",
                        result.output.c_str());
        return false;
    }

    std::string schema_error;
    if (!validate_agent_config_json(resolved, &schema_error)) {
        if (error) {
            *error = schema_error.empty() ? "agent config invalid schema" : schema_error;
        }
        AIDEN_LOG_ERROR("agent_config", "invalid_schema", "error=%s output=%s",
                        schema_error.empty() ? "unknown_schema_error" : schema_error.c_str(),
                        result.output.c_str());
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
        return;
    }
    preserve_redacted_agent_secrets(options, config);
}

void preserve_redacted_agent_secrets(const Options& options, aiden::AgentToml* config) {
    if (!config) {
        return;
    }
    bool need_search_api_key = config->search.api_key.empty() && config->search.has_api_key;
    bool need_provider_secrets = false;
    for (const auto& item : config->model_providers) {
        need_provider_secrets = need_provider_secrets || item.second.api_key.empty();
    }
    for (const auto& item : config->tts_providers) {
        need_provider_secrets = need_provider_secrets || item.second.api_key.empty();
    }
    for (const auto& item : config->stt_providers) {
        need_provider_secrets = need_provider_secrets || item.second.api_key.empty() ||
            item.second.secret_id.empty() || item.second.secret_key.empty();
    }
    if (!need_search_api_key && !need_provider_secrets) {
        return;
    }

    aiden::AgentToml stored;
    std::string load_error;
    if (!aiden::load_agent_toml(options.agent_config_path.c_str(), stored, &load_error)) {
        return;
    }
    if (need_search_api_key && !stored.search.api_key.empty()) {
        config->search.api_key = stored.search.api_key;
        config->search.has_api_key = true;
    }
    for (auto& item : config->model_providers) {
        std::map<std::string, aiden::ModelProviderToml>::const_iterator previous =
            stored.model_providers.find(item.first);
        if (previous != stored.model_providers.end() && item.second.api_key.empty()) {
            item.second.api_key = previous->second.api_key;
        }
    }
    for (auto& item : config->tts_providers) {
        std::map<std::string, aiden::TTSProviderToml>::const_iterator previous =
            stored.tts_providers.find(item.first);
        if (previous != stored.tts_providers.end() && item.second.api_key.empty()) {
            item.second.api_key = previous->second.api_key;
        }
    }
    for (auto& item : config->stt_providers) {
        std::map<std::string, aiden::STTProviderToml>::const_iterator previous =
            stored.stt_providers.find(item.first);
        if (previous == stored.stt_providers.end()) continue;
        if (item.second.api_key.empty()) item.second.api_key = previous->second.api_key;
        if (item.second.secret_id.empty()) item.second.secret_id = previous->second.secret_id;
        if (item.second.secret_key.empty()) item.second.secret_key = previous->second.secret_key;
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

bool parse_stt_test_values_request(cJSON* root, cJSON** stt_values, std::string* error) {
    if (stt_values) {
        *stt_values = NULL;
    }
    cJSON* values = root ? cJSON_GetObjectItem(root, "stt_values") : NULL;
    if (!json_is_object(values)) {
        if (error) *error = "missing 'stt_values' object";
        return false;
    }
    if (stt_values) {
        *stt_values = values;
    }
    if (error) {
        error->clear();
    }
    return true;
}

bool validate_stt_test_provider_fields(cJSON* values, std::string* error) {
    static const char* fields[] = {
        "provider",
        "api_key",
        "model",
        "base_url",
        "language",
        "app_id",
        "secret_id",
        "secret_key",
        "region",
        "engine_model_type",
        NULL,
    };
    for (size_t i = 0; fields[i]; ++i) {
        if (!validate_config_field_type(
                values, "stt_values", fields[i], CONFIG_FIELD_STRING, error)) {
            return false;
        }
    }
    return true;
}

bool parse_http_base_url(const std::string& base_url,
                         AgentHttpTarget* target,
                         std::string* error) {
    if (target) {
        *target = AgentHttpTarget();
    }
    std::string trimmed = trim_copy(base_url);
    if (trimmed.empty()) {
        if (error) *error = "empty agent HTTP base URL";
        return false;
    }
    if (!starts_with(trimmed, "http://")) {
        if (error) *error = "agent HTTP base URL must start with http://";
        return false;
    }

    std::string rest = trimmed.substr(strlen("http://"));
    size_t slash = rest.find('/');
    std::string host_port = slash == std::string::npos ? rest : rest.substr(0, slash);
    std::string base_path = slash == std::string::npos ? "" : rest.substr(slash);
    if (host_port.empty()) {
        if (error) *error = "agent HTTP base URL is missing host";
        return false;
    }

    std::string host = host_port;
    int port = 80;
    if (host_port[0] == '[') {
        size_t end = host_port.find(']');
        if (end == std::string::npos) {
            if (error) *error = "agent HTTP base URL has invalid IPv6 host";
            return false;
        }
        host = host_port.substr(1, end - 1);
        if (end + 1 < host_port.size()) {
            if (host_port[end + 1] != ':') {
                if (error) *error = "agent HTTP base URL has invalid host:port";
                return false;
            }
            if (!parse_decimal(host_port.substr(end + 2), 1, 65535, &port)) {
                if (error) *error = "agent HTTP base URL has invalid port";
                return false;
            }
        }
    } else {
        size_t colon = host_port.rfind(':');
        if (colon != std::string::npos) {
            host = host_port.substr(0, colon);
            if (!parse_decimal(host_port.substr(colon + 1), 1, 65535, &port)) {
                if (error) *error = "agent HTTP base URL has invalid port";
                return false;
            }
        }
    }

    host = trim_copy(host);
    if (host.empty()) {
        if (error) *error = "agent HTTP base URL is missing host";
        return false;
    }

    if (base_path == "/") {
        base_path.clear();
    }
    if (!base_path.empty() && base_path[0] != '/') {
        base_path.insert(base_path.begin(), '/');
    }

    if (target) {
        target->host = host;
        target->port = port;
        target->base_path = base_path;
    }
    if (error) {
        error->clear();
    }
    return true;
}

std::string join_url_path(const std::string& base_path, const std::string& path) {
    if (base_path.empty()) {
        return path.empty() ? "/" : path;
    }
    if (path.empty() || path == "/") {
        return base_path;
    }
    if (base_path[base_path.size() - 1] == '/' && path[0] == '/') {
        return base_path + path.substr(1);
    }
    if (base_path[base_path.size() - 1] != '/' && path[0] != '/') {
        return base_path + "/" + path;
    }
    return base_path + path;
}

int connect_tcp_host(const std::string& host, int port, int timeout_ms, std::string* error) {
    if (error) {
        error->clear();
    }

    struct addrinfo hints;
    memset(&hints, 0, sizeof(hints));
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;

    char port_text[16];
    snprintf(port_text, sizeof(port_text), "%d", port);

    struct addrinfo* results = NULL;
    int gai = getaddrinfo(host.c_str(), port_text, &hints, &results);
    if (gai != 0) {
        if (error) {
            *error = std::string("resolve agent host: ") + gai_strerror(gai);
        }
        return -1;
    }

    int fd = -1;
    std::string last_error = "connect failed";
    for (struct addrinfo* ai = results; ai != NULL; ai = ai->ai_next) {
        fd = socket(ai->ai_family, ai->ai_socktype, ai->ai_protocol);
        if (fd < 0) {
            last_error = std::string("socket failed: ") + strerror(errno);
            continue;
        }

        int flags = fcntl(fd, F_GETFL, 0);
        if (flags >= 0) {
            (void)fcntl(fd, F_SETFL, flags | O_NONBLOCK);
        }

        int rc = connect(fd, ai->ai_addr, ai->ai_addrlen);
        if (rc == 0) {
            if (flags >= 0) {
                (void)fcntl(fd, F_SETFL, flags);
            }
            break;
        }
        if (errno != EINPROGRESS) {
            last_error = strerror(errno);
            close(fd);
            fd = -1;
            continue;
        }

        fd_set write_fds;
        FD_ZERO(&write_fds);
        FD_SET(fd, &write_fds);
        struct timeval timeout;
        timeout.tv_sec = timeout_ms / 1000;
        timeout.tv_usec = (timeout_ms % 1000) * 1000;
        rc = select(fd + 1, NULL, &write_fds, NULL, &timeout);
        if (rc > 0 && FD_ISSET(fd, &write_fds)) {
            int socket_error = 0;
            socklen_t socket_error_len = sizeof(socket_error);
            if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &socket_error, &socket_error_len) == 0 &&
                socket_error == 0) {
                if (flags >= 0) {
                    (void)fcntl(fd, F_SETFL, flags);
                }
                break;
            }
            last_error = strerror(socket_error == 0 ? errno : socket_error);
        } else if (rc == 0) {
            last_error = "connect timed out";
        } else {
            last_error = std::string("select failed: ") + strerror(errno);
        }
        close(fd);
        fd = -1;
    }

    freeaddrinfo(results);

    if (fd < 0 && error) {
        *error = last_error;
    }
    return fd;
}

ReadStatus read_to_eof(int fd, std::string* out, size_t max_size) {
    if (out) {
        out->clear();
    }

    char buf[16 * 1024];
    while (true) {
        size_t remaining = out ? (max_size > out->size() ? max_size - out->size() : 0) : sizeof(buf);
        size_t to_read = remaining < sizeof(buf) ? remaining : sizeof(buf);
        if (to_read == 0) {
            to_read = 1;
        }

        ssize_t n = read(fd, buf, to_read);
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
            return READ_STATUS_OK;
        }
        if (out && out->size() + static_cast<size_t>(n) > max_size) {
            return READ_STATUS_LIMIT_EXCEEDED;
        }
        if (out) {
            out->append(buf, static_cast<size_t>(n));
        }
    }
}

// Sends one HTTP/1.1 request to the agent and reads the whole response.
// Both the POST and the GET proxy go through here: keeping a single copy of the
// socket setup, status-line parsing and body reading is what stops one path
// from silently losing a size cap or a short-write guard.
// `send_body` separates a POST that carries an (possibly empty) JSON body from a
// GET, which must not announce Content-Type or Content-Length at all.
bool send_agent_http_request(const AgentHttpTarget& target,
                             const char* method,
                             const std::string& path,
                             const std::string& body,
                             bool send_body,
                             AgentHttpResponse* response) {
    if (response) {
        *response = AgentHttpResponse();
    }

    std::string connect_error;
    int fd = connect_tcp_host(target.host, target.port, 2000, &connect_error);
    if (fd < 0) {
        if (response) {
            response->error = connect_error.empty() ? "connect failed" : connect_error;
        }
        return false;
    }

    if (!set_socket_recv_timeout(fd, kClientReadTimeoutSeconds)) {
        if (response) {
            response->error = std::string("set socket timeout failed: ") + strerror(errno);
        }
        close(fd);
        return false;
    }

    std::string request_path = join_url_path(target.base_path, path);
    if (request_path.empty()) {
        request_path = "/";
    }

    std::ostringstream request;
    request << method << " " << request_path << " HTTP/1.1\r\n";
    request << "Host: " << target.host << ":" << target.port << "\r\n";
    request << "Accept: application/json\r\n";
    if (send_body) {
        request << "Content-Type: application/json\r\n";
        request << "Content-Length: " << body.size() << "\r\n";
    }
    request << "Connection: close\r\n\r\n";
    if (send_body) {
        request << body;
    }
    std::string request_text = request.str();
    if (!write_all(fd, request_text.data(), request_text.size())) {
        if (response) {
            response->error = std::string("write request failed: ") + strerror(errno);
        }
        close(fd);
        return false;
    }

    std::string headers;
    ReadStatus header_status = read_until(fd, "\r\n\r\n", kMaxHttpHeaderSize, &headers);
    if (header_status != READ_STATUS_OK) {
        if (response) {
            response->error = header_status == READ_STATUS_TIMEOUT
                ? "agent HTTP response timed out"
                : "invalid agent HTTP response";
        }
        close(fd);
        return false;
    }

    std::istringstream header_stream(headers);
    std::string line;
    if (!std::getline(header_stream, line)) {
        if (response) {
            response->error = "agent HTTP response missing status line";
        }
        close(fd);
        return false;
    }
    line = trim_copy(line);
    std::istringstream status_stream(line);
    std::string http_version;
    int status_code = 0;
    status_stream >> http_version >> status_code;
    std::string status_text;
    std::getline(status_stream, status_text);
    status_text = trim_copy(status_text);
    if (status_code <= 0) {
        if (response) {
            response->error = "agent HTTP response has invalid status code";
        }
        close(fd);
        return false;
    }

    size_t content_length = 0;
    bool has_content_length = false;
    std::string content_type = "application/json; charset=utf-8";
    while (std::getline(header_stream, line)) {
        line = trim_copy(line);
        if (line.empty()) {
            continue;
        }
        std::string lower = lowercase_copy(line);
        if (starts_with(lower, "content-length:")) {
            if (!parse_size(trim_copy(line.substr(strlen("Content-Length:"))), &content_length)) {
                if (response) {
                    response->error = "agent HTTP response has invalid Content-Length";
                }
                close(fd);
                return false;
            }
            has_content_length = true;
        } else if (starts_with(lower, "content-type:")) {
            content_type = trim_copy(line.substr(strlen("Content-Type:")));
        }
    }

    std::string response_body;
    if (has_content_length) {
        if (content_length > kMaxHttpBodySize) {
            if (response) {
                response->error = "agent HTTP response body too large";
            }
            close(fd);
            return false;
        }
        ReadStatus body_status = read_exact(fd, &response_body, content_length);
        if (body_status != READ_STATUS_OK) {
            if (response) {
                response->error = body_status == READ_STATUS_TIMEOUT
                    ? "agent HTTP response body timed out"
                    : "truncated agent HTTP response body";
            }
            close(fd);
            return false;
        }
    } else {
        ReadStatus body_status = read_to_eof(fd, &response_body, kMaxHttpBodySize);
        if (body_status != READ_STATUS_OK) {
            if (response) {
                response->error = body_status == READ_STATUS_TIMEOUT
                    ? "agent HTTP response body timed out"
                    : "agent HTTP response body too large";
            }
            close(fd);
            return false;
        }
    }

    close(fd);

    if (response) {
        response->status_code = status_code;
        response->status_text = status_text.empty() ? "OK" : status_text;
        response->content_type = content_type.empty() ? "application/json; charset=utf-8" : content_type;
        response->body = response_body;
        response->error.clear();
    }
    return true;
}

bool post_agent_http_json(const AgentHttpTarget& target,
                          const std::string& path,
                          const std::string& body,
                          AgentHttpResponse* response) {
    return send_agent_http_request(target, "POST", path, body, true, response);
}

bool resolve_agent_http_target(const Options& options,
                               AgentHttpTarget* target,
                               std::string* error) {
    if (target) {
        *target = AgentHttpTarget();
    }

    const char* override_url = std::getenv("AIDEN_AGENT_HTTP_BASE_URL");
    if (override_url && override_url[0] != '\0') {
        return parse_http_base_url(override_url, target, error);
    }

    AgentRuntimeStatus status = query_agent_status(options);
    if (target) {
        target->host = trim_copy(status.port_host).empty() ? kAgentPortHost : status.port_host;
        target->port = status.port > 0 ? status.port : kDefaultAgentPort;
        target->base_path.clear();
    }
    if (error) {
        error->clear();
    }
    (void)options;
    return true;
}

// Resolves the agent endpoint, forwards one request to it and translates the
// upstream reply into an ApiResponse. `agent_path` may already carry a query
// string; it is forwarded verbatim.
ApiResponse proxy_agent_request(const Options& options,
                                const char* method,
                                const std::string& agent_path,
                                const std::string& body,
                                bool send_body) {
    AgentHttpTarget target;
    std::string target_error;
    if (!resolve_agent_http_target(options, &target, &target_error)) {
        return make_json_error(503, target_error.empty() ? "resolve agent HTTP target failed" : target_error);
    }

    AgentHttpResponse upstream;
    if (!send_agent_http_request(target, method, agent_path, body, send_body, &upstream)) {
        return make_json_error(503, upstream.error.empty() ? "agent HTTP request failed" : upstream.error);
    }

    ApiResponse response;
    response.status_code = upstream.status_code > 0 ? upstream.status_code : 200;
    response.status_text = upstream.status_text.empty() ? "OK" : upstream.status_text;
    response.content_type = upstream.content_type.empty()
        ? "application/json; charset=utf-8"
        : upstream.content_type;
    response.body = upstream.body;
    return response;
}

ApiResponse proxy_agent_json_request(const Options& options,
                                         const std::string& agent_path,
                                         const std::string& body) {
    return proxy_agent_request(options, "POST", agent_path, body, true);
}

ApiResponse proxy_stt_test_start_after_restart(const Options& options,
                                               const std::string& body) {
    const bool wait_for_readiness = g_agent_restart_readiness_pending;
    if (wait_for_readiness) {
        std::string wait_error;
        if (!reap_agent_restart_process(true, &wait_error)) {
            return make_json_error(503, wait_error);
        }
    }

    AgentHttpTarget target;
    std::string target_error;
    if (!resolve_agent_http_target(options, &target, &target_error)) {
        return make_json_error(503, target_error.empty() ? "resolve agent HTTP target failed" : target_error);
    }

    const long long deadline = monotonic_millis() + kAgentRestartWaitTimeoutMs;
    AgentHttpResponse upstream;
    while (!send_agent_http_request(
            target, "POST", "/api/config-test/stt/start", body, true, &upstream)) {
        if (!wait_for_readiness || monotonic_millis() >= deadline) {
            return make_json_error(503, upstream.error.empty() ? "agent HTTP request failed" : upstream.error);
        }
        usleep(kAgentRestartPollIntervalMs * 1000);
    }

    if (wait_for_readiness) {
        g_agent_restart_readiness_pending = false;
    }

    ApiResponse response;
    response.status_code = upstream.status_code > 0 ? upstream.status_code : 200;
    response.status_text = upstream.status_text.empty() ? "OK" : upstream.status_text;
    response.content_type = upstream.content_type.empty()
        ? "application/json; charset=utf-8"
        : upstream.content_type;
    response.body = upstream.body;
    if (response.status_code >= 200 && response.status_code < 300) {
        g_stt_live_test_active = true;
    }
    return response;
}

ApiResponse handle_stt_test_start(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    cJSON* stt_values = NULL;
    std::string parse_error;
    if (!parse_stt_test_values_request(root, &stt_values, &parse_error)) {
        cJSON_Delete(root);
        return make_json_error(400, parse_error);
    }
    if (!validate_stt_test_provider_fields(stt_values, &parse_error)) {
        cJSON_Delete(root);
        return make_json_error(400, parse_error);
    }

    cJSON_Delete(root);
    return proxy_stt_test_start_after_restart(options, body);
}

ApiResponse handle_stt_test_stop(const Options& options, const std::string& body) {
    ApiResponse response = proxy_agent_json_request(options, "/api/config-test/stt/stop", body);
    if (response.status_code >= 200 && response.status_code < 300) {
        g_stt_live_test_active = false;
        start_deferred_agent_restart_if_idle();
    }
    return response;
}

// GET request proxy to agent. `agent_path_with_query` keeps its query string.
ApiResponse proxy_agent_get_request(const Options& options,
                                     const std::string& agent_path_with_query) {
    return proxy_agent_request(options, "GET", agent_path_with_query, std::string(), false);
}

// Handle GET /api/models?provider=xxx&locale=xxx
ApiResponse handle_api_models(const Options& options, const std::string& query_string) {
    // Build agent path with query string
    std::string agent_path = "/api/models";
    if (!query_string.empty()) {
        agent_path += "?" + query_string;
    }

    return proxy_agent_get_request(options, agent_path);
}

// Shell script that unbinds and rebinds the USB gadget UDC to force the host
// to re-enumerate the device. Fails loudly if either transition does not
// succeed so callers can distinguish a real re-enumeration from a no-op.
static const char* kUsbReenumerateScript =
    "#!/bin/sh\n"
    "UDC=$(ls /sys/class/udc/ 2>/dev/null | head -n1)\n"
    "GADGET_DIR=/sys/kernel/config/usb_gadget/aiden_hid\n"
    "if [ -z \"$UDC\" ] || [ ! -d \"$GADGET_DIR\" ]; then\n"
    "  echo 'Error: UDC device or gadget directory not found' >&2\n"
    "  exit 1\n"
    "fi\n"
    "# Unbind\n"
    "if ! echo \"\" > \"$GADGET_DIR/UDC\" 2>/dev/null; then\n"
    "  echo 'Error: Failed to unbind UDC' >&2\n"
    "  exit 2\n"
    "fi\n"
    "sleep 1\n"
    "# Rebind\n"
    "if ! echo \"$UDC\" > \"$GADGET_DIR/UDC\" 2>/dev/null; then\n"
    "  echo 'Error: Failed to rebind UDC' >&2\n"
    "  exit 3\n"
    "fi\n"
    "echo 'USB re-enumeration completed successfully'\n";

ApiResponse handle_usb_reenumerate(const Options& options) {
    CommandResult result = run_command_with_stdin("sh -s", kUsbReenumerateScript, 5000);

    if (result.exit_code != 0) {
        std::string error_msg = result.output.empty()
            ? "USB re-enumeration failed with exit code " + std::to_string(result.exit_code)
            : result.output;
        return make_json_error(500, error_msg);
    }

    cJSON* response = cJSON_CreateObject();
    cJSON_AddStringToObject(response, "status", "ok");
    cJSON_AddStringToObject(response, "message", "USB re-enumeration triggered successfully");
    return make_json_ok(response);
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

    // Add model_providers
    cJSON* model_providers = cJSON_CreateObject();
    for (const auto& item : config.model_providers) {
        const std::string& provider_name = item.first;
        const aiden::ModelProviderToml& provider = item.second;

        cJSON* provider_obj = cJSON_CreateObject();
        cJSON_AddStringToObject(provider_obj, "type", provider.type.c_str());

        if (include_secrets) {
            cJSON_AddStringToObject(provider_obj, "api_key", provider.api_key.c_str());
        } else {
            cJSON_AddBoolToObject(provider_obj, "has_api_key", provider.api_key.empty() ? 0 : 1);
        }
        if (!provider.base_url.empty()) {
            cJSON_AddStringToObject(provider_obj, "base_url", provider.base_url.c_str());
        }

        cJSON_AddItemToObject(model_providers, provider_name.c_str(), provider_obj);
    }
    cJSON_AddItemToObject(root, "model_providers", model_providers);

    // Provider credentials are write-only. Browser responses expose only
    // configured-state flags; the save path preserves omitted values.
    cJSON* tts_providers = cJSON_CreateObject();
    for (const auto& item : config.tts_providers) {
        const aiden::TTSProviderToml& record = item.second;
        cJSON* record_obj = cJSON_CreateObject();
        cJSON_AddStringToObject(record_obj, "type", record.type.c_str());
        if (include_secrets) {
            cJSON_AddStringToObject(record_obj, "api_key", record.api_key.c_str());
        } else {
            cJSON_AddBoolToObject(record_obj, "has_api_key", record.api_key.empty() ? 0 : 1);
        }
        if (!record.model.empty()) {
            cJSON_AddStringToObject(record_obj, "model", record.model.c_str());
        }
        if (!record.voice_id.empty()) {
            cJSON_AddStringToObject(record_obj, "voice_id", record.voice_id.c_str());
        }
        if (!record.emotion.empty()) {
            cJSON_AddStringToObject(record_obj, "emotion", record.emotion.c_str());
        }
        if (!record.reference_id.empty()) {
            cJSON_AddStringToObject(record_obj, "reference_id", record.reference_id.c_str());
        }
        cJSON_AddItemToObject(tts_providers, item.first.c_str(), record_obj);
    }
    cJSON_AddItemToObject(root, "tts_providers", tts_providers);

    // STT credentials follow the same write-only contract.
    cJSON* stt_providers = cJSON_CreateObject();
    for (const auto& item : config.stt_providers) {
        const aiden::STTProviderToml& record = item.second;
        cJSON* record_obj = cJSON_CreateObject();
        cJSON_AddStringToObject(record_obj, "type", record.type.c_str());
        if (include_secrets) {
            cJSON_AddStringToObject(record_obj, "api_key", record.api_key.c_str());
        } else {
            cJSON_AddBoolToObject(record_obj, "has_api_key", record.api_key.empty() ? 0 : 1);
        }
        if (!record.model.empty()) {
            cJSON_AddStringToObject(record_obj, "model", record.model.c_str());
        }
        if (!record.base_url.empty()) {
            cJSON_AddStringToObject(record_obj, "base_url", record.base_url.c_str());
        }
        if (!record.app_id.empty()) {
            cJSON_AddStringToObject(record_obj, "app_id", record.app_id.c_str());
        }
        if (!record.secret_id.empty()) {
            if (include_secrets) {
                cJSON_AddStringToObject(record_obj, "secret_id", record.secret_id.c_str());
            }
        }
        if (!include_secrets) {
            cJSON_AddBoolToObject(record_obj, "has_secret_id", record.secret_id.empty() ? 0 : 1);
        }
        if (!record.secret_key.empty()) {
            if (include_secrets) {
                cJSON_AddStringToObject(record_obj, "secret_key", record.secret_key.c_str());
            }
        }
        if (!include_secrets) {
            cJSON_AddBoolToObject(record_obj, "has_secret_key", record.secret_key.empty() ? 0 : 1);
        }
        if (!record.region.empty()) {
            cJSON_AddStringToObject(record_obj, "region", record.region.c_str());
        }
        if (!record.engine_model_type.empty()) {
            cJSON_AddStringToObject(record_obj, "engine_model_type", record.engine_model_type.c_str());
        }
        cJSON_AddItemToObject(stt_providers, item.first.c_str(), record_obj);
    }
    cJSON_AddItemToObject(root, "stt_providers", stt_providers);

    cJSON* device = add_object(root, "device");
    cJSON_AddStringToObject(device, "backend", config.device.backend.c_str());
    cJSON_AddStringToObject(device, "device_type", effective_device_type(config).c_str());

    cJSON* model = add_object(root, "model");
    cJSON_AddStringToObject(model, "provider", config.model.provider.c_str());
    // model.api_key is accepted only as a legacy/internal validation field.
    // The config web contract exposes credentials through model_providers so
    // the flat model section cannot become a second source of truth.
    if (include_secrets) {
        cJSON_AddStringToObject(model, "api_key", config.model.api_key.c_str());
    }
    cJSON_AddStringToObject(model, "model", config.model.model.c_str());
    if (!config.model.api_mode.empty()) {
        cJSON_AddStringToObject(model, "api_mode", config.model.api_mode.c_str());
    }
    cJSON_AddStringToObject(model, "reasoning_effort", config.model.reasoning_effort.c_str());
    if (config.model.has_temperature) {
        cJSON_AddNumberToObject(model, "temperature", config.model.temperature);
    }
    cJSON_AddNumberToObject(model, "max_response_tokens", config.model.max_response_tokens);
    cJSON_AddNumberToObject(model, "context_window", config.model.context_window);
    cJSON_AddNumberToObject(model, "model_max_output_tokens", config.model.model_max_output_tokens);

    cJSON* tts = add_object(root, "tts");
    cJSON_AddStringToObject(tts, "provider", config.tts.provider.c_str());
    if (include_secrets) {
        cJSON_AddStringToObject(tts, "api_key", config.tts.api_key.c_str());
    }
    cJSON_AddStringToObject(tts, "model", config.tts.model.c_str());
    cJSON_AddStringToObject(tts, "voice_id", config.tts.voice_id.c_str());
    cJSON_AddStringToObject(tts, "reference_id", config.tts.reference_id.c_str());
    cJSON_AddStringToObject(tts, "emotion", config.tts.emotion.c_str());
    cJSON_AddNumberToObject(tts, "speed", config.tts.speed);

    cJSON* stt = add_object(root, "stt");
    cJSON_AddStringToObject(stt, "provider", config.stt.provider.c_str());
    cJSON_AddStringToObject(stt, "language", config.stt.language.c_str());
    if (include_secrets) {
        cJSON_AddStringToObject(stt, "api_key", config.stt.api_key.c_str());
    }
    cJSON_AddStringToObject(stt, "model", config.stt.model.c_str());
    cJSON_AddStringToObject(stt, "base_url", config.stt.base_url.c_str());
    cJSON_AddStringToObject(stt, "app_id", config.stt.app_id.c_str());
    if (include_secrets) {
        cJSON_AddStringToObject(stt, "secret_id", config.stt.secret_id.c_str());
        cJSON_AddStringToObject(stt, "secret_key", config.stt.secret_key.c_str());
    }
    cJSON_AddStringToObject(stt, "region", config.stt.region.c_str());
    cJSON_AddStringToObject(stt, "engine_model_type", config.stt.engine_model_type.c_str());

    cJSON* audio = add_object(root, "audio");
    cJSON_AddStringToObject(audio, "socket", config.audio.socket.c_str());
    cJSON_AddNumberToObject(audio, "sample_rate", config.audio.sample_rate);
    cJSON_AddNumberToObject(audio, "channels", config.audio.channels);
    cJSON_AddNumberToObject(audio, "bit_width", config.audio.bit_width);
    cJSON_AddStringToObject(audio, "playback_backend", config.audio.playback_backend.c_str());

    cJSON* audio_archive = add_object(root, "audio_archive");
    cJSON_AddBoolToObject(audio_archive, "enabled", config.audio_archive.enabled ? 1 : 0);
    cJSON_AddStringToObject(audio_archive, "storage_path", config.audio_archive.storage_path.c_str());
    cJSON_AddNumberToObject(audio_archive, "max_files", config.audio_archive.max_files);
    cJSON_AddNumberToObject(audio_archive, "max_size_mb", config.audio_archive.max_size_mb);

    cJSON* voice_notifications = add_object(root, "voice_notifications");
    cJSON_AddBoolToObject(voice_notifications, "enabled", config.voice_notifications.enabled ? 1 : 0);
    cJSON_AddNumberToObject(voice_notifications, "max_pending", config.voice_notifications.max_pending);
    cJSON* response_tail = add_object(voice_notifications, "response_tail");
    cJSON_AddBoolToObject(response_tail, "enabled", config.voice_notifications.response_tail.enabled ? 1 : 0);
    cJSON_AddNumberToObject(response_tail, "max_items", config.voice_notifications.response_tail.max_items);
    cJSON_AddNumberToObject(response_tail, "max_text_chars", config.voice_notifications.response_tail.max_text_chars);
    cJSON* expiration = add_object(voice_notifications, "expiration");
    cJSON_AddNumberToObject(expiration, "default_ttl_seconds", config.voice_notifications.expiration.default_ttl_seconds);
    cJSON* code_ttl_seconds = add_object(expiration, "code_ttl_seconds");
    for (const auto& item : config.voice_notifications.expiration.code_ttl_seconds) {
        cJSON_AddNumberToObject(code_ttl_seconds, item.first.c_str(), item.second);
    }

    cJSON* log_config = add_object(root, "log");
    cJSON_AddNumberToObject(log_config, "llm_http_retention_days", config.log.llm_http_retention_days);

    cJSON* ota = add_object(root, "ota");
    cJSON_AddStringToObject(ota, "github_proxy_url", config.ota.github_proxy_url.c_str());

    cJSON* hid = add_object(root, "hid");
    cJSON_AddStringToObject(hid, "keyboard_device", config.hid.keyboard_device.c_str());
    cJSON_AddStringToObject(hid, "keyboard_layout", config.hid.keyboard_layout.c_str());
    cJSON_AddStringToObject(hid, "mouse_device", config.hid.mouse_device.c_str());
    cJSON_AddStringToObject(hid, "android_keyboard_device", config.hid.android_keyboard_device.c_str());
    cJSON_AddStringToObject(hid, "frame_socket", config.hid.frame_socket.c_str());
    cJSON_AddStringToObject(hid, "input_backend", normalize_input_backend(config.hid.input_backend).c_str());

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

    cJSON* termination_policy = add_object(root, "termination_policy");
    cJSON_AddBoolToObject(termination_policy, "enabled", config.termination_policy.enabled ? 1 : 0);
    cJSON_AddNumberToObject(termination_policy, "max_seconds", config.termination_policy.max_seconds);
    cJSON_AddNumberToObject(termination_policy, "repeat_action_limit", config.termination_policy.repeat_action_limit);
    cJSON_AddNumberToObject(termination_policy, "same_result_limit", config.termination_policy.same_result_limit);
    cJSON_AddNumberToObject(termination_policy, "screen_unchanged_limit", config.termination_policy.screen_unchanged_limit);
    cJSON_AddNumberToObject(termination_policy, "soft_notice_stall_score", config.termination_policy.soft_notice_stall_score);
    cJSON_AddNumberToObject(termination_policy, "restrict_tools_stall_score", config.termination_policy.restrict_tools_stall_score);
    cJSON_AddNumberToObject(termination_policy, "terminate_stall_score", config.termination_policy.terminate_stall_score);
    cJSON_AddNumberToObject(termination_policy, "parse_failure_limit", config.termination_policy.parse_failure_limit);

    cJSON* live_activity = add_object(root, "live_activity");
    cJSON_AddBoolToObject(live_activity, "enabled", config.live_activity.enabled ? 1 : 0);

    cJSON* agent = add_object(root, "agent");
    cJSON_AddStringToObject(agent, "locale", config.locale.c_str());
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
    cJSON_AddNumberToObject(agent, "voice_max_response_tokens", config.voice_max_response_tokens);
    cJSON_AddBoolToObject(agent, "load_all_tools", config.load_all_tools ? 1 : 0);
    cJSON_AddNumberToObject(agent, "max_iterations", config.max_iterations);
    cJSON_AddNumberToObject(agent, "screenshot_keep_n", config.screenshot_keep_n);
    cJSON_AddNumberToObject(agent, "screenshot_prune_interval", config.screenshot_prune_interval);
    cJSON_AddNumberToObject(agent, "screen_stable_timeout_ms", config.screen_stable_timeout_ms);
    cJSON_AddNumberToObject(agent, "screen_stable_ms", config.screen_stable_ms);
    cJSON_AddNumberToObject(agent, "screen_stable_diff_threshold", config.screen_stable_diff_threshold);
    cJSON_AddStringToObject(agent, "default_platform", config.default_platform.c_str());

    return root;
}

cJSON* wifi_to_json(const aiden::WifiNetworkConfig& wifi) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "ssid", wifi.ssid.c_str());
    cJSON_AddBoolToObject(root, "has_psk", wifi.psk.empty() ? 0 : 1);
    cJSON_AddStringToObject(root, "country", wifi.country.c_str());
    cJSON* networks = add_array(root, "networks");
    for (size_t i = 0; i < wifi.networks.size(); ++i) {
        const aiden::WifiNetwork& network = wifi.networks[i];
        cJSON* item = cJSON_CreateObject();
        cJSON_AddStringToObject(item, "ssid", network.ssid.c_str());
        cJSON_AddBoolToObject(item, "has_psk", network.psk.empty() ? 0 : 1);
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
        case 413: response.status_text = "Payload Too Large"; break;
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

void set_json_str_alias(std::string* dst,
                        cJSON* obj,
                        const char* canonical_key,
                        const char* legacy_key) {
    cJSON* canonical = cJSON_GetObjectItem(obj, canonical_key);
    if (canonical) {
        if (dst && json_is_string(canonical)) {
            *dst = canonical->valuestring;
        }
        return;
    }
    set_json_str(dst, obj, legacy_key);
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

bool config_metadata_string_in_rule_matches(
        const ConfigMetadataSchema& metadata,
        const std::string& section,
        const std::string& field,
        const std::string& dependency,
        const std::string& value) {
    std::map<std::string, std::map<std::string, ConfigMetadataSchema::StringInRule> >::const_iterator
        section_rules = metadata.string_in_rules.find(section);
    if (section_rules == metadata.string_in_rules.end()) return false;
    std::map<std::string, ConfigMetadataSchema::StringInRule>::const_iterator rule_it =
        section_rules->second.find(field);
    if (rule_it == section_rules->second.end()) return false;
    const ConfigMetadataSchema::StringInRule& rule = rule_it->second;
    return rule.field == dependency &&
        std::find(rule.values.begin(), rule.values.end(), value) != rule.values.end();
}

bool model_provider_base_url_allowed(const std::string& provider) {
    const ConfigMetadataSchema* metadata = NULL;
    if (!load_agent_config_metadata(&metadata, NULL) || !metadata) return false;
    return config_metadata_string_in_rule_matches(
        *metadata, "model_providers", "base_url", "model_providers.type",
        lowercase_copy(trim_copy(provider)));
}

std::string provider_secret_source_name(cJSON* root, const char* section,
                                        const std::string& submitted_name) {
    cJSON* renames = cJSON_GetObjectItem(root, "_provider_renames");
    if (!json_is_object(renames)) return submitted_name;
    cJSON* section_renames = cJSON_GetObjectItem(renames, section);
    if (!json_is_object(section_renames)) return submitted_name;
    cJSON* old_name = cJSON_GetObjectItem(section_renames, submitted_name.c_str());
    if (!json_is_string(old_name) || !old_name->valuestring || !old_name->valuestring[0]) {
        return submitted_name;
    }
    return old_name->valuestring;
}

void update_write_only_api_key(cJSON* record, const std::string& previous_api_key,
                               std::string* api_key) {
    if (!record || !api_key) return;

    cJSON* submitted_api_key = cJSON_GetObjectItem(record, "api_key");
    if (json_is_string(submitted_api_key) && submitted_api_key->valuestring[0]) {
        const std::string value = submitted_api_key->valuestring;
        *api_key = !previous_api_key.empty() && value == mask_secret(previous_api_key)
            ? previous_api_key
            : value;
        return;
    }

    *api_key = previous_api_key;
}

void update_write_only_secret(cJSON* record, const char* field,
                              const std::string& previous, std::string* value) {
    if (!record || !field || !value) return;
    cJSON* submitted = cJSON_GetObjectItem(record, field);
    if (!json_is_string(submitted) || !submitted->valuestring[0]) {
        *value = previous;
        return;
    }
    const std::string submitted_value = submitted->valuestring;
    *value = !previous.empty() && submitted_value == mask_secret(previous)
        ? previous
        : submitted_value;
}

void update_model_from_json(cJSON* obj, aiden::ModelToml* m) {
    if (!json_is_object(obj) || !m) return;
    set_json_str(&m->provider, obj, "provider");
    set_json_str(&m->model, obj, "model");
    m->base_url.clear();
    set_json_str(&m->api_key, obj, "api_key");
    set_json_str(&m->api_mode, obj, "api_mode");
    set_json_str(&m->reasoning_effort, obj, "reasoning_effort");
    // Temperature is nullable: presence of the key sets has_temperature, and
    // its absence clears it. This function applies JSON as a patch onto an
    // existing config (see handle_post_config), so an omitted key must unset the
    // value rather than leave a stale one, letting the UI clear temperature by
    // omitting it.
    cJSON* temp_item = cJSON_GetObjectItem(obj, "temperature");
    if (temp_item && json_is_number(temp_item)) {
        m->temperature = temp_item->valuedouble;
        m->has_temperature = true;
    } else {
        m->has_temperature = false;
    }
    set_json_int(&m->max_response_tokens, obj, "max_response_tokens");
    set_json_int(&m->context_window, obj, "context_window");
    set_json_int(&m->model_max_output_tokens, obj, "model_max_output_tokens");

    // base_url belongs to [model_providers.*], not the flat [model] section.
}

void move_model_api_key_to_provider(aiden::AgentToml* config, bool submitted_api_key) {
    if (!config) return;
    if (!submitted_api_key) {
        // load_current_agent_config returns the runtime-resolved credential on
        // model.api_key. It belongs to the previously resolved provider and
        // must never be persisted or assigned after a provider switch.
        config->model.api_key.clear();
        return;
    }
    if (config->model.api_key.empty()) return;

    const std::string provider_name = trim_copy(config->model.provider);
    if (provider_name.empty()) return;

    aiden::ModelProviderToml& provider = config->model_providers[provider_name];
    if (provider.type.empty()) {
        provider.type = provider_name;
    }
    provider.api_key = config->model.api_key;
    config->model.api_key.clear();
}

void update_config_from_json(cJSON* root, aiden::AgentToml* config) {
    if (!root || !config) {
        return;
    }

    // Provider maps are authoritative for record membership. Credentials are
    // write-only: omitted or empty values preserve the stored record, including
    // across a rename identified by _provider_renames.
    cJSON* model_providers = cJSON_GetObjectItem(root, "model_providers");
    if (json_is_object(model_providers)) {
        const std::map<std::string, aiden::ModelProviderToml> previous_providers =
            config->model_providers;
        config->model_providers.clear();

        for (cJSON* item = model_providers->child; item; item = item->next) {
            if (!item->string) continue;

            std::string provider_name = item->string;
            if (!json_is_object(item)) continue;

            aiden::ModelProviderToml provider;
            set_json_str_alias(&provider.type, item, "type", "provider");
            const std::string secret_source = provider_secret_source_name(
                root, "model_providers", provider_name);
            std::map<std::string, aiden::ModelProviderToml>::const_iterator previous =
                previous_providers.find(secret_source);
            update_write_only_api_key(
                item,
                previous == previous_providers.end() ? std::string() : previous->second.api_key,
                &provider.api_key);

            cJSON* base_url = cJSON_GetObjectItem(item, "base_url");
            // A named provider's base_url is inherited by every model that
            // references it (applyProviderRef), where the runtime then drops it
            // for types that pin their endpoint. Applying the metadata-backed
            // capability rule here keeps agent.toml free of dead config.
            if (json_is_string(base_url) && model_provider_base_url_allowed(provider.type)) {
                provider.base_url = base_url->valuestring;
            }

            config->model_providers[provider_name] = provider;
        }
    }

    // Update tts_providers. Same contract as providers above: the payload is
    // authoritative, so a record absent from it was deleted in the UI -- but an
    // ABSENT KEY means "not edited" and must leave the stored records alone.
    // Clearing unconditionally is exactly the bug that erased every [model_providers.*]
    // whenever an unrelated section was saved.
    cJSON* tts_providers = cJSON_GetObjectItem(root, "tts_providers");
    if (json_is_object(tts_providers)) {
        const std::map<std::string, aiden::TTSProviderToml> previous = config->tts_providers;
        config->tts_providers.clear();

        for (cJSON* item = tts_providers->child; item; item = item->next) {
            if (!item->string) continue;
            if (!json_is_object(item)) continue;
            const std::string record_name = item->string;

            aiden::TTSProviderToml record;
            set_json_str_alias(&record.type, item, "type", "provider");
            set_json_str(&record.model, item, "model");
            set_json_str(&record.voice_id, item, "voice_id");
            set_json_str(&record.emotion, item, "emotion");
            set_json_str(&record.reference_id, item, "reference_id");

            const std::string secret_source = provider_secret_source_name(
                root, "tts_providers", record_name);
            std::map<std::string, aiden::TTSProviderToml>::const_iterator stored =
                previous.find(secret_source);
            update_write_only_api_key(
                item,
                stored == previous.end() ? std::string() : stored->second.api_key,
                &record.api_key);

            config->tts_providers[record_name] = record;
        }
    }

    cJSON* stt_providers = cJSON_GetObjectItem(root, "stt_providers");
    if (json_is_object(stt_providers)) {
        const std::map<std::string, aiden::STTProviderToml> previous = config->stt_providers;
        config->stt_providers.clear();

        for (cJSON* item = stt_providers->child; item; item = item->next) {
            if (!item->string) continue;
            if (!json_is_object(item)) continue;
            const std::string record_name = item->string;
            const std::string secret_source = provider_secret_source_name(
                root, "stt_providers", record_name);

            aiden::STTProviderToml record;
            set_json_str_alias(&record.type, item, "type", "provider");
            set_json_str(&record.model, item, "model");
            set_json_str(&record.base_url, item, "base_url");
            set_json_str(&record.app_id, item, "app_id");
            set_json_str(&record.region, item, "region");
            set_json_str(&record.engine_model_type, item, "engine_model_type");

            std::map<std::string, aiden::STTProviderToml>::const_iterator stored =
                previous.find(secret_source);
            const aiden::STTProviderToml* stored_record =
                stored == previous.end() ? NULL : &stored->second;
            update_write_only_api_key(
                item,
                stored_record ? stored_record->api_key : std::string(),
                &record.api_key);
            update_write_only_secret(item, "secret_id",
                                     stored_record ? stored_record->secret_id : std::string(),
                                     &record.secret_id);
            update_write_only_secret(item, "secret_key",
                                     stored_record ? stored_record->secret_key : std::string(),
                                     &record.secret_key);

            config->stt_providers[record_name] = record;
        }
    }

    update_model_from_json(cJSON_GetObjectItem(root, "model"), &config->model);

    cJSON* tts = cJSON_GetObjectItem(root, "tts");
    if (json_is_object(tts)) {
        set_json_str(&config->tts.provider, tts, "provider");
        set_json_str(&config->tts.api_key, tts, "api_key");
        set_json_str(&config->tts.model, tts, "model");
        set_json_str(&config->tts.voice_id, tts, "voice_id");
        set_json_str(&config->tts.reference_id, tts, "reference_id");
        set_json_str(&config->tts.emotion, tts, "emotion");
        set_json_double(&config->tts.speed, tts, "speed");
    }

    cJSON* stt = cJSON_GetObjectItem(root, "stt");
    if (json_is_object(stt)) {
        set_json_str(&config->stt.provider, stt, "provider");
        set_json_str(&config->stt.language, stt, "language");
        set_json_str(&config->stt.api_key, stt, "api_key");
        set_json_str(&config->stt.model, stt, "model");
        set_json_str(&config->stt.base_url, stt, "base_url");
        set_json_str(&config->stt.app_id, stt, "app_id");
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
        set_json_str(&config->audio.playback_backend, audio, "playback_backend");
    }

    cJSON* audio_archive = cJSON_GetObjectItem(root, "audio_archive");
    if (json_is_object(audio_archive)) {
        set_json_bool(&config->audio_archive.enabled, audio_archive, "enabled");
        set_json_str(&config->audio_archive.storage_path, audio_archive, "storage_path");
        set_json_int(&config->audio_archive.max_files, audio_archive, "max_files");
        set_json_int(&config->audio_archive.max_size_mb, audio_archive, "max_size_mb");
    }

    cJSON* voice_notifications = cJSON_GetObjectItem(root, "voice_notifications");
    if (json_is_object(voice_notifications)) {
        set_json_bool(&config->voice_notifications.enabled, voice_notifications, "enabled");
        set_json_int(&config->voice_notifications.max_pending, voice_notifications, "max_pending");

        cJSON* response_tail = cJSON_GetObjectItem(voice_notifications, "response_tail");
        if (json_is_object(response_tail)) {
            set_json_bool(&config->voice_notifications.response_tail.enabled, response_tail, "enabled");
            set_json_int(&config->voice_notifications.response_tail.max_items, response_tail, "max_items");
            set_json_int(&config->voice_notifications.response_tail.max_text_chars, response_tail, "max_text_chars");
        }

        cJSON* expiration = cJSON_GetObjectItem(voice_notifications, "expiration");
        if (json_is_object(expiration)) {
            set_json_int(&config->voice_notifications.expiration.default_ttl_seconds, expiration, "default_ttl_seconds");
            cJSON* code_ttl_seconds = cJSON_GetObjectItem(expiration, "code_ttl_seconds");
            if (json_is_object(code_ttl_seconds)) {
                config->voice_notifications.expiration.code_ttl_seconds.clear();
                for (cJSON* item = code_ttl_seconds->child; item; item = item->next) {
                    if (json_is_number(item) && item->string) {
                        config->voice_notifications.expiration.code_ttl_seconds[item->string] = item->valueint;
                    }
                }
            }
        }
    }

    cJSON* log_config = cJSON_GetObjectItem(root, "log");
    if (json_is_object(log_config)) {
        set_json_int(&config->log.llm_http_retention_days, log_config, "llm_http_retention_days");
    }

    cJSON* ota = cJSON_GetObjectItem(root, "ota");
    if (json_is_object(ota)) {
        set_json_str(&config->ota.github_proxy_url, ota, "github_proxy_url");
    }

    cJSON* device = cJSON_GetObjectItem(root, "device");
    if (json_is_object(device)) {
        set_json_str(&config->device.backend, device, "backend");
        set_json_str(&config->device.device_type, device, "device_type");
    }

    cJSON* hid = cJSON_GetObjectItem(root, "hid");
    if (json_is_object(hid)) {
        set_json_str(&config->hid.keyboard_device, hid, "keyboard_device");
        set_json_str(&config->hid.keyboard_layout, hid, "keyboard_layout");
        set_json_str(&config->hid.mouse_device, hid, "mouse_device");
        set_json_str(&config->hid.android_keyboard_device, hid, "android_keyboard_device");
        set_json_str(&config->hid.frame_socket, hid, "frame_socket");
        set_json_str(&config->hid.input_backend, hid, "input_backend");
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

    cJSON* termination_policy = cJSON_GetObjectItem(root, "termination_policy");
    if (json_is_object(termination_policy)) {
        set_json_bool(&config->termination_policy.enabled, termination_policy, "enabled");
        set_json_double(&config->termination_policy.max_seconds, termination_policy, "max_seconds");
        set_json_int(&config->termination_policy.repeat_action_limit, termination_policy, "repeat_action_limit");
        set_json_int(&config->termination_policy.same_result_limit, termination_policy, "same_result_limit");
        set_json_int(&config->termination_policy.screen_unchanged_limit, termination_policy, "screen_unchanged_limit");
        set_json_int(&config->termination_policy.soft_notice_stall_score, termination_policy, "soft_notice_stall_score");
        set_json_int(&config->termination_policy.restrict_tools_stall_score, termination_policy, "restrict_tools_stall_score");
        set_json_int(&config->termination_policy.terminate_stall_score, termination_policy, "terminate_stall_score");
        set_json_int(&config->termination_policy.parse_failure_limit, termination_policy, "parse_failure_limit");
    }

    cJSON* live_activity = cJSON_GetObjectItem(root, "live_activity");
    if (json_is_object(live_activity)) {
        set_json_bool(&config->live_activity.enabled, live_activity, "enabled");
    }

    cJSON* agent = cJSON_GetObjectItem(root, "agent");
    if (json_is_object(agent)) {
        set_json_str(&config->locale, agent, "locale");
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
        set_json_int(&config->voice_max_response_tokens, agent, "voice_max_response_tokens");
        set_json_bool(&config->load_all_tools, agent, "load_all_tools");
        set_json_int(&config->max_iterations, agent, "max_iterations");
        set_json_int(&config->screenshot_keep_n, agent, "screenshot_keep_n");
        set_json_int(&config->screenshot_prune_interval, agent, "screenshot_prune_interval");
        set_json_int(&config->screen_stable_timeout_ms, agent, "screen_stable_timeout_ms");
        set_json_int(&config->screen_stable_ms, agent, "screen_stable_ms");
        set_json_double(&config->screen_stable_diff_threshold, agent, "screen_stable_diff_threshold");
        set_json_str(&config->default_platform, agent, "default_platform");
    }
}

ValidationResult parse_agent_config_validation_result(const CommandResult& result) {
    if (result.timed_out) {
        return ValidationResult::unavailable("config validation timed out");
    }

    cJSON* response = cJSON_Parse(result.output.c_str());
    if (!response) {
        // CLI produced unparseable output. We cannot tell if the config is
        // valid; fail closed but as UNAVAILABLE so the UI distinguishes a
        // broken validator from a rejected user input.
        AIDEN_LOG_ERROR("agent_config", "config_check_invalid_json", "output=%s",
                        result.output.c_str());
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
    std::string cmd = shell_quote(agent_bin_path) + " config-check --stdin --format=json";
    return parse_agent_config_validation_result(run_command_with_stdin(cmd, input, 2000));
}

ValidationResult validate_agent_config_path_via_cli(const std::string& config_path,
                                                     const char* agent_bin_path) {
    std::string cmd = shell_quote(agent_bin_path) + " config-check --config="
                    + shell_quote(config_path) + " --format=json";
    return parse_agent_config_validation_result(run_command_with_stdin(cmd, "", 2000));
}

ValidationResult validate_agent_config_for_save(const aiden::AgentToml& config) {
    // The agent CLI's `config-check` is the single source of truth for config
    // validation. If the binary is unavailable, fail closed: reject the save
    // rather than persist a config we cannot validate.
    const char* agent_bin = agent_bin_path();
    if (!file_exists(agent_bin)) {
        AIDEN_LOG_ERROR("agent_config", "binary_not_found",
                        "path=%s operation=validate_json_save", agent_bin);
        return ValidationResult::unavailable(
                "config validation unavailable: agent binary not found");
    }
    return validate_agent_config_via_cli(config, agent_bin);
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
        aiden::sync_legacy_wifi_fields(wifi);
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

// Poll until the interface has obtained an IPv4 address (DHCP lease), up to
// max_seconds. `dhcpcd -n` only signals the running daemon and returns
// immediately, so the lease is not yet in place when it exits; without this
// wait the connect-confirmation check sees an empty ip_address and rolls back
// a connection that would have succeeded a second or two later.
bool wait_for_ip(const Options& options, std::ostringstream& log, int max_seconds) {
    for (int i = 0; i < max_seconds; ++i) {
        if (command_exists("wpa_cli")) {
            CommandResult status = run_shell_command(
                "wpa_cli -i " + shell_quote(options.wifi_interface) + " status 2>&1");
            if (status.exit_code == 0) {
                std::string ip = find_key_value_line(status.output, "ip_address");
                if (!ip.empty()) {
                    log << "$ wpa_cli status -> ip_address=" << ip << " after "
                        << (i + 1) << "s\n";
                    return true;
                }
            }
        }
        if (command_exists("ifconfig")) {
            CommandResult status = run_shell_command(
                "ifconfig " + shell_quote(options.wifi_interface) + " 2>&1");
            std::string ip = extract_ifconfig_ipv4(status.output);
            if (!ip.empty()) {
                log << "$ ifconfig " << options.wifi_interface << " -> inet " << ip
                    << " after " << (i + 1) << "s\n";
                return true;
            }
        }
        sleep(1);
    }
    log << "$ no IPv4 address obtained within " << max_seconds << "s\n";
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

long long monotonic_millis() {
    struct timespec now;
    if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) {
        return static_cast<long long>(time(NULL)) * 1000LL;
    }
    return static_cast<long long>(now.tv_sec) * 1000LL + now.tv_nsec / 1000000LL;
}

bool reap_agent_restart_process(bool wait, std::string* error) {
    if (error) error->clear();
    if (g_agent_restart_pid <= 0) {
        return true;
    }

    const long long deadline = monotonic_millis() + kAgentRestartWaitTimeoutMs;
    while (true) {
        int status = 0;
        pid_t result = waitpid(g_agent_restart_pid, &status, WNOHANG);
        if (result == g_agent_restart_pid) {
            g_agent_restart_pid = -1;
            return true;
        }
        if (result < 0) {
            if (errno == EINTR) {
                continue;
            }
            if (errno == ECHILD) {
                g_agent_restart_pid = -1;
                return true;
            }
            if (error) *error = std::string("wait for agent restart: ") + strerror(errno);
            return false;
        }
        if (!wait) {
            return true;
        }
        if (monotonic_millis() >= deadline) {
            if (error) *error = "timed out waiting for agent restart";
            return false;
        }
        usleep(kAgentRestartPollIntervalMs * 1000);
    }
}

bool launch_agent_restart(std::string* error) {
    if (error) error->clear();
    const char* init_script = agent_init_script_path();
    if (!init_script || init_script[0] == '\0') {
        if (error) *error = "agent init script path is empty";
        return false;
    }

    pid_t pid = fork();
    if (pid < 0) {
        if (error) *error = std::string("fork agent restart: ") + strerror(errno);
        return false;
    }
    if (pid == 0) {
        int devnull = open("/dev/null", O_RDWR);
        if (devnull >= 0) {
            dup2(devnull, STDIN_FILENO);
            dup2(devnull, STDOUT_FILENO);
            dup2(devnull, STDERR_FILENO);
            if (devnull > STDERR_FILENO) close(devnull);
        }
        execl(init_script, init_script, "restart", static_cast<char*>(NULL));
        execl("/bin/sh", "sh", init_script, "restart", static_cast<char*>(NULL));
        _exit(127);
    }

    g_agent_restart_pid = pid;
    g_agent_restart_readiness_pending = true;
    return true;
}

void start_deferred_agent_restart_if_idle() {
    std::string ignored_error;
    (void)reap_agent_restart_process(false, &ignored_error);
    if (!g_stt_live_test_active && g_agent_restart_deferred && g_agent_restart_pid <= 0) {
        g_agent_restart_deferred = false;
        if (!launch_agent_restart(&ignored_error)) {
            AIDEN_LOG_ERROR("agent_restart", "launch_failed", "error=%s",
                            ignored_error.c_str());
        }
    }
}

void schedule_agent_restart() {
    if (g_stt_live_test_active) {
        g_agent_restart_deferred = true;
        g_agent_restart_readiness_pending = true;
        return;
    }

    std::string ignored_error;
    (void)reap_agent_restart_process(false, &ignored_error);
    if (g_agent_restart_pid > 0) {
        g_agent_restart_deferred = true;
        g_agent_restart_readiness_pending = true;
        return;
    }
    if (!launch_agent_restart(&ignored_error)) {
        AIDEN_LOG_ERROR("agent_restart", "launch_failed", "error=%s",
                        ignored_error.c_str());
    }
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
        "rc=$?; level=INFO; [ \"$rc\" -eq 0 ] || level=ERROR; "
        "printf '%s [%s] [config_web] [ota] update_exited exit_code=%s\\n' "
        "\"$(date -u '+%Y-%m-%dT%H:%M:%SZ')\" \"$level\" \"$rc\"; exit $rc"
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

bool schedule_ota_update(unsigned long long* start_size_bytes, std::string* error) {
    if (start_size_bytes) {
        *start_size_bytes = 0;
    }
    int lock_fd = acquire_ota_update_launch_lock(error);
    if (lock_fd < 0) {
        return false;
    }
    std::string log_path = ota_update_log_path();
    unsigned long long prepared_start_size = 0;
    if (!prepare_ota_update_log_file(log_path, kOtaWebUpdateLogMaxBytes, &prepared_start_size, error)) {
        close(lock_fd);
        return false;
    }
    if (!launch_ota_update_supervisor(lock_fd, log_path, error)) {
        close(lock_fd);
        return false;
    }
    close(lock_fd);
    if (start_size_bytes) {
        *start_size_bytes = prepared_start_size;
    }
    return true;
}

bool schedule_reboot(std::string* error) {
    CommandResult lookup = run_shell_command("PATH=/sbin:/bin:/usr/sbin:/usr/bin; command -v reboot >/dev/null 2>&1");
    if (lookup.exit_code != 0) {
        if (error) {
            *error = "reboot command not found";
        }
        return false;
    }

    int rc = system("(PATH=/sbin:/bin:/usr/sbin:/usr/bin; sync; sleep 1; reboot) >/dev/null 2>&1 &");
    if (rc == -1) {
        if (error) {
            *error = std::string("launch reboot command: ") + strerror(errno);
        }
        return false;
    }
    if (!WIFEXITED(rc) || WEXITSTATUS(rc) != 0) {
        if (error) {
            std::ostringstream msg;
            msg << "launch reboot command failed";
            if (WIFEXITED(rc)) {
                msg << " (exit " << WEXITSTATUS(rc) << ")";
            } else if (WIFSIGNALED(rc)) {
                msg << " (signal " << WTERMSIG(rc) << ")";
            }
            *error = msg.str();
        }
        return false;
    }
    return true;
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
            // `dhcpcd -n` only signals the running daemon and returns
            // immediately, so its exit code says nothing about whether a lease
            // was actually obtained. Wait for the IPv4 address to appear.
            dhcp_ok = dhcp.exit_code == 0 && wait_for_ip(options, log, 15);
            if (!dhcp_ok) {
                result.exit_code = dhcp.exit_code != 0 ? dhcp.exit_code : 1;
            }
        } else if (command_exists("dhclient")) {
            CommandResult dhcp = run_shell_command("dhclient " + shell_quote(options.wifi_interface) + " 2>&1");
            log << "$ dhclient " << options.wifi_interface << "\n" << dhcp.output;
            dhcp_ok = dhcp.exit_code == 0 && wait_for_ip(options, log, 15);
            if (!dhcp_ok) {
                result.exit_code = dhcp.exit_code != 0 ? dhcp.exit_code : 1;
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
            // wpa_supplicant is not wired to DHCP on this device (dhcpcd runs
            // separately), so `wpa_cli status` never reports ip_address=. Fall
            // back to ifconfig for the IPv4 lease so the connect-confirmation
            // gate and the UI status see the same address wait_for_ip() trusts.
            if (status.ip_address.empty() && status.connected && command_exists("ifconfig")) {
                CommandResult ifc = run_shell_command(
                    "ifconfig " + shell_quote(options.wifi_interface) + " 2>&1");
                status.ip_address = extract_ifconfig_ipv4(ifc.output);
            }
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

bool read_file_contents_checked(const char* path, size_t max_size, std::string* contents, std::string* error) {
    if (contents) {
        contents->clear();
    }
    if (!path) {
        if (error) *error = "path is null";
        return false;
    }
    FILE* f = fopen(path, "r");
    if (!f) {
        if (error) *error = std::string("open ") + path + ": " + strerror(errno);
        return false;
    }
    if (fseek(f, 0, SEEK_END) != 0) {
        if (error) *error = std::string("seek ") + path + ": " + strerror(errno);
        fclose(f);
        return false;
    }
    long file_size = ftell(f);
    if (file_size < 0) {
        if (error) *error = std::string("tell ") + path + ": " + strerror(errno);
        fclose(f);
        return false;
    }
    if (static_cast<size_t>(file_size) > max_size) {
        if (error) {
            *error = std::string("file too large: ") + path + " (" +
                     std::to_string(file_size) + " bytes)";
        }
        fclose(f);
        return false;
    }
    if (fseek(f, 0, SEEK_SET) != 0) {
        if (error) *error = std::string("seek ") + path + ": " + strerror(errno);
        fclose(f);
        return false;
    }
    std::string result;
    result.reserve(static_cast<size_t>(file_size));
    char buf[1024];
    while (size_t n = fread(buf, 1, sizeof(buf), f)) {
        result.append(buf, n);
    }
    if (ferror(f)) {
        if (error) *error = std::string("read ") + path + ": " + strerror(errno);
        fclose(f);
        return false;
    }
    if (fclose(f) != 0) {
        if (error) *error = std::string("close ") + path + ": " + strerror(errno);
        return false;
    }
    if (contents) {
        *contents = result;
    }
    if (error) {
        error->clear();
    }
    return true;
}

std::string read_file_contents(const char* path, size_t max_size = 64 * 1024) {
    std::string contents;
    std::string error;
    if (!read_file_contents_checked(path, max_size, &contents, &error)) {
        return "";
    }
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

bool prepare_ota_update_log_file(const std::string& path,
                                 size_t max_size,
                                 unsigned long long* start_size_bytes,
                                 std::string* error) {
    if (start_size_bytes) {
        *start_size_bytes = 0;
    }
    if (!mkdir_p(parent_dir(path), error)) {
        return false;
    }
    int fd = open(path.c_str(), O_WRONLY | O_CREAT, 0644);
    if (fd < 0) {
        if (error) *error = "open " + path + ": " + strerror(errno);
        return false;
    }
    struct stat st;
    if (fstat(fd, &st) != 0) {
        if (error) *error = "stat " + path + ": " + strerror(errno);
        close(fd);
        return false;
    }
    if (st.st_size < 0) {
        if (error) *error = "stat " + path + ": negative file size";
        close(fd);
        return false;
    }
    unsigned long long size_bytes = static_cast<unsigned long long>(st.st_size);
    if (size_bytes >= static_cast<unsigned long long>(max_size)) {
        if (ftruncate(fd, 0) != 0) {
            if (error) *error = "truncate " + path + ": " + strerror(errno);
            close(fd);
            return false;
        }
        size_bytes = 0;
    }
    if (close(fd) != 0) {
        if (error) *error = "close " + path + ": " + strerror(errno);
        return false;
    }
    if (start_size_bytes) {
        *start_size_bytes = size_bytes;
    }
    return true;
}

bool create_temp_file_in_dir(const std::string& dir,
                             const std::string& prefix,
                             mode_t mode,
                             int* fd,
                             std::string* path,
                             std::string* error) {
    if (fd) {
        *fd = -1;
    }
    if (path) {
        path->clear();
    }
    if (!mkdir_p(dir, error)) {
        return false;
    }

    std::string tmpl = dir;
    if (tmpl.empty() || tmpl[tmpl.size() - 1] != '/') {
        tmpl += "/";
    }
    tmpl += ".";
    tmpl += prefix.empty() ? "tmp-" : prefix;
    tmpl += "XXXXXX";

    std::vector<char> buf(tmpl.begin(), tmpl.end());
    buf.push_back('\0');
    int tmp_fd = mkstemp(buf.data());
    if (tmp_fd < 0) {
        if (error) *error = "mkstemp " + tmpl + ": " + strerror(errno);
        return false;
    }
    std::string cloexec_error;
    if (!set_fd_cloexec(tmp_fd, &cloexec_error)) {
        if (error) *error = cloexec_error;
        close(tmp_fd);
        unlink(buf.data());
        return false;
    }
    if (fchmod(tmp_fd, mode) != 0) {
        if (error) *error = "chmod " + std::string(buf.data()) + ": " + strerror(errno);
        close(tmp_fd);
        unlink(buf.data());
        return false;
    }
    if (fd) {
        *fd = tmp_fd;
    }
    if (path) {
        *path = buf.data();
    }
    return true;
}

bool create_temp_dir_in_dir(const std::string& dir,
                            const std::string& prefix,
                            std::string* path,
                            std::string* error) {
    if (path) {
        path->clear();
    }
    if (!mkdir_p(dir, error)) {
        return false;
    }

    std::string tmpl = dir;
    if (tmpl.empty() || tmpl[tmpl.size() - 1] != '/') {
        tmpl += "/";
    }
    tmpl += ".";
    tmpl += prefix.empty() ? "tmp-" : prefix;
    tmpl += "XXXXXX";

    std::vector<char> buf(tmpl.begin(), tmpl.end());
    buf.push_back('\0');
    char* created = mkdtemp(buf.data());
    if (!created) {
        if (error) *error = "mkdtemp " + tmpl + ": " + strerror(errno);
        return false;
    }
    if (path) {
        *path = created;
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

size_t find_toml_line_comment(const std::string& line, size_t start) {
    bool in_basic_string = false;
    bool in_literal_string = false;
    bool escaped = false;
    for (size_t i = start; i < line.size(); ++i) {
        const char ch = line[i];
        if (in_basic_string) {
            if (escaped) {
                escaped = false;
            } else if (ch == '\\') {
                escaped = true;
            } else if (ch == '"') {
                in_basic_string = false;
            }
            continue;
        }
        if (in_literal_string) {
            if (ch == '\'') {
                in_literal_string = false;
            }
            continue;
        }
        if (ch == '"') {
            in_basic_string = true;
        } else if (ch == '\'') {
            in_literal_string = true;
        } else if (ch == '#') {
            return i;
        }
    }
    return std::string::npos;
}

bool find_top_level_locale_assignment(const std::string& line, size_t* equals_offset) {
    size_t pos = 0;
    while (pos < line.size() && (line[pos] == ' ' || line[pos] == '\t')) {
        ++pos;
    }
    size_t key_size = 0;
    if (line.compare(pos, 6, "locale") == 0) {
        key_size = 6;
    } else if (line.compare(pos, 8, "\"locale\"") == 0 ||
               line.compare(pos, 8, "'locale'") == 0) {
        key_size = 8;
    } else {
        return false;
    }
    pos += key_size;
    while (pos < line.size() && (line[pos] == ' ' || line[pos] == '\t')) {
        ++pos;
    }
    if (pos >= line.size() || line[pos] != '=') {
        return false;
    }
    if (equals_offset) {
        *equals_offset = pos;
    }
    return true;
}

void update_toml_multiline_string_state(const std::string& line,
                                        bool* in_multiline_basic,
                                        bool* in_multiline_literal) {
    bool in_basic = false;
    bool in_literal = false;
    bool escaped = false;
    for (size_t i = 0; i < line.size();) {
        if (*in_multiline_basic) {
            if (i + 2 < line.size() && line.compare(i, 3, "\"\"\"") == 0 && !escaped) {
                *in_multiline_basic = false;
                i += 3;
                continue;
            }
            if (line[i] == '\\' && !escaped) {
                escaped = true;
            } else {
                escaped = false;
            }
            ++i;
            continue;
        }
        if (*in_multiline_literal) {
            if (i + 2 < line.size() && line.compare(i, 3, "'''") == 0) {
                *in_multiline_literal = false;
                i += 3;
            } else {
                ++i;
            }
            continue;
        }
        if (in_basic) {
            if (escaped) {
                escaped = false;
            } else if (line[i] == '\\') {
                escaped = true;
            } else if (line[i] == '"') {
                in_basic = false;
            }
            ++i;
            continue;
        }
        if (in_literal) {
            if (line[i] == '\'') {
                in_literal = false;
            }
            ++i;
            continue;
        }
        if (line[i] == '#') {
            break;
        }
        if (i + 2 < line.size() && line.compare(i, 3, "\"\"\"") == 0) {
            *in_multiline_basic = true;
            escaped = false;
            i += 3;
        } else if (i + 2 < line.size() && line.compare(i, 3, "'''") == 0) {
            *in_multiline_literal = true;
            i += 3;
        } else if (line[i] == '"') {
            in_basic = true;
            escaped = false;
            ++i;
        } else if (line[i] == '\'') {
            in_literal = true;
            ++i;
        } else {
            ++i;
        }
    }
}

std::string update_top_level_locale(const std::string& content, const std::string& locale) {
    size_t first_section_offset = std::string::npos;
    size_t line_start = 0;
    bool in_multiline_basic = false;
    bool in_multiline_literal = false;
    while (line_start < content.size()) {
        size_t line_end = content.find('\n', line_start);
        if (line_end == std::string::npos) {
            line_end = content.size();
        }
        size_t logical_end = line_end;
        if (logical_end > line_start && content[logical_end - 1] == '\r') {
            --logical_end;
        }
        const std::string line = content.substr(line_start, logical_end - line_start);

        const bool line_starts_inside_multiline = in_multiline_basic || in_multiline_literal;
        update_toml_multiline_string_state(line, &in_multiline_basic, &in_multiline_literal);
        if (line_starts_inside_multiline) {
            if (line_end == content.size()) {
                break;
            }
            line_start = line_end + 1;
            continue;
        }

        size_t first_non_space = 0;
        while (first_non_space < line.size() &&
               (line[first_non_space] == ' ' || line[first_non_space] == '\t')) {
            ++first_non_space;
        }
        if (first_non_space < line.size() && line[first_non_space] == '[') {
            first_section_offset = line_start;
            break;
        }

        size_t equals_offset = 0;
        if (find_top_level_locale_assignment(line, &equals_offset)) {
            size_t value_start = equals_offset + 1;
            while (value_start < line.size() &&
                   (line[value_start] == ' ' || line[value_start] == '\t')) {
                ++value_start;
            }
            size_t comment_offset = find_toml_line_comment(line, value_start);
            size_t value_end = comment_offset == std::string::npos ? line.size() : comment_offset;
            while (value_end > value_start &&
                   (line[value_end - 1] == ' ' || line[value_end - 1] == '\t')) {
                --value_end;
            }
            const size_t absolute_value_start = line_start + value_start;
            const size_t absolute_value_end = line_start + value_end;
            return content.substr(0, absolute_value_start) + "\"" + locale + "\""
                 + content.substr(absolute_value_end);
        }

        if (line_end == content.size()) {
            break;
        }
        line_start = line_end + 1;
    }

    const size_t insert_offset = first_section_offset == std::string::npos
        ? content.size() : first_section_offset;
    std::string insertion;
    if (insert_offset > 0 && content[insert_offset - 1] != '\n') {
        insertion += "\n";
    }
    insertion += "locale = \"" + locale + "\"\n";
    return content.substr(0, insert_offset) + insertion + content.substr(insert_offset);
}

ValidationResult validate_agent_toml_content_for_save(const std::string& content,
                                                       const std::string& config_path) {
    const char* agent_bin = agent_bin_path();
    if (!file_exists(agent_bin)) {
        AIDEN_LOG_ERROR("agent_config", "binary_not_found",
                        "path=%s operation=validate_toml_save", agent_bin);
        return ValidationResult::unavailable(
                "config validation unavailable: agent binary not found");
    }

    std::string temp_dir;
    std::string error;
    std::string validation_dir = parent_dir(config_path);
    if (validation_dir.empty()) {
        validation_dir = ".";
    }
    if (!create_temp_dir_in_dir(validation_dir, "agent-locale-check-", &temp_dir, &error)) {
        return ValidationResult::unavailable("config validation unavailable: " + error);
    }
    const std::string temp_path = temp_dir + "/agent.toml";
    if (!atomic_write_file(temp_path, content, 0600, &error)) {
        rmdir(temp_dir.c_str());
        return ValidationResult::unavailable("config validation unavailable: " + error);
    }

    ValidationResult result = validate_agent_config_path_via_cli(temp_path, agent_bin);
    unlink(temp_path.c_str());
    rmdir(temp_dir.c_str());
    return result;
}

bool copy_regular_file_tail(const std::string& source,
                            const std::string& destination,
                            size_t max_bytes,
                            mode_t mode,
                            std::string* error) {
    if (max_bytes == 0) {
        if (error) *error = "max copy size is zero";
        return false;
    }

    int in = open(source.c_str(), O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (in < 0) {
        if (error) *error = "open " + source + ": " + strerror(errno);
        return false;
    }

    struct stat st;
    if (fstat(in, &st) != 0) {
        if (error) *error = "stat " + source + ": " + strerror(errno);
        close(in);
        return false;
    }
    if (!S_ISREG(st.st_mode)) {
        if (error) *error = source + " is not a regular file";
        close(in);
        return false;
    }

    off_t start_offset = 0;
    bool truncated = false;
    if (st.st_size > 0 && static_cast<unsigned long long>(st.st_size) > max_bytes) {
        truncated = true;
        start_offset = st.st_size - static_cast<off_t>(max_bytes);
    }
    if (lseek(in, start_offset, SEEK_SET) == static_cast<off_t>(-1)) {
        if (error) *error = "seek " + source + ": " + strerror(errno);
        close(in);
        return false;
    }

    if (!mkdir_p(parent_dir(destination), error)) {
        close(in);
        return false;
    }
    int out = open(destination.c_str(), O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, mode);
    if (out < 0) {
        if (error) *error = "open " + destination + ": " + strerror(errno);
        close(in);
        return false;
    }

    bool ok = true;
    size_t bytes_left = max_bytes;
    if (truncated) {
        std::string header = "# truncated: copied latest " + std::to_string(max_bytes) +
            " of " + std::to_string(static_cast<unsigned long long>(st.st_size)) +
            " bytes from " + source + "\n";
        if (!write_all(out, header.data(), header.size())) {
            if (error) *error = "write " + destination + ": " + strerror(errno);
            ok = false;
        }
    }

    char buf[16 * 1024];
    while (ok && bytes_left > 0) {
        size_t read_size = std::min(sizeof(buf), bytes_left);
        ssize_t n = read(in, buf, read_size);
        if (n < 0) {
            if (errno == EINTR) {
                continue;
            }
            if (error) *error = "read " + source + ": " + strerror(errno);
            ok = false;
            break;
        }
        if (n == 0) {
            break;
        }
        if (!write_all(out, buf, static_cast<size_t>(n))) {
            if (error) *error = "write " + destination + ": " + strerror(errno);
            ok = false;
            break;
        }
        bytes_left -= static_cast<size_t>(n);
    }

    if (close(in) != 0 && ok) {
        if (error) *error = "close " + source + ": " + strerror(errno);
        ok = false;
    }
    if (close(out) != 0 && ok) {
        if (error) *error = "close " + destination + ": " + strerror(errno);
        ok = false;
    }
    if (!ok) {
        unlink(destination.c_str());
    }
    return ok;
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

AgentLogSnapshot read_agent_log_snapshot(const Options& options) {
    const std::string path = agent_log_path(options);
    return read_log_snapshot(path.c_str(), kAgentLogReadSize, kAgentLogDisplaySize);
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

std::string recent_agent_startup_log(const Options& options) {
    const std::string path = agent_log_path(options);
    std::string log = read_file_tail(path.c_str(), kAgentStatusLogReadSize);
    if (log.empty()) {
        return "";
    }

    const auto rfind_event = [&log](const std::string& event) {
        size_t marker = log.rfind(event);
        while (marker != std::string::npos) {
            const size_t boundary = marker + event.size();
            if (boundary == log.size() || log[boundary] == ' ' ||
                log[boundary] == '\n' || log[boundary] == '\r') {
                return marker;
            }
            if (marker == 0) {
                break;
            }
            marker = log.rfind(event, marker - 1);
        }
        return std::string::npos;
    };

    size_t marker = rfind_event("[agent] [supervisor] process_starting");
    if (marker == std::string::npos) {
        marker = rfind_event("[agent] [supervisor] binary_wait");
    }
    // Preserve compatibility with logs written before the common format was deployed.
    if (marker == std::string::npos) {
        marker = log.rfind("[agent] starting ");
    }
    if (marker == std::string::npos) {
        marker = log.rfind("[agent] waiting for ");
    }
    if (marker != std::string::npos) {
        size_t line_start = log.rfind('\n', marker);
        log = log.substr(line_start == std::string::npos ? 0 : line_start + 1);
    }
    return trim_for_display(log, kAgentStatusLogDisplaySize);
}

bool log_excerpt_looks_like_startup_failure(const std::string& log) {
    std::string lower = lowercase_copy(log);
    return lower.find(" [error] ") != std::string::npos ||
           lower.find("exited with status") != std::string::npos ||
           lower.find("error") != std::string::npos ||
           lower.find("failed") != std::string::npos ||
           lower.find("not found") != std::string::npos ||
           lower.find("panic") != std::string::npos;
}

AgentRuntimeStatus query_agent_status(const Options& options) {
    AgentRuntimeStatus status;
    status.addr = ":8080";
    status.port_host = kAgentPortHost;
    status.port = kDefaultAgentPort;
    const char* init_script = agent_init_script_path();

    if (!file_exists(init_script)) {
        status.state = "unknown";
        status.detail = std::string("agent init script not found: ") + init_script;
        status.startup_error = status.detail;
    } else {
        CommandResult script_status = run_shell_command_with_timeout(
            shell_quote(init_script) + " status 2>&1", kAgentStatusCommandTimeoutMs);
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
        std::string log = recent_agent_startup_log(options);
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
    AgentRuntimeStatus agent_status = query_agent_status(options);
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
    const ConfigMetadataSchema* metadata = NULL;
    std::string error;
    if (!load_agent_config_metadata(&metadata, &error)) {
        return make_json_error(503, error.empty() ? "config metadata unavailable" : error);
    }

    ApiResponse response;
    response.body = metadata->json;
    return response;
}

ApiResponse handle_get_agent_status(const Options& options) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddItemToObject(root, "agent_status", agent_status_to_json(query_agent_status(options)));
    return make_json_ok(root);
}

ApiResponse handle_get_agent_log(const Options& options) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddItemToObject(root, "agent_log", agent_log_to_json(read_agent_log_snapshot(options)));
    return make_json_ok(root);
}

// ----- storage (microSD) ---------------------------------------------------
//
// The agent's StorageManager owns the SD card and mirrors its state to a
// small KEY=VALUE file. Status reads come straight from that mirror; format
// and eject requests are proxied to the agent HTTP API, which stays the single
// executor for card operations. The effective mode is derived from card
// availability and cannot be changed through the portal.

bool read_storage_state(const std::string& path, std::map<std::string, std::string>* out) {
    if (!out) {
        return false;
    }
    out->clear();
    std::string contents = read_file_contents(path.c_str(), 16 * 1024);
    if (contents.empty()) {
        return false;
    }
    std::istringstream input(contents);
    std::string line;
    while (std::getline(input, line)) {
        std::string trimmed = trim_copy(line);
        if (trimmed.empty()) {
            continue;
        }
        size_t eq = trimmed.find('=');
        if (eq == std::string::npos) {
            continue;
        }
        (*out)[trimmed.substr(0, eq)] = trimmed.substr(eq + 1);
    }
    return !out->empty();
}

int storage_state_int(const std::map<std::string, std::string>& kv, const char* key, int fallback) {
    std::map<std::string, std::string>::const_iterator it = kv.find(key);
    if (it == kv.end() || it->second.empty()) {
        return fallback;
    }
    char* end = NULL;
    long value = strtol(it->second.c_str(), &end, 10);
    if (!end || *end != '\0' || value < 0 || value > 1000000) {
        return fallback;
    }
    return static_cast<int>(value);
}

double storage_state_double(const std::map<std::string, std::string>& kv, const char* key) {
    std::map<std::string, std::string>::const_iterator it = kv.find(key);
    if (it == kv.end() || it->second.empty()) {
        return 0;
    }
    return strtod(it->second.c_str(), NULL);
}

std::string storage_state_string(const std::map<std::string, std::string>& kv, const char* key) {
    std::map<std::string, std::string>::const_iterator it = kv.find(key);
    return it == kv.end() ? std::string() : it->second;
}

bool storage_state_flag(const std::map<std::string, std::string>& kv, const char* key) {
    return storage_state_string(kv, key) == "1";
}

ApiResponse handle_get_storage_status(const Options& options) {
    std::map<std::string, std::string> kv;
    bool available = read_storage_state(options.storage_state_path, &kv);

    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddBoolToObject(root, "available", available ? 1 : 0);
    cJSON_AddBoolToObject(root, "sd_present", storage_state_flag(kv, "SD_PRESENT") ? 1 : 0);
    cJSON_AddBoolToObject(root, "sd_mounted", storage_state_flag(kv, "SD_MOUNTED") ? 1 : 0);
    cJSON_AddStringToObject(root, "device", storage_state_string(kv, "SD_DEVICE").c_str());
    cJSON_AddStringToObject(root, "mount_point", storage_state_string(kv, "SD_MOUNTPOINT").c_str());
    cJSON_AddNumberToObject(root, "effective_mode", storage_state_int(kv, "EFFECTIVE_MODE", 1));
    cJSON_AddNumberToObject(root, "total_bytes", storage_state_double(kv, "SD_TOTAL_BYTES"));
    cJSON_AddNumberToObject(root, "free_bytes", storage_state_double(kv, "SD_FREE_BYTES"));
    cJSON_AddStringToObject(root, "reason", storage_state_string(kv, "REASON").c_str());

    cJSON* job = cJSON_CreateObject();
    std::string job_status = storage_state_string(kv, "FORMAT_STATUS");
    cJSON_AddStringToObject(job, "status", job_status.empty() ? "idle" : job_status.c_str());
    cJSON_AddStringToObject(job, "fs", storage_state_string(kv, "FORMAT_FS").c_str());
    cJSON_AddBoolToObject(job, "auto", storage_state_flag(kv, "FORMAT_AUTO") ? 1 : 0);
    cJSON_AddStringToObject(job, "error", storage_state_string(kv, "FORMAT_ERROR").c_str());
    cJSON_AddItemToObject(root, "format_job", job);

    cJSON* migration = cJSON_CreateObject();
    std::string migrate_status = storage_state_string(kv, "MIGRATE_STATUS");
    cJSON_AddStringToObject(migration, "status", migrate_status.empty() ? "idle" : migrate_status.c_str());
    cJSON_AddStringToObject(migration, "detail", storage_state_string(kv, "MIGRATE_DETAIL").c_str());
    cJSON_AddStringToObject(migration, "error", storage_state_string(kv, "MIGRATE_ERROR").c_str());
    cJSON_AddNumberToObject(migration, "moved_files", storage_state_int(kv, "MIGRATE_MOVED_FILES", 0));
    cJSON_AddNumberToObject(migration, "moved_bytes", storage_state_double(kv, "MIGRATE_MOVED_BYTES"));
    cJSON_AddItemToObject(root, "migration", migration);
    return make_json_ok(root);
}

ApiResponse handle_post_storage(const Options& options,
                                const std::string& agent_path,
                                const std::string& body) {
    return proxy_agent_json_request(options, agent_path, body.empty() ? "{}" : body);
}
// LLM_LOG_HANDLERS_PLACEHOLDER

// url_decode expands percent-escapes (%XX) and '+' in a single URL path
// segment. Invalid escapes are passed through verbatim. Decoding before
// validation ensures an encoded separator like %2f cannot smuggle a path
// traversal past is_llm_log_name().
std::string url_decode(const std::string& in) {
    std::string out;
    out.reserve(in.size());
    for (size_t i = 0; i < in.size(); ++i) {
        char c = in[i];
        if (c == '%' && i + 2 < in.size()) {
            char hi = in[i + 1];
            char lo = in[i + 2];
            if (isxdigit(static_cast<unsigned char>(hi)) && isxdigit(static_cast<unsigned char>(lo))) {
                auto hex_val = [](char h) -> int {
                    if (h >= '0' && h <= '9') return h - '0';
                    if (h >= 'a' && h <= 'f') return h - 'a' + 10;
                    return h - 'A' + 10;
                };
                out.push_back(static_cast<char>((hex_val(hi) << 4) | hex_val(lo)));
                i += 2;
                continue;
            }
        }
        if (c == '+') {
            out.push_back(' ');
            continue;
        }
        out.push_back(c);
    }
    return out;
}

// llm_log_dir returns the directory holding the agent's raw LLM HTTP logs.
// The agent writes them to <config_dir>/log (see runtime.go), so we derive
// the directory from the config path the server was launched with.
std::string llm_log_dir(const Options& options) {
    std::string base = parent_dir(options.agent_config_path);
    if (base.empty()) {
        return "log";
    }
    if (base == "/") {
        return "/log";
    }
    return base + "/log";
}

std::string agent_log_path(const Options& options) {
    return llm_log_dir(options) + "/agent.log";
}

std::string llm_log_path(const Options& options, const std::string& name) {
    return llm_log_dir(options) + "/" + name;
}

// is_llm_log_name reports whether name is a plain llm-http-*.log filename with
// no path separators. Used both to filter directory listings and to validate
// the filename segment of the import/export endpoints.
bool is_llm_log_name(const std::string& name) {
    if (name.find('/') != std::string::npos || name.find('\\') != std::string::npos) {
        return false;
    }
    // Reject embedded NUL/control bytes. A decoded '\0' (from %00) would pass
    // the suffix check on the std::string yet truncate at the NUL once handed
    // to C file APIs via c_str(), bypassing the .log suffix guard.
    for (unsigned char ch : name) {
        if (ch < 0x20 || ch == 0x7f) {
            return false;
        }
    }
    if (name == "." || name == "..") {
        return false;
    }
    const std::string prefix = kLlmLogPrefix;
    const std::string suffix = kLlmLogSuffix;
    if (name.size() <= prefix.size() + suffix.size()) {
        return false;
    }
    if (name.compare(0, prefix.size(), prefix) != 0) {
        return false;
    }
    if (name.compare(name.size() - suffix.size(), suffix.size(), suffix) != 0) {
        return false;
    }
    return true;
}

int open_llm_log_file(const Options& options,
                      const std::string& name,
                      struct stat* st,
                      LlmLogOpenStatus* status,
                      std::string* error) {
    if (st) {
        memset(st, 0, sizeof(*st));
    }
    if (status) {
        *status = LLM_LOG_OPEN_ERROR;
    }
    if (!is_llm_log_name(name)) {
        if (status) *status = LLM_LOG_OPEN_INVALID_NAME;
        if (error) *error = "invalid log file name";
        return -1;
    }
    const std::string full = llm_log_path(options, name);
    int fd = open(full.c_str(), O_RDONLY | O_NOFOLLOW | O_CLOEXEC);
    if (fd < 0) {
        const int open_errno = errno;
        if (open_errno == ENOENT || open_errno == ENOTDIR || open_errno == ELOOP) {
            if (status) *status = LLM_LOG_OPEN_NOT_FOUND;
            if (error) *error = "log file not found";
        } else {
            if (status) *status = LLM_LOG_OPEN_ERROR;
            if (error) *error = std::string("failed to open log file: ") + strerror(open_errno);
        }
        return -1;
    }
    struct stat info;
    if (fstat(fd, &info) != 0) {
        const int stat_errno = errno;
        close(fd);
        if (status) *status = LLM_LOG_OPEN_ERROR;
        if (error) *error = std::string("failed to stat log file: ") + strerror(stat_errno);
        return -1;
    }
    if (!S_ISREG(info.st_mode)) {
        close(fd);
        if (status) *status = LLM_LOG_OPEN_NOT_FOUND;
        if (error) *error = "log file not found";
        return -1;
    }
    if (st) {
        *st = info;
    }
    if (status) {
        *status = LLM_LOG_OPEN_OK;
    }
    if (error) {
        error->clear();
    }
    return fd;
}

ApiResponse handle_get_llm_logs(const Options& options) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON* files = add_array(root, "files");

    const std::string dir_path = llm_log_dir(options);
    DIR* dir = opendir(dir_path.c_str());
    if (dir) {
        // Collect matching names, then sort descending so the newest session
        // (which sorts last by the YYYYMMDDHHMMSSmmm-keyed name) shows first.
        std::vector<std::string> names;
        struct dirent* entry = NULL;
        while ((entry = readdir(dir)) != NULL) {
            std::string name = entry->d_name;
            if (is_llm_log_name(name)) {
                names.push_back(name);
            }
        }
        closedir(dir);
        std::sort(names.begin(), names.end(), std::greater<std::string>());

        for (size_t i = 0; i < names.size(); ++i) {
            const std::string full = dir_path + "/" + names[i];
            // lstat (not stat) so a symlink is not followed: only genuine
            // regular files in the log dir are listed, never a symlink that
            // could point outside the intended log set.
            struct stat st;
            if (lstat(full.c_str(), &st) != 0 || !S_ISREG(st.st_mode)) {
                continue;
            }
            cJSON* file = cJSON_CreateObject();
            cJSON_AddStringToObject(file, "name", names[i].c_str());
            cJSON_AddNumberToObject(file, "size_bytes", static_cast<double>(st.st_size));
            cJSON_AddNumberToObject(file, "mtime", static_cast<double>(st.st_mtime));
            cJSON_AddItemToArray(files, file);
        }
    }
    // A missing directory simply yields an empty list -- the agent may not
    // have produced any logs yet, which is not an error condition.
    return make_json_ok(root);
}

ApiResponse handle_export_llm_log_file(const Options& options, const std::string& name) {
    struct stat st;
    LlmLogOpenStatus open_status = LLM_LOG_OPEN_ERROR;
    std::string error;
    int fd = open_llm_log_file(options, name, &st, &open_status, &error);
    if (fd < 0) {
        int status_code = 500;
        switch (open_status) {
            case LLM_LOG_OPEN_INVALID_NAME:
                status_code = 400;
                break;
            case LLM_LOG_OPEN_NOT_FOUND:
                status_code = 404;
                break;
            default:
                status_code = 500;
                break;
        }
        return make_json_error(status_code, error.empty() ? "failed to open log file" : error);
    }
    ApiResponse response;
    response.content_type = "text/plain; charset=utf-8";
    response.body.clear();
    response.body_fd = fd;
    response.body_fd_size = static_cast<unsigned long long>(st.st_size);
    return response;
}

ApiResponse handle_import_llm_log_file(const Options& options, const std::string& name, const HttpRequest& request) {
    if (!is_llm_log_name(name)) {
        return make_json_error(400, "invalid log file name");
    }
    const std::string path = llm_log_path(options, name);
    std::string error;
    if (!request.body_file_path.empty()) {
        if (!mkdir_p(parent_dir(path), &error)) {
            return make_json_error(500, error.empty() ? "failed to prepare log directory" : error);
        }
        if (rename(request.body_file_path.c_str(), path.c_str()) != 0) {
            return make_json_error(500, std::string("rename ") + request.body_file_path + " -> " + path + ": " + strerror(errno));
        }
        if (chmod(path.c_str(), 0644) != 0) {
            return make_json_error(500, std::string("chmod ") + path + ": " + strerror(errno));
        }
    } else if (!atomic_write_file(path, request.body, 0644, &error)) {
        return make_json_error(500, error.empty() ? "failed to import log file" : error);
    }

    cJSON* root = cJSON_CreateObject();
    cJSON_AddBoolToObject(root, "ok", 1);
    cJSON_AddStringToObject(root, "name", name.c_str());
    cJSON_AddNumberToObject(root, "size_bytes", static_cast<double>(request.body_file_path.empty() ? request.body.size() : request.body_size));
    cJSON_AddStringToObject(root, "message", "llm log imported");
    return make_json_ok(root);
}

std::string memory_dir(const Options& options) {
    std::string base = parent_dir(options.agent_config_path);
    if (base.empty()) {
        return "memory";
    }
    if (base == "/") {
        return "/memory";
    }
    return base + "/memory";
}

std::string episode_dir(const Options& options) {
    return memory_dir(options) + "/episodes";
}

std::string latest_llm_log_name(const Options& options) {
    const std::string dir_path = llm_log_dir(options);
    DIR* dir = opendir(dir_path.c_str());
    if (!dir) {
        return "";
    }

    bool found = false;
    time_t best_mtime = 0;
    std::string best_name;
    struct dirent* entry = NULL;
    while ((entry = readdir(dir)) != NULL) {
        std::string name = entry->d_name;
        if (!is_llm_log_name(name)) {
            continue;
        }
        const std::string full = dir_path + "/" + name;
        struct stat st;
        if (lstat(full.c_str(), &st) != 0 || !S_ISREG(st.st_mode)) {
            continue;
        }
        if (!found || st.st_mtime > best_mtime ||
            (st.st_mtime == best_mtime && name > best_name)) {
            found = true;
            best_mtime = st.st_mtime;
            best_name = name;
        }
    }
    closedir(dir);
    return best_name;
}

std::string latest_llm_log_path(const Options& options) {
    std::string name = latest_llm_log_name(options);
    return name.empty() ? "" : llm_log_path(options, name);
}

std::string latest_episode_yaml_path(const Options& options) {
    const std::string root = episode_dir(options);
    std::vector<std::string> pending;
    pending.push_back(root);

    bool found = false;
    time_t best_mtime = 0;
    std::string best_path;
    while (!pending.empty()) {
        std::string dir_path = pending.back();
        pending.pop_back();

        DIR* dir = opendir(dir_path.c_str());
        if (!dir) {
            continue;
        }
        struct dirent* entry = NULL;
        while ((entry = readdir(dir)) != NULL) {
            std::string name = entry->d_name;
            if (name == "." || name == "..") {
                continue;
            }
            const std::string full = dir_path + "/" + name;
            struct stat st;
            if (lstat(full.c_str(), &st) != 0) {
                continue;
            }
            if (S_ISDIR(st.st_mode)) {
                pending.push_back(full);
                continue;
            }
            if (name != "episode.yaml" || !S_ISREG(st.st_mode)) {
                continue;
            }
            if (!found || st.st_mtime > best_mtime ||
                (st.st_mtime == best_mtime && full > best_path)) {
                found = true;
                best_mtime = st.st_mtime;
                best_path = full;
            }
        }
        closedir(dir);
    }
    return best_path;
}

bool write_placeholder_file(const std::string& path,
                            const std::string& label,
                            const std::string& detail,
                            std::string* error) {
    std::ostringstream content;
    content << label << "\n";
    content << detail;
    if (detail.empty() || detail[detail.size() - 1] != '\n') {
        content << "\n";
    }
    return atomic_write_file(path, content.str(), 0644, error);
}

bool stage_file_or_placeholder(const std::string& source,
                               const std::string& destination,
                               const std::string& label,
                               const std::string& missing_detail,
                               size_t max_bytes,
                               std::string* error) {
    if (!source.empty()) {
        std::string copy_error;
        if (copy_regular_file_tail(source, destination, max_bytes, 0644, &copy_error)) {
            return true;
        }
        std::ostringstream detail;
        detail << "source: " << source << "\n";
        detail << "copy_error: " << copy_error << "\n";
        return write_placeholder_file(destination, label + " unavailable", detail.str(), error);
    }
    return write_placeholder_file(destination, label + " unavailable", missing_detail, error);
}

void remove_tree_best_effort(const std::string& path) {
    if (path.empty()) {
        return;
    }
    CommandResult result = run_shell_command_with_timeout(
        "rm -rf " + shell_quote(path), 5000);
    (void)result;
}

ApiResponse handle_export_support_logs(const Options& options) {
    std::string stage_dir;
    std::string error;
    const std::string log_path = agent_log_path(options);
    if (!create_temp_dir_in_dir("/tmp", kSupportLogStagePrefix, &stage_dir, &error)) {
        return make_json_error(500, error.empty() ? "failed to create log staging directory" : error);
    }

    bool staged = true;
    staged = staged && stage_file_or_placeholder(
        latest_episode_yaml_path(options),
        stage_dir + "/" + kSupportLogLangfuseName,
        "Langfuse episode data",
        "No episode.yaml files found under " + episode_dir(options) + ".\n",
        kSupportLogLangfuseMaxBytes,
        &error);
    staged = staged && stage_file_or_placeholder(
        log_path,
        stage_dir + "/" + kSupportLogAgentName,
        "Agent log",
        "Agent log path not available: " + log_path + "\n",
        kSupportLogAgentMaxBytes,
        &error);

    // Keep the original LLM log filename instead of renaming to http.log
    // Capture both name and path atomically to avoid race conditions during log rotation
    std::string llm_log_original_name = latest_llm_log_name(options);
    std::string llm_log_source_path;
    if (llm_log_original_name.empty()) {
        llm_log_original_name = kSupportLogHttpName;
        llm_log_source_path = "";  // Will trigger placeholder
    } else {
        llm_log_source_path = llm_log_path(options, llm_log_original_name);
    }
    staged = staged && stage_file_or_placeholder(
        llm_log_source_path,
        stage_dir + "/" + llm_log_original_name,
        "HTTP log",
        "No llm-http-*.log files found under " + llm_log_dir(options) + ".\n",
        kSupportLogHttpMaxBytes,
        &error);
    if (!staged) {
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, error.empty() ? "failed to stage support logs" : error);
    }

    int archive_fd = -1;
    std::string archive_path;
    if (!create_temp_file_in_dir("/tmp", kSupportLogArchivePrefix, 0600, &archive_fd, &archive_path, &error)) {
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, error.empty() ? "failed to create log archive file" : error);
    }
    if (close(archive_fd) != 0) {
        unlink(archive_path.c_str());
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, std::string("close ") + archive_path + ": " + strerror(errno));
    }
    archive_fd = -1;

    std::string tar_args = shell_quote(archive_path) +
        " -C " + shell_quote(stage_dir) + " " +
        shell_quote(kSupportLogLangfuseName) + " " +
        shell_quote(kSupportLogAgentName) + " " +
        shell_quote(llm_log_original_name);
    std::string tar_stream_args =
        "-C " + shell_quote(stage_dir) + " " +
        shell_quote(kSupportLogLangfuseName) + " " +
        shell_quote(kSupportLogAgentName) + " " +
        shell_quote(llm_log_original_name);
    std::string fallback_tar_path = archive_path + ".tar";
    std::string command = "tar -czf " + tar_args +
        " 2>/dev/null || (rm -f " + shell_quote(fallback_tar_path) +
        " && tar -cf " + shell_quote(fallback_tar_path) + " " + tar_stream_args +
        " && gzip -c " + shell_quote(fallback_tar_path) + " > " + shell_quote(archive_path) +
        " && rm -f " + shell_quote(fallback_tar_path) + ")";
    CommandResult tar = run_shell_command_with_timeout(command, kSupportLogExportTimeoutMs);
    if (tar.timed_out || tar.exit_code != 0) {
        std::string detail = tar.timed_out ? "tar timed out" : "tar exited " + std::to_string(tar.exit_code);
        if (!trim_trailing_newlines(tar.output).empty()) {
            detail += ": " + trim_trailing_newlines(tar.output);
        }
        unlink(archive_path.c_str());
        unlink(fallback_tar_path.c_str());
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, "failed to create " + std::string(kSupportLogArchiveName) + ": " + detail);
    }

    int fd = open(archive_path.c_str(), O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        std::string open_error = std::string("open ") + archive_path + ": " + strerror(errno);
        unlink(archive_path.c_str());
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, open_error);
    }
    struct stat st;
    if (fstat(fd, &st) != 0) {
        std::string stat_error = std::string("stat ") + archive_path + ": " + strerror(errno);
        close(fd);
        unlink(archive_path.c_str());
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, stat_error);
    }
    unsigned char magic[2];
    ssize_t magic_read = read(fd, magic, sizeof(magic));
    if (magic_read != static_cast<ssize_t>(sizeof(magic)) || magic[0] != 0x1f || magic[1] != 0x8b) {
        close(fd);
        unlink(archive_path.c_str());
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, "failed to create " + std::string(kSupportLogArchiveName) + ": archive is not gzip");
    }
    if (lseek(fd, 0, SEEK_SET) == static_cast<off_t>(-1)) {
        std::string seek_error = std::string("seek ") + archive_path + ": " + strerror(errno);
        close(fd);
        unlink(archive_path.c_str());
        remove_tree_best_effort(stage_dir);
        return make_json_error(500, seek_error);
    }

    unlink(archive_path.c_str());
    remove_tree_best_effort(stage_dir);

    ApiResponse response;
    response.content_type = "application/gzip";
    response.body.clear();
    response.body_fd = fd;
    response.body_fd_size = static_cast<unsigned long long>(st.st_size);
    return response;
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
    unsigned long long start_size_bytes = 0;
    if (!schedule_ota_update(&start_size_bytes, &error)) {
        return make_json_error(500, error.empty() ? "failed to start ota update" : error);
    }

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddBoolToObject(response, "ota_update_started", 1);
    cJSON_AddStringToObject(response, "message", "ota update started");
    cJSON_AddNumberToObject(response, "ota_log_start_size_bytes", static_cast<double>(start_size_bytes));
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
    std::string original_device_type = effective_device_type(config);
    std::string original_keyboard_layout = config.hid.keyboard_layout;

    bool submitted_model_api_key = false;
    cJSON* config_json = cJSON_GetObjectItem(root, "config");
    if (config_json) {
        std::string metadata_error;
        if (!load_agent_config_metadata(NULL, &metadata_error)) {
            cJSON_Delete(root);
            return make_json_error(
                503,
                metadata_error.empty() ? "config metadata unavailable" : metadata_error);
        }
        std::string schema_error;
        if (!validate_agent_config_patch_json(config_json, &schema_error)) {
            cJSON_Delete(root);
            return make_json_error(400, schema_error.empty() ? "invalid config schema" : schema_error);
        }
        cJSON* model_patch = cJSON_GetObjectItem(config_json, "model");
        submitted_model_api_key =
            json_is_object(model_patch) &&
            json_is_string(cJSON_GetObjectItem(model_patch, "api_key"));
        update_config_from_json(config_json, &config);
        preserve_redacted_agent_secrets(options, &config);
    }
    aiden::migrate_flat_voice_provider_fields(config);
    // Only an explicitly submitted legacy model.api_key can be migrated. The
    // value loaded from the runtime config is already resolved from a provider
    // record and may belong to a different provider than the request selects.
    move_model_api_key_to_provider(&config, submitted_model_api_key);

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
        AIDEN_LOG_WARN("agent_config", "save_rejected", "outcome=%s error=%s",
                       validation.outcome == ValidationOutcome::UNAVAILABLE ? "unavailable" : "invalid",
                       validation.message.c_str());
        cJSON_Delete(root);
        // 503 when the validator itself is missing/broken: a server-side
        // dependency outage, not a user input error. 400 only when the
        // validator ran and rejected the user's config.
        int status = validation.outcome == ValidationOutcome::UNAVAILABLE ? 503 : 400;
        return make_json_error(status, validation.message);
    }

    cJSON_Delete(root);
    config.device.device_type = effective_device_type(config);
    config.hid.pointer_mode = pointer_mode_for_device_type(config.device.device_type);
    config.hid.input_backend = normalize_input_backend(config.hid.input_backend);
    // device_type is the user-visible source of truth for the USB HID profile;
    // pointer_mode is derived from it. keyboard_layout changes the Agent's key
    // mapping, but iOS also pins its hardware layout at USB enumeration. A
    // same-identity UDC bounce can leave iOS with inconsistent keyboard and
    // pointer state, so either configuration change requires a clean restart.
    bool device_type_changed = original_device_type != config.device.device_type;
    bool keyboard_layout_changed = original_keyboard_layout != config.hid.keyboard_layout;
    bool usb_hid_reboot_required = device_type_changed || keyboard_layout_changed;

    std::string save_error;
    if (!aiden::save_agent_toml(options.agent_config_path.c_str(), config, &save_error)) {
        return make_json_error(500, save_error);
    }

    bool should_save_wifi = !wifi.ssid.empty() || !wifi.networks.empty() || file_exists(options.wifi_config_path.c_str());
    if (should_save_wifi && !aiden::save_wifi_config(options.wifi_config_path.c_str(), wifi, &save_error)) {
        return make_json_error(500, save_error);
    }

    bool agent_restart_scheduled = !usb_hid_reboot_required;
    if (agent_restart_scheduled) {
        schedule_agent_restart();
    }

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "message",
                            usb_hid_reboot_required
                                ? "config saved; USB HID configuration changed; reboot required"
                                : "config saved; agent restarting");
    cJSON_AddBoolToObject(response, "agent_restart_scheduled", agent_restart_scheduled ? 1 : 0);
    cJSON_AddBoolToObject(response, "usb_reenumeration_scheduled", 0);
    cJSON_AddBoolToObject(response, "usbhid_restart_scheduled", 0);
    cJSON_AddBoolToObject(response, "usbhid_restart_required", usb_hid_reboot_required ? 1 : 0);
    cJSON_AddBoolToObject(response, "reboot_required", usb_hid_reboot_required ? 1 : 0);
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

ApiResponse handle_put_config_locale(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    cJSON* locale_item = cJSON_GetObjectItem(root, "locale");
    if (!json_is_string(locale_item)) {
        cJSON_Delete(root);
        return make_json_error(400, "missing locale string");
    }
    const std::string locale = locale_item->valuestring;
    cJSON_Delete(root);

    if (locale != kLocaleSimplifiedChinese && locale != kLocaleEnglishUS) {
        return make_json_error(400, "unsupported locale; expected zh-CN or en-US");
    }

    std::string original_content;
    mode_t config_mode = 0600;
    if (file_exists(options.agent_config_path.c_str())) {
        std::string read_error;
        if (!read_file_contents_checked(options.agent_config_path.c_str(),
                                        kMaxAgentConfigSize,
                                        &original_content,
                                        &read_error)) {
            return make_json_error(500, read_error.empty() ? "failed to read agent config" : read_error);
        }
        struct stat st;
        if (stat(options.agent_config_path.c_str(), &st) == 0) {
            config_mode = st.st_mode & 0777;
        }
    }
    const std::string updated_content = update_top_level_locale(original_content, locale);

    ValidationResult validation = validate_agent_toml_content_for_save(
            updated_content, options.agent_config_path);
    if (validation.outcome != ValidationOutcome::OK) {
        const int status = validation.outcome == ValidationOutcome::UNAVAILABLE ? 503 : 400;
        return make_json_error(status, validation.message);
    }

    std::string save_error;
    if (!atomic_write_file(options.agent_config_path, updated_content, config_mode, &save_error)) {
        return make_json_error(500, save_error);
    }

    schedule_agent_restart();

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "locale", locale.c_str());
    cJSON_AddStringToObject(response, "message", "locale saved; agent restarting");
    cJSON_AddBoolToObject(response, "agent_restart_scheduled", 1);
    return make_json_ok(response);
}

ApiResponse handle_post_reboot() {
    std::string error;
    if (!schedule_reboot(&error)) {
        return make_json_error(503, error.empty() ? "failed to schedule reboot" : error);
    }

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "message", "reboot scheduled");
    cJSON_AddBoolToObject(response, "reboot_scheduled", 1);
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

    const std::string target_ssid = ssid_item->valuestring;
    cJSON* psk_item = cJSON_GetObjectItem(root, "psk");
    const bool has_psk = json_is_string(psk_item);
    std::string requested_psk = has_psk ? std::string(psk_item->valuestring) : std::string();
    cJSON* country_item = cJSON_GetObjectItem(root, "country");
    std::string requested_country = json_is_string(country_item) ? trim_copy(country_item->valuestring) : std::string();
    cJSON_Delete(root);

    aiden::WifiNetworkConfig original;
    std::string ignore_error;
    const bool had_original_config = file_exists(options.wifi_config_path.c_str());
    std::string original_wifi_config_text;
    if (had_original_config) {
        struct stat st;
        if (stat(options.wifi_config_path.c_str(), &st) != 0) {
            return make_json_error(500,
                                   std::string("failed to stat existing wifi config for rollback: ") +
                                       strerror(errno));
        }
        if (st.st_size < 0) {
            return make_json_error(500, "failed to stat existing wifi config for rollback: invalid size");
        }
        std::string snapshot_error;
        if (!read_file_contents_checked(options.wifi_config_path.c_str(),
                                        static_cast<size_t>(st.st_size) + 1,
                                        &original_wifi_config_text,
                                        &snapshot_error)) {
            return make_json_error(500, "failed to read existing wifi config for rollback: " + snapshot_error);
        }
    }
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
    const std::string ssid = ssid_item->valuestring;
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

bool build_agent_config_test_env_prefix(const Options& options,
                                        std::string* prefix,
                                        std::string* error) {
    if (prefix) prefix->clear();
    if (error) error->clear();
    if (!file_exists(options.system_env_path.c_str())) {
        return true;
    }

    std::string content;
    std::string read_error;
    if (!read_file_contents_checked(options.system_env_path.c_str(),
                                    kMaxSystemEnvSize,
                                    &content,
                                    &read_error)) {
        AIDEN_LOG_ERROR("config_test", "system_env_read_failed", "error=%s",
                        read_error.c_str());
        if (error) *error = "system env file is unavailable";
        return false;
    }

    std::string validation_error;
    if (!validate_system_env_content(content, &validation_error)) {
        AIDEN_LOG_ERROR("config_test", "system_env_parse_failed", "error=%s",
                        validation_error.c_str());
        if (error) *error = "system env file is invalid";
        return false;
    }

    if (prefix) {
        *prefix = "set -a && . " + shell_quote(options.system_env_path) +
                  " && set +a && ";
    }
    return true;
}

ApiResponse run_agent_provider_config_test(const Options& options,
                                           const std::string& section,
                                           const std::string& body) {
    std::string env_prefix;
    std::string env_error;
    if (!build_agent_config_test_env_prefix(options, &env_prefix, &env_error)) {
        return make_json_error(503, env_error);
    }

    std::string cmd = env_prefix + shell_quote(agent_bin_path()) +
                      " config-test --format=json --stdin --section=" +
                      shell_quote(section) + " --config=" +
                      shell_quote(options.agent_config_path) + " 2>&1";
    CommandResult cr = run_command_with_stdin(cmd, body, 60000);
    if (cr.timed_out) {
        return make_json_error(503, "agent config-test timed out");
    }

    cJSON* root = parse_config_test_result(cr.output);
    if (!json_is_object(root)) {
        if (root) {
            cJSON_Delete(root);
        }
        std::string detail = trim_trailing_newlines(cr.output);
        if (detail.empty()) {
            detail = "agent config-test returned an unexpected response";
        } else if (detail.size() > 400) {
            detail = detail.substr(0, 400) + "...";
        }
        return make_json_error(503, detail);
    }
    return make_json_ok(root);
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

    if (section == "model" || section == "tts" || section == "stt") {
        cJSON_Delete(root);
        return run_agent_provider_config_test(options, section, body);
    }

    cJSON* response = cJSON_CreateObject();
    cJSON* results = add_array(response, "results");
    bool all_passed = true;

    SystemProxy current_proxy;
    load_system_env_proxy(options.system_env_path, &current_proxy);
    std::string curl_proxy_arg = build_curl_proxy_arg(current_proxy);
    std::string curl_env_exports = build_proxy_env_exports(current_proxy);

    if (section == "audio") {
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
    } else if (section == "log") {
        cJSON* retention_item = cJSON_GetObjectItem(values, "llm_http_retention_days");
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "llm_http_retention_days");
        if (json_is_number(retention_item)) {
            int days = retention_item->valueint;
            if (days < 0) {
                cJSON_AddBoolToObject(r, "passed", 0);
                std::string msg = "must be >= 0, got " + std::to_string(days);
                cJSON_AddStringToObject(r, "detail", msg.c_str());
                all_passed = false;
            } else {
                cJSON_AddBoolToObject(r, "passed", 1);
                std::string detail = std::to_string(days) + (days == 0 ? " (default 7 days)" : " days");
                cJSON_AddStringToObject(r, "detail", detail.c_str());
            }
        } else {
            cJSON_AddBoolToObject(r, "passed", 0);
            cJSON_AddStringToObject(r, "detail", "not a number");
            all_passed = false;
        }
        cJSON_AddItemToArray(results, r);
    } else if (section == "device") {
        cJSON* device_type_item = cJSON_GetObjectItem(values, "device_type");
        bool string_value = true;
        std::string device_type = "iOS";
        if (device_type_item != NULL) {
            if (json_is_string(device_type_item)) {
                device_type = normalize_device_type(device_type_item->valuestring);
            } else {
                string_value = false;
            }
        }
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "device_type");
        bool valid = string_value &&
                     (device_type == "iOS" || device_type == "Android" || device_type == "macOS" ||
                      device_type == "windows" || device_type == "linux");
        cJSON_AddBoolToObject(r, "passed", valid ? 1 : 0);
        std::string detail = valid ? "effective pointer_mode: " + pointer_mode_for_device_type(device_type)
                                   : (string_value ? "must be iOS, Android, macOS, windows, or linux"
                                                   : "must be a string");
        cJSON_AddStringToObject(r, "detail", detail.c_str());
        if (!valid) all_passed = false;
        cJSON_AddItemToArray(results, r);
    } else if (section == "hid") {
        cJSON* backend_item = cJSON_GetObjectItem(values, "input_backend");
        std::string input_backend = json_is_string(backend_item) ? normalize_input_backend(backend_item->valuestring) : "hid";
        const char* dev_keys[] = {"keyboard_device", "mouse_device", "android_keyboard_device", NULL};
        for (int i = 0; dev_keys[i]; ++i) {
            cJSON* item = cJSON_GetObjectItem(values, dev_keys[i]);
            std::string path = json_is_string(item) ? trim_copy(item->valuestring) : "";
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", dev_keys[i]);
            if (input_backend == "adb") {
                cJSON_AddBoolToObject(r, "passed", 1);
                cJSON_AddStringToObject(r, "detail", "skipped when input_backend=adb");
            } else if (path.empty()) {
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
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "input_backend");
        bool valid = input_backend == "hid" || input_backend == "adb";
        cJSON_AddBoolToObject(r, "passed", valid ? 1 : 0);
        std::string detail = valid ? "effective backend: " + input_backend : "must be hid or adb";
        cJSON_AddStringToObject(r, "detail", detail.c_str());
        if (!valid) all_passed = false;
        cJSON_AddItemToArray(results, r);
    } else if (section == "agent") {
        struct Check {
            const char* key;
            const char* allowed[4];
        };
        Check enums[] = {
            {"input_mode", {"text", "stt", NULL}},
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
            } else if (std::string(enums[i].key) == "input_mode" && lowercase_copy(val) == "audio") {
                cJSON_AddBoolToObject(r, "passed", 0);
                std::string msg = "invalid input_mode: " + val + " (audio mode has been removed; use stt instead)";
                cJSON_AddStringToObject(r, "detail", msg.c_str());
                all_passed = false;
            } else if (ok && std::string(enums[i].key) == "trigger_mode" && val == "wakeup") {
                cJSON* input_item = cJSON_GetObjectItem(values, "input_mode");
                std::string input_mode = json_is_string(input_item) ? trim_copy(input_item->valuestring) : "";
                if (input_mode == "stt") {
                    cJSON_AddBoolToObject(r, "passed", 1);
                    cJSON_AddStringToObject(r, "detail", val.c_str());
                } else {
                    cJSON_AddBoolToObject(r, "passed", 0);
                    std::string msg = "wakeup requires input_mode stt, got '" + input_mode + "'";
                    cJSON_AddStringToObject(r, "detail", msg.c_str());
                    all_passed = false;
                }
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
            "voice_max_turns", "voice_max_response_tokens",
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
    start_deferred_agent_restart_if_idle();

    if (request.error_status_code != 0) {
        ApiResponse response;
        response.status_code = request.error_status_code;
        response.status_text = request.error_status_text;
        response.content_type = "text/plain; charset=utf-8";
        response.body = request.error_body;
        return response;
    }

    aiden::ConfigWebStaticAssetResponse static_asset =
        aiden::serve_config_web_static_asset(options.web_root, request.method, request.path);
    if (static_asset.handled) {
        ApiResponse response;
        response.status_code = static_asset.status_code;
        response.status_text = static_asset.status_text;
        response.content_type = static_asset.content_type;
        response.body = static_asset.body;
        response.body_fd = static_asset.body_fd;
        response.body_fd_size = static_asset.body_fd_size;
        response.headers = static_asset.headers;
        return response;
    }

    if (request.method == "GET" && request.path == "/api/llm-logs") {
        return handle_get_llm_logs(options);
    }

    {
        const std::string export_prefix = "/api/llm-logs/export/";
        if (request.method == "GET" && request.path.compare(0, export_prefix.size(), export_prefix) == 0) {
            std::string name = url_decode(request.path.substr(export_prefix.size()));
            return handle_export_llm_log_file(options, name);
        }
    }

    {
        const std::string import_prefix = "/api/llm-logs/import/";
        if (request.method == "POST" && request.path.compare(0, import_prefix.size(), import_prefix) == 0) {
            std::string name = url_decode(request.path.substr(import_prefix.size()));
            return handle_import_llm_log_file(options, name, request);
        }
    }

    if (request.method == "GET" && request.path == "/api/storage/status") {
        return handle_get_storage_status(options);
    }

    if (request.method == "POST" && request.path == "/api/storage/format") {
        return handle_post_storage(options, "/api/storage/format", request.body);
    }

    if (request.method == "POST" && request.path == "/api/storage/eject") {
        return handle_post_storage(options, "/api/storage/eject", request.body);
    }

    if (request.method == "GET" && request.path == "/api/agent/status") {
        return handle_get_agent_status(options);
    }

    if (request.method == "GET" && request.path == "/api/agent/logs") {
        return handle_get_agent_log(options);
    }

    if (request.method == "GET" && request.path == "/api/logs/export") {
        return handle_export_support_logs(options);
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

    // Handle /api/models?provider=xxx&locale=xxx
    if (request.method == "GET" && request.path == "/api/models") {
        return handle_api_models(options, request.query);
    }

    if (request.method == "GET" &&
        (request.path == "/api/config-meta" || request.path == "/api/config/meta")) {
        return handle_get_config_meta();
    }

    if (request.method == "POST" && request.path == "/api/config") {
        return handle_post_config(options, request.body);
    }

    if (request.method == "PUT" && request.path == "/api/config/locale") {
        return handle_put_config_locale(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/system/env") {
        return handle_post_system_env(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/reboot") {
        return handle_post_reboot();
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

    if (request.method == "POST" && request.path == "/api/config/test/stt/start") {
        return handle_stt_test_start(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/config/test/stt/stop") {
        return handle_stt_test_stop(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/hid/usb-reenumerate") {
        return handle_usb_reenumerate(options);
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
              << " [--ota-state=PATH] [--cmdline=PATH] [--system-env=PATH]"
              << " [--storage-state=PATH] [--web-root=PATH]" << std::endl;
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
        } else if (consume_prefix(arg, "--storage-state=", &options->storage_state_path)) {
        } else if (consume_prefix(arg, "--web-root=", &options->web_root)) {
            if (options->web_root.empty()) {
                return false;
            }
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

    aiden::set_log_service("config_web");

    if (!is_allowed_bind_address(options.bind_address)) {
        AIDEN_LOG_ERROR("server", "bind_address_rejected", "bind_address=%s",
                        options.bind_address.c_str());
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
        AIDEN_LOG_ERROR("server", "socket_create_failed", "error=%s", strerror(errno));
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
        AIDEN_LOG_ERROR("server", "bind_address_invalid", "bind_address=%s",
                        options.bind_address.c_str());
        close(server_fd);
        return 1;
    }

    if (bind(server_fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        AIDEN_LOG_ERROR("server", "bind_failed", "bind_address=%s port=%d error=%s",
                        options.bind_address.c_str(), options.port, strerror(errno));
        close(server_fd);
        return 1;
    }

    if (listen(server_fd, 10) < 0) {
        AIDEN_LOG_ERROR("server", "listen_failed", "error=%s", strerror(errno));
        close(server_fd);
        return 1;
    }

    AIDEN_LOG_INFO("server", "listening",
                   "bind_address=%s port=%d agent_config=%s wifi_config=%s system_env=%s web_root=%s",
                   options.bind_address.c_str(), options.port,
                   options.agent_config_path.c_str(), options.wifi_config_path.c_str(),
                   options.system_env_path.c_str(), options.web_root.c_str());

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

        // Restart helpers are forked while a request is being handled. Do not
        // let them inherit the client connection and keep the response socket
        // open until the init script exits.
        fcntl(client_fd, F_SETFD, FD_CLOEXEC);

        if (!set_socket_recv_timeout(client_fd, kClientReadTimeoutSeconds)) {
            close(client_fd);
            continue;
        }

        HttpRequest request = parse_request(client_fd, options);
        ApiResponse response = handle_request(options, request);
        send_response(client_fd, response);
        close_response_stream(&response);
        cleanup_request_temp_file(&request);
        close(client_fd);
    }

    close(server_fd);
    AIDEN_LOG_INFO("server", "stopped", "bind_address=%s port=%d",
                   options.bind_address.c_str(), options.port);
    return 0;
}
#endif  // AIDEN_CONFIG_WEB_NO_MAIN
