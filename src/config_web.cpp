#include "agent_toml.h"
#include "config_web_html.h"
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
#include <sys/socket.h>
#include <sys/select.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

#include <iostream>
#include <map>
#include <set>
#include <sstream>
#include <string>
#include <vector>

namespace {

struct Options {
    std::string bind_address = "192.168.42.1";
    int port = 80;
    std::string agent_config_path = "/userdata/agent/agent.toml";
    std::string wifi_config_path = "/userdata/wpa_supplicant.conf";
    std::string wifi_interface = "wlan0";
    std::string ota_state_path = "/userdata/ota/state.json";
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

struct TcpPortStatus {
    bool reachable = false;
    std::string detail;
};

volatile sig_atomic_t g_should_stop = 0;

const char* kUsbBindAddress = "192.168.42.1";
const char* kLoopbackBindAddress = "127.0.0.1";
const char* kAgentInitScript = "/etc/init.d/S53agent";
const char* kAgentLogPath = "/var/log/agent/agent.log";
const char* kAgentPortHost = "127.0.0.1";
const int kDefaultAgentPort = 8080;
const int kAgentStatusCommandTimeoutMs = 1500;
const size_t kAgentStatusLogReadSize = 16 * 1024;
const size_t kAgentStatusLogDisplaySize = 4096;
const size_t kAgentLogReadSize = 64 * 1024;
const size_t kAgentLogDisplaySize = 48 * 1024;
const size_t kMaxHttpHeaderSize = 8 * 1024;
const size_t kMaxHttpBodySize = 64 * 1024;
const int kClientReadTimeoutSeconds = 5;
const char* kDefaultNoProxy = "localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16";

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

bool starts_with(const std::string& text, const std::string& prefix) {
    return text.compare(0, prefix.size(), prefix) == 0;
}

bool has_proxy_url(const aiden::ProxyToml& proxy) {
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

bool json_is_bool(cJSON* item) {
    return json_is_type(item, cJSON_True) || json_is_type(item, cJSON_False);
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
    return bind_address == kUsbBindAddress || bind_address == kLoopbackBindAddress;
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

void apply_default_agent_config(aiden::AgentToml& cfg) {
    cfg.instruction =
        "默认用简体中文回答，语气要像真人说话，简短自然，适合 TTS 播放。"
        "需要读取或改变手机、外部设备或服务状态时必须使用工具；可以连续组合多个工具完成任务。"
        "每次截图或输入工具返回 post-action screenshot 后，都要先根据最新画面判断上一步是否已经生效、焦点是否改变、页面是否跳转；不要连续重复同一个点击、手势或按键。"
        "在手机上打开 App、查找联系人、设置项、商品或页面内容时，优先使用系统搜索、App 内搜索或页面上的搜索框；不要先靠连续滑动、翻页来碰运气。"
        "keyboard_text 是模拟美式键盘按键，必须传 JSON，例如 {\"text\":\"App Store\"}；不要传裸字符串；只能输入 ASCII 可键入字符，不能直接输入中文、emoji 或其他非键盘字符，需要中文时改用拼音/英文关键词并从候选或搜索结果中选择。"
        "点击要以最新截图为准，选择可见目标的中心点，并优先使用 coord_space:\"normalized\" 的 0..1 坐标；手机投屏/截图可能被缩放，pixel 坐标容易和实际触控坐标偏移。除非用户明确要求或坐标系已经校准，不要使用 coord_space:\"pixel\"。坐标不确定时先截图确认，不要用大概位置连续试点。"
        "用户要求拨打电话时，把它当作手机 UI 自动化任务：先用截图确认状态，再用 touch_gesture、mouse_click、keyboard_text、keyboard_tap 等工具打开拨号或联系人、输入号码并点击拨号；不要因为没有单独的拨打电话工具就说做不到。"
        "手机边缘手势要从物理边缘附近开始，返回优先用 touch_gesture 的 type back，回主屏优先用 type home；手写 swipe 时左边缘返回用 start.x=0.001 左右，底边回主页用 start.y=0.999 左右。";
    cfg.input_mode = "text";
    cfg.trigger_mode = "manual";
    cfg.vad_backend = "rknn";
    cfg.vad_model_path = "/oem/usr/model/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn";
    cfg.vad_helper_path = "/oem/usr/bin/rknn_vad";
    cfg.vad_speech_threshold = 0.5;
    cfg.silence_ms = 650;
    cfg.min_speech_ms = 300;
    cfg.voice_session_enabled = true;
    cfg.voice_followup_timeout_ms = 6000;
    cfg.voice_first_turn_timeout_ms = 10000;
    cfg.voice_max_turns = 0;
    cfg.voice_interrupt_on_wakeup = true;
    cfg.voice_streaming_tts_enabled = true;
    cfg.voice_tool_call_speech = true;
    cfg.voice_max_response_tokens = 400;
    cfg.max_iterations = -1;

    cfg.model.provider = "openrouter";
    cfg.model.model = "bytedance-seed/seed-2.0-lite";
    cfg.model.temperature = 0.2;
    cfg.model.max_tokens = 1000;

    cfg.tts.provider = "minimax-ws";
    cfg.tts.voice_id = "male-qn-qingse";
    cfg.tts.emotion = "happy";
    cfg.tts.speed = 1.0;

    cfg.stt.provider = "openai-whisper";
    cfg.stt.model = "whisper-1";

    cfg.audio.socket = "/run/audio_service/audio_service.sock";
    cfg.audio.sample_rate = 16000;
    cfg.audio.channels = 1;
    cfg.audio.bit_width = 16;

    cfg.hid.keyboard_device = "/dev/hidg0";
    cfg.hid.mouse_device = "/dev/hidg1";
    cfg.hid.frame_socket = "/run/frame_service/frame_service.sock";

    cfg.search.provider = "duckduckgo";
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

    *config = aiden::AgentToml();
    apply_default_agent_config(*config);
    if (file_exists(options.agent_config_path.c_str())) {
        aiden::AgentToml loaded;
        std::string err;
        if (aiden::load_agent_toml(options.agent_config_path.c_str(), loaded, &err)) {
            if (loaded.search.provider.empty()) {
                loaded.search.provider = "duckduckgo";
            }
            if (has_proxy_url(loaded.proxy) && loaded.proxy.no_proxy.empty()) {
                loaded.proxy.no_proxy = kDefaultNoProxy;
            }
            *config = loaded;
        } else if (load_error) {
            *load_error = err;
        }
    }
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

cJSON* config_to_json(const aiden::AgentToml& config) {
    cJSON* root = cJSON_CreateObject();

    cJSON* model = add_object(root, "model");
    cJSON_AddStringToObject(model, "provider", config.model.provider.c_str());
    cJSON_AddStringToObject(model, "api_key", config.model.api_key.c_str());
    cJSON_AddStringToObject(model, "model", config.model.model.c_str());
    cJSON_AddStringToObject(model, "base_url", config.model.base_url.c_str());
    cJSON_AddStringToObject(model, "token_env", config.model.token_env.c_str());
    cJSON_AddNumberToObject(model, "temperature", config.model.temperature);
    cJSON_AddNumberToObject(model, "max_tokens", config.model.max_tokens);

    cJSON* model_text = add_object(root, "model_text");
    cJSON_AddStringToObject(model_text, "provider", config.model_text.provider.c_str());
    cJSON_AddStringToObject(model_text, "api_key", config.model_text.api_key.c_str());
    cJSON_AddStringToObject(model_text, "model", config.model_text.model.c_str());
    cJSON_AddStringToObject(model_text, "base_url", config.model_text.base_url.c_str());
    cJSON_AddStringToObject(model_text, "token_env", config.model_text.token_env.c_str());
    cJSON_AddNumberToObject(model_text, "temperature", config.model_text.temperature);
    cJSON_AddNumberToObject(model_text, "max_tokens", config.model_text.max_tokens);

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

    cJSON* hid = add_object(root, "hid");
    cJSON_AddStringToObject(hid, "keyboard_device", config.hid.keyboard_device.c_str());
    cJSON_AddStringToObject(hid, "mouse_device", config.hid.mouse_device.c_str());
    cJSON_AddStringToObject(hid, "frame_socket", config.hid.frame_socket.c_str());

    cJSON* proxy = add_object(root, "proxy");
    cJSON_AddStringToObject(proxy, "http_proxy", config.proxy.http_proxy.c_str());
    cJSON_AddStringToObject(proxy, "https_proxy", config.proxy.https_proxy.c_str());
    cJSON_AddStringToObject(proxy, "all_proxy", config.proxy.all_proxy.c_str());
    cJSON_AddStringToObject(proxy, "no_proxy", config.proxy.no_proxy.c_str());

    cJSON* search = add_object(root, "search");
    cJSON_AddStringToObject(search, "provider", config.search.provider.c_str());
    cJSON_AddBoolToObject(search, "has_api_key", !config.search.api_key.empty());

    cJSON* agent = add_object(root, "agent");
    cJSON_AddStringToObject(agent, "instruction", config.instruction.c_str());
    cJSON_AddStringToObject(agent, "additional_prompt", config.additional_prompt.c_str());
    cJSON_AddStringToObject(agent, "input_mode", config.input_mode.c_str());
    cJSON_AddStringToObject(agent, "trigger_mode", config.trigger_mode.c_str());
    cJSON_AddStringToObject(agent, "vad_backend", config.vad_backend.c_str());
    cJSON_AddStringToObject(agent, "vad_model_path", config.vad_model_path.c_str());
    cJSON_AddStringToObject(agent, "vad_helper_path", config.vad_helper_path.c_str());
    cJSON_AddNumberToObject(agent, "vad_speech_threshold", config.vad_speech_threshold);
    cJSON_AddNumberToObject(agent, "silence_ms", config.silence_ms);
    cJSON_AddNumberToObject(agent, "min_speech_ms", config.min_speech_ms);
    cJSON_AddBoolToObject(agent, "voice_session_enabled", config.voice_session_enabled ? 1 : 0);
    cJSON_AddNumberToObject(agent, "voice_followup_timeout_ms", config.voice_followup_timeout_ms);
    cJSON_AddNumberToObject(agent, "voice_first_turn_timeout_ms", config.voice_first_turn_timeout_ms);
    cJSON_AddNumberToObject(agent, "voice_max_turns", config.voice_max_turns);
    cJSON_AddBoolToObject(agent, "voice_interrupt_on_wakeup", config.voice_interrupt_on_wakeup ? 1 : 0);
    cJSON_AddBoolToObject(agent, "voice_streaming_tts_enabled", config.voice_streaming_tts_enabled ? 1 : 0);
    cJSON_AddBoolToObject(agent, "voice_tool_call_speech", config.voice_tool_call_speech ? 1 : 0);
    cJSON_AddNumberToObject(agent, "voice_max_response_tokens", config.voice_max_response_tokens);
    cJSON_AddNumberToObject(agent, "max_iterations", config.max_iterations);

    return root;
}

cJSON* wifi_to_json(const aiden::WifiNetworkConfig& wifi) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "ssid", wifi.ssid.c_str());
    cJSON_AddStringToObject(root, "psk", wifi.psk.c_str());
    cJSON_AddStringToObject(root, "country", wifi.country.c_str());
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
    response.status_text = status_code == 400 ? "Bad Request" : "Internal Server Error";

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

void update_model_from_json(cJSON* obj, aiden::ModelToml* m) {
    if (!json_is_object(obj) || !m) return;
    set_json_str(&m->provider, obj, "provider");
    set_json_str(&m->model, obj, "model");
    set_json_str(&m->base_url, obj, "base_url");
    set_json_str(&m->api_key, obj, "api_key");
    set_json_str(&m->token_env, obj, "token_env");
    set_json_double(&m->temperature, obj, "temperature");
    set_json_int(&m->max_tokens, obj, "max_tokens");
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

    cJSON* hid = cJSON_GetObjectItem(root, "hid");
    if (json_is_object(hid)) {
        set_json_str(&config->hid.keyboard_device, hid, "keyboard_device");
        set_json_str(&config->hid.mouse_device, hid, "mouse_device");
        set_json_str(&config->hid.frame_socket, hid, "frame_socket");
    }

    cJSON* proxy = cJSON_GetObjectItem(root, "proxy");
    if (json_is_object(proxy)) {
        set_json_str(&config->proxy.http_proxy, proxy, "http_proxy");
        set_json_str(&config->proxy.https_proxy, proxy, "https_proxy");
        set_json_str(&config->proxy.all_proxy, proxy, "all_proxy");
        set_json_str(&config->proxy.no_proxy, proxy, "no_proxy");
    }

    cJSON* search = cJSON_GetObjectItem(root, "search");
    if (json_is_object(search)) {
        set_json_str(&config->search.provider, search, "provider");
        cJSON* key_item = cJSON_GetObjectItem(search, "api_key");
        if (json_is_string(key_item) && strlen(key_item->valuestring) > 0) {
            config->search.api_key = key_item->valuestring;
        }
    }

    cJSON* agent = cJSON_GetObjectItem(root, "agent");
    if (json_is_object(agent)) {
        set_json_str(&config->instruction, agent, "instruction");
        set_json_str(&config->additional_prompt, agent, "additional_prompt");
        set_json_str(&config->input_mode, agent, "input_mode");
        set_json_str(&config->trigger_mode, agent, "trigger_mode");
        set_json_str(&config->vad_backend, agent, "vad_backend");
        set_json_str(&config->vad_model_path, agent, "vad_model_path");
        set_json_str(&config->vad_helper_path, agent, "vad_helper_path");
        set_json_double(&config->vad_speech_threshold, agent, "vad_speech_threshold");
        set_json_int(&config->silence_ms, agent, "silence_ms");
        set_json_int(&config->min_speech_ms, agent, "min_speech_ms");
        set_json_bool(&config->voice_session_enabled, agent, "voice_session_enabled");
        set_json_int(&config->voice_followup_timeout_ms, agent, "voice_followup_timeout_ms");
        set_json_int(&config->voice_first_turn_timeout_ms, agent, "voice_first_turn_timeout_ms");
        set_json_int(&config->voice_max_turns, agent, "voice_max_turns");
        set_json_bool(&config->voice_interrupt_on_wakeup, agent, "voice_interrupt_on_wakeup");
        set_json_bool(&config->voice_streaming_tts_enabled, agent, "voice_streaming_tts_enabled");
        set_json_bool(&config->voice_tool_call_speech, agent, "voice_tool_call_speech");
        set_json_int(&config->voice_max_response_tokens, agent, "voice_max_response_tokens");
        set_json_int(&config->max_iterations, agent, "max_iterations");
    }
}

std::string validate_agent_config_for_save(const aiden::AgentToml& config) {
    if (config.vad_speech_threshold < 0.0 || config.vad_speech_threshold > 1.0) {
        return "vad_speech_threshold must be in range [0.0, 1.0]";
    }
    if (config.voice_followup_timeout_ms < 0) {
        return "voice_followup_timeout_ms must be >= 0";
    }
    if (config.voice_first_turn_timeout_ms < 0) {
        return "voice_first_turn_timeout_ms must be >= 0";
    }
    if (config.voice_max_turns < 0) {
        return "voice_max_turns must be >= 0";
    }
    if (config.voice_max_response_tokens < 0) {
        return "voice_max_response_tokens must be >= 0";
    }
    if (config.max_iterations < -1) {
        return "max_iterations must be >= -1";
    }
    return "";
}

void update_wifi_from_json(cJSON* root, aiden::WifiNetworkConfig* wifi) {
    if (!root || !wifi) {
        return;
    }
    set_json_str(&wifi->ssid, root, "ssid");
    set_json_str(&wifi->psk, root, "psk");
    set_json_str(&wifi->country, root, "country");
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

void schedule_agent_restart() {
    int rc = system("/etc/init.d/S53agent restart >/dev/null 2>&1 &");
    (void)rc;
}

CommandResult apply_wifi_config(const Options& options) {
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

    if (command_exists("wpa_cli")) {
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
        if (command_exists("udhcpc")) {
            CommandResult dhcp = run_shell_command("udhcpc -i " + shell_quote(options.wifi_interface) + " -n -q 2>&1");
            log << "$ udhcpc -i " << options.wifi_interface << " -n -q\n" << dhcp.output;
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
            log << "No supported DHCP client found (need udhcpc or dhclient).\n";
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

AgentLogSnapshot read_agent_log_snapshot() {
    AgentLogSnapshot snapshot;
    snapshot.path = kAgentLogPath;

    FILE* f = fopen(kAgentLogPath, "r");
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
    if (static_cast<size_t>(file_size) > kAgentLogReadSize) {
        offset = file_size - static_cast<long>(kAgentLogReadSize);
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

    if (contents.size() > kAgentLogDisplaySize) {
        snapshot.truncated = true;
    }
    snapshot.log = trim_for_display(contents, kAgentLogDisplaySize);
    return snapshot;
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

cJSON* agent_log_to_json(const AgentLogSnapshot& snapshot) {
    cJSON* root = cJSON_CreateObject();
    cJSON_AddStringToObject(root, "path", snapshot.path.c_str());
    cJSON_AddBoolToObject(root, "exists", snapshot.exists ? 1 : 0);
    cJSON_AddNumberToObject(root, "size_bytes", snapshot.size_bytes);
    cJSON_AddBoolToObject(root, "truncated", snapshot.truncated ? 1 : 0);
    cJSON_AddStringToObject(root, "log", snapshot.log.c_str());
    cJSON_AddStringToObject(root, "error", snapshot.error.c_str());
    return root;
}

cJSON* firmware_info_to_json(const Options& options) {
    cJSON* fw = cJSON_CreateObject();
    if (!file_exists(options.ota_state_path.c_str())) {
        cJSON_AddStringToObject(fw, "version", "");
        cJSON_AddStringToObject(fw, "build_time", "");
        cJSON_AddStringToObject(fw, "phase", "");
        return fw;
    }
    std::string contents = read_file_contents(options.ota_state_path.c_str());
    cJSON* state = cJSON_Parse(contents.c_str());
    if (!state) {
        cJSON_AddStringToObject(fw, "version", "");
        cJSON_AddStringToObject(fw, "build_time", "");
        cJSON_AddStringToObject(fw, "phase", "");
        return fw;
    }
    cJSON* version = cJSON_GetObjectItem(state, "current_version");
    cJSON* build_time = cJSON_GetObjectItem(state, "current_build_time");
    cJSON* phase = cJSON_GetObjectItem(state, "phase");
    cJSON_AddStringToObject(fw, "version",
                            json_is_string(version) ? version->valuestring : "");
    cJSON_AddStringToObject(fw, "build_time",
                            json_is_string(build_time) ? build_time->valuestring : "");
    cJSON_AddStringToObject(fw, "phase",
                            json_is_string(phase) ? phase->valuestring : "");
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

    cJSON* paths = add_object(root, "paths");
    cJSON_AddStringToObject(paths, "agent_config", options.agent_config_path.c_str());
    cJSON_AddStringToObject(paths, "wifi_config", options.wifi_config_path.c_str());
    cJSON_AddStringToObject(paths, "wifi_interface", options.wifi_interface.c_str());

    if (!config_error.empty()) {
        cJSON_AddStringToObject(root, "config_error", config_error.c_str());
    }
    if (!wifi_error.empty()) {
        cJSON_AddStringToObject(root, "wifi_error", wifi_error.c_str());
    }

    return make_json_ok(root);
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

    cJSON* config_json = cJSON_GetObjectItem(root, "config");
    if (json_is_object(config_json)) {
        update_config_from_json(config_json, &config);
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

    std::string validation_error = validate_agent_config_for_save(config);
    if (!validation_error.empty()) {
        std::cerr << "Invalid agent config: " << validation_error << "\n";
        cJSON_Delete(root);
        return make_json_error(400, validation_error);
    }

    cJSON_Delete(root);

    std::string save_error;
    if (!aiden::save_agent_toml(options.agent_config_path.c_str(), config, &save_error)) {
        return make_json_error(500, save_error);
    }

    bool should_save_wifi = !wifi.ssid.empty() || file_exists(options.wifi_config_path.c_str());
    if (should_save_wifi && !aiden::save_wifi_config(options.wifi_config_path.c_str(), wifi, &save_error)) {
        return make_json_error(500, save_error);
    }

    schedule_agent_restart();

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", 1);
    cJSON_AddStringToObject(response, "message", "config saved; agent restarting");
    cJSON_AddBoolToObject(response, "agent_restart_scheduled", 1);
    cJSON_AddItemToObject(response, "config", config_to_json(config));

    cJSON* paths = add_object(response, "paths");
    cJSON_AddStringToObject(paths, "agent_config", options.agent_config_path.c_str());
    cJSON_AddStringToObject(paths, "wifi_config", options.wifi_config_path.c_str());

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

ApiResponse handle_wifi_connect(const Options& options, const std::string& body) {
    cJSON* root = cJSON_Parse(body.c_str());
    if (!root) {
        return make_json_error(400, "invalid JSON body");
    }

    aiden::WifiNetworkConfig wifi;
    std::string ignore_error;
    load_current_wifi_config(options, &wifi, &ignore_error);
    update_wifi_from_json(root, &wifi);
    cJSON_Delete(root);

    if (wifi.ssid.empty()) {
        return make_json_error(400, "ssid is required");
    }

    std::string save_error;
    if (!aiden::save_wifi_config(options.wifi_config_path.c_str(), wifi, &save_error)) {
        return make_json_error(500, save_error);
    }

    CommandResult wifi_apply = apply_wifi_config(options);
    WifiRuntimeStatus wifi_status = query_wifi_status(options);

    cJSON* response = cJSON_CreateObject();
    cJSON_AddBoolToObject(response, "ok", wifi_apply.exit_code == 0);
    cJSON_AddItemToObject(response, "wifi", wifi_to_json(wifi));
    cJSON_AddItemToObject(response, "wifi_status", wifi_status_to_json(wifi_status));
    cJSON_AddStringToObject(response,
                            "message",
                            wifi_apply.exit_code == 0 ? "wifi connect attempted" : "wifi connect failed");

    cJSON* apply = add_object(response, "wifi_apply");
    cJSON_AddBoolToObject(apply, "ok", wifi_apply.exit_code == 0);
    cJSON_AddNumberToObject(apply, "exit_code", wifi_apply.exit_code);
    cJSON_AddStringToObject(apply, "output", trim_trailing_newlines(wifi_apply.output).c_str());
    if (wifi_apply.exit_code != 0) {
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

std::string benchmark_html_page() {
    return R"HTML(<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Aiden Benchmark</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#f5f5f5;padding:24px;color:#333}
h1{font-size:20px;margin-bottom:16px}
.card{background:#fff;border-radius:8px;padding:16px;margin-bottom:16px;box-shadow:0 1px 3px rgba(0,0,0,.1)}
select,button{font-size:14px;padding:8px 16px;border-radius:6px;border:1px solid #ddd}
button{background:#2563eb;color:#fff;border:none;cursor:pointer}
button:disabled{background:#94a3b8;cursor:not-allowed}
button:hover:not(:disabled){background:#1d4ed8}
.status{margin-top:8px;font-size:13px;color:#666}
.status.running{color:#d97706}
.status.done{color:#16a34a}
table{width:100%;border-collapse:collapse;font-size:13px;margin-top:8px}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid #eee}
th{font-weight:600;color:#666}
a{color:#2563eb;text-decoration:none}
a:hover{text-decoration:underline}
.pass{color:#16a34a;font-weight:600}
.fail{color:#dc2626;font-weight:600}
</style></head><body>
<h1>Aiden Benchmark</h1>
<div class="card">
<div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
<select id="suiteSelect"><option value="">Loading...</option></select>
<button id="runBtn" onclick="startRun()">Run</button>
<span id="statusText" class="status">idle</span>
</div></div>
<div class="card"><h2 style="font-size:15px;margin-bottom:8px">History</h2>
<table><thead><tr><th>Run ID</th><th>Suite</th><th>Passed</th><th>Failed</th><th>Report</th></tr></thead>
<tbody id="historyBody"><tr><td colspan="5">Loading...</td></tr></tbody></table></div>
<script>
var polling=null;
function load(){loadSuites();loadRuns();loadStatus()}
function loadSuites(){
fetch('/benchmark/suites').then(r=>r.json()).then(d=>{
var s=document.getElementById('suiteSelect');s.innerHTML='';
d.forEach(function(x){var o=document.createElement('option');o.value=x.path;o.textContent=x.name;s.appendChild(o)});
})}
function loadRuns(){
fetch('/benchmark/runs').then(r=>r.json()).then(d=>{
var tb=document.getElementById('historyBody');tb.innerHTML='';
if(!d.length){tb.innerHTML='<tr><td colspan="5">No runs yet</td></tr>';return}
d.forEach(function(r){
var t=r.totals||{};var suite=r.suite||'';
var sn=suite.split('/').pop().replace('.json','');
var tr=document.createElement('tr');
tr.innerHTML='<td>'+r.run_id+'</td><td>'+sn+'</td>'
+'<td class="pass">'+(t.passed||0)+'</td><td class="fail">'+(t.failed||0)+'</td>'
+'<td><a href="/benchmark/report/'+r.run_id+'">View</a></td>';
tb.appendChild(tr)});
})}
function loadStatus(){
fetch('/benchmark/status').then(r=>r.json()).then(d=>{
var el=document.getElementById('statusText');
var btn=document.getElementById('runBtn');
el.textContent=d.status||'idle';
el.className='status '+(d.status||'');
btn.disabled=(d.status==='running');
if(d.status==='running'&&!polling)polling=setInterval(pollStatus,3000);
})}
function pollStatus(){
fetch('/benchmark/status').then(r=>r.json()).then(d=>{
var el=document.getElementById('statusText');
var btn=document.getElementById('runBtn');
el.textContent=d.status||'idle';
el.className='status '+(d.status||'');
btn.disabled=(d.status==='running');
if(d.status!=='running'){clearInterval(polling);polling=null;loadRuns()}
})}
function startRun(){
var suite=document.getElementById('suiteSelect').value;
if(!suite){alert('Select a suite');return}
document.getElementById('runBtn').disabled=true;
document.getElementById('statusText').textContent='running';
document.getElementById('statusText').className='status running';
fetch('/benchmark/run',{method:'POST',headers:{'Content-Type':'application/json'},
body:JSON.stringify({suite:suite})}).then(r=>r.json()).then(function(){
polling=setInterval(pollStatus,3000)});
}
load();
</script></body></html>)HTML";
}

bool is_valid_proxy_scheme(const std::string& url) {
    if (url.empty()) return true;
    return starts_with(url, "http://") || starts_with(url, "https://") ||
           starts_with(url, "socks5://") || starts_with(url, "socks4://") ||
           starts_with(url, "socks4a://");
}

std::string provider_default_url(const std::string& provider) {
    if (provider == "openrouter") return "https://openrouter.ai/api/v1";
    if (provider == "openai" || provider == "openai-whisper") return "https://api.openai.com/v1";
    if (provider == "minimax") return "https://api.minimax.chat";
    if (provider == "tencent") return "https://asr.cloud.tencent.com";
    return "";
}

std::string build_curl_proxy_arg(const aiden::ProxyToml& proxy) {
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

std::string build_proxy_env_exports(const aiden::ProxyToml& proxy) {
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

    aiden::AgentToml current_config;
    std::string config_error;
    load_current_agent_config(options, &current_config, &config_error);
    std::string curl_proxy_arg = build_curl_proxy_arg(current_config.proxy);
    std::string curl_env_exports = build_proxy_env_exports(current_config.proxy);

    if (section == "proxy") {
        const char* proxy_keys[] = {"http_proxy", "https_proxy", "all_proxy", NULL};
        for (int i = 0; proxy_keys[i]; ++i) {
            cJSON* item = cJSON_GetObjectItem(values, proxy_keys[i]);
            std::string val = json_is_string(item) ? trim_copy(item->valuestring) : "";
            cJSON* r = cJSON_CreateObject();
            cJSON_AddStringToObject(r, "check", proxy_keys[i]);
            if (val.empty()) {
                cJSON_AddBoolToObject(r, "passed", 1);
                cJSON_AddStringToObject(r, "detail", "empty (ok)");
            } else if (is_valid_proxy_scheme(val)) {
                cJSON_AddBoolToObject(r, "passed", 1);
                cJSON_AddStringToObject(r, "detail", val.c_str());
            } else {
                cJSON_AddBoolToObject(r, "passed", 0);
                std::string msg = "invalid scheme in: " + val + " (allowed: http, https, socks5, socks4)";
                cJSON_AddStringToObject(r, "detail", msg.c_str());
                all_passed = false;
            }
            cJSON_AddItemToArray(results, r);
        }
    } else if (section == "model" || section == "tts" || section == "stt") {
        cJSON* provider_item = cJSON_GetObjectItem(values, "provider");
        cJSON* base_url_item = cJSON_GetObjectItem(values, "base_url");
        std::string provider = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
        std::string base_url = json_is_string(base_url_item) ? trim_copy(base_url_item->valuestring) : "";
        std::string url = base_url.empty() ? provider_default_url(provider) : base_url;

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

        cJSON* api_key_item = cJSON_GetObjectItem(values, "api_key");
        std::string api_key = json_is_string(api_key_item) ? trim_copy(api_key_item->valuestring) : "";
        cJSON* r2 = cJSON_CreateObject();
        cJSON_AddStringToObject(r2, "check", "api_key_present");
        cJSON_AddBoolToObject(r2, "passed", !api_key.empty() ? 1 : 0);
        cJSON_AddStringToObject(r2, "detail", api_key.empty() ? "api_key is empty" : "api_key is set");
        if (api_key.empty()) all_passed = false;
        cJSON_AddItemToArray(results, r2);

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

        const char* numeric_keys[] = {
            "silence_ms", "min_speech_ms",
            "voice_followup_timeout_ms", "voice_first_turn_timeout_ms",
            "voice_max_turns", "voice_max_response_tokens", NULL
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
    } else if (section == "search") {
        cJSON* provider_item = cJSON_GetObjectItem(values, "provider");
        std::string provider = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : "";
        cJSON* r = cJSON_CreateObject();
        cJSON_AddStringToObject(r, "check", "provider");
        const char* allowed[] = {"duckduckgo", "tavily", "google", "bing", NULL};
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

        cJSON* api_key_item = cJSON_GetObjectItem(values, "api_key");
        std::string api_key = json_is_string(api_key_item) ? trim_copy(api_key_item->valuestring) : "";
        cJSON* r2 = cJSON_CreateObject();
        cJSON_AddStringToObject(r2, "check", "api_key");
        cJSON_AddBoolToObject(r2, "passed", 1);
        if (provider == "duckduckgo") {
            cJSON_AddStringToObject(r2, "detail", api_key.empty() ? "not required for duckduckgo" : "set (not required for duckduckgo)");
        } else {
            cJSON_AddStringToObject(r2, "detail", api_key.empty() ? "empty (may be required for paid providers)" : "set");
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

    // ===== Benchmark routes =====
    if (request.method == "GET" && request.path == "/benchmark") {
        ApiResponse response;
        response.content_type = "text/html; charset=utf-8";
        response.body = benchmark_html_page();
        return response;
    }

    if (request.method == "GET" && request.path == "/benchmark/suites") {
        ApiResponse response;
        cJSON* root = cJSON_CreateArray();
        FILE* pipe = popen("find /userdata/agent/benchmark/suites -name '*.json' ! -name '._*' 2>/dev/null | sort", "r");
        if (pipe) {
            char line[512];
            while (fgets(line, sizeof(line), pipe)) {
                std::string path = trim_copy(line);
                if (path.empty()) continue;
                size_t pos = path.rfind('/');
                std::string name = (pos != std::string::npos) ? path.substr(pos + 1) : path;
                if (name.size() > 5) name = name.substr(0, name.size() - 5);
                cJSON* item = cJSON_CreateObject();
                cJSON_AddStringToObject(item, "name", name.c_str());
                cJSON_AddStringToObject(item, "path", path.c_str());
                cJSON_AddItemToArray(root, item);
            }
            pclose(pipe);
        }
        char* out = cJSON_PrintUnformatted(root);
        response.body = out ? out : "[]";
        if (out) free(out);
        cJSON_Delete(root);
        return response;
    }

    if (request.method == "GET" && request.path == "/benchmark/runs") {
        ApiResponse response;
        cJSON* root = cJSON_CreateArray();
        FILE* pipe = popen("ls -1d /userdata/agent/benchmark/runs/2* 2>/dev/null | sort -r | head -20", "r");
        if (pipe) {
            char line[512];
            while (fgets(line, sizeof(line), pipe)) {
                std::string dir = trim_copy(line);
                if (dir.empty()) continue;
                size_t pos = dir.rfind('/');
                std::string run_id = (pos != std::string::npos) ? dir.substr(pos + 1) : dir;
                std::string manifest_path = dir + "/manifest.json";
                cJSON* item = cJSON_CreateObject();
                cJSON_AddStringToObject(item, "run_id", run_id.c_str());
                FILE* mf = fopen(manifest_path.c_str(), "r");
                if (mf) {
                    fseek(mf, 0, SEEK_END);
                    long sz = ftell(mf);
                    fseek(mf, 0, SEEK_SET);
                    if (sz > 0 && sz < 64 * 1024) {
                        std::string buf(sz, '\0');
                        fread(&buf[0], 1, sz, mf);
                        cJSON* manifest = cJSON_Parse(buf.c_str());
                        if (manifest) {
                            cJSON* totals = cJSON_GetObjectItem(manifest, "totals");
                            if (totals) cJSON_AddItemToObject(item, "totals", cJSON_Duplicate(totals, 1));
                            cJSON* suite = cJSON_GetObjectItem(manifest, "suite_path");
                            if (suite) cJSON_AddStringToObject(item, "suite", suite->valuestring);
                            cJSON_Delete(manifest);
                        }
                    }
                    fclose(mf);
                }
                cJSON_AddItemToArray(root, item);
            }
            pclose(pipe);
        }
        char* out = cJSON_PrintUnformatted(root);
        response.body = out ? out : "[]";
        if (out) free(out);
        cJSON_Delete(root);
        return response;
    }

    if (request.method == "GET" && starts_with(request.path, "/benchmark/report/")) {
        std::string run_id = request.path.substr(18);
        std::string safe_id;
        for (char c : run_id) {
            if (isalnum(c) || c == '-' || c == '_') safe_id.push_back(c);
        }
        std::string path = "/userdata/agent/benchmark/runs/" + safe_id + "/report.html";
        ApiResponse response;
        response.content_type = "text/html; charset=utf-8";
        FILE* fp = fopen(path.c_str(), "r");
        if (fp) {
            fseek(fp, 0, SEEK_END);
            long sz = ftell(fp);
            fseek(fp, 0, SEEK_SET);
            if (sz > 0 && sz < 512 * 1024) {
                std::string buf(sz, '\0');
                fread(&buf[0], 1, sz, fp);
                response.body = std::move(buf);
            } else {
                response.status_code = 500;
                response.body = "report too large or empty";
            }
            fclose(fp);
        } else {
            response.status_code = 404;
            response.body = "<html><body><h2>Report not found</h2></body></html>";
        }
        return response;
    }

    if (request.method == "GET" && request.path == "/benchmark/status") {
        ApiResponse response;
        std::string path = "/userdata/agent/benchmark/state.json";
        FILE* fp = fopen(path.c_str(), "r");
        if (fp) {
            fseek(fp, 0, SEEK_END);
            long sz = ftell(fp);
            fseek(fp, 0, SEEK_SET);
            if (sz > 0 && sz < 8192) {
                std::string buf(sz, '\0');
                fread(&buf[0], 1, sz, fp);
                response.body = std::move(buf);
            }
            fclose(fp);
        } else {
            response.body = "{\"status\":\"idle\"}";
        }
        return response;
    }

    if (request.method == "POST" && request.path == "/benchmark/run") {
        ApiResponse response;
        cJSON* req_body = cJSON_Parse(request.body.c_str());
        if (!req_body || !json_is_string(cJSON_GetObjectItem(req_body, "suite"))) {
            response.status_code = 400;
            response.body = "{\"error\":\"missing suite field\"}";
            if (req_body) cJSON_Delete(req_body);
            return response;
        }
        std::string suite_path = cJSON_GetObjectItem(req_body, "suite")->valuestring;
        cJSON_Delete(req_body);
        // Write state.json
        std::string state = "{\"status\":\"running\",\"suite\":\"" + suite_path + "\"}";
        FILE* sf = fopen("/userdata/agent/benchmark/state.json", "w");
        if (sf) { fputs(state.c_str(), sf); fclose(sf); }

        aiden::AgentToml current_config;
        std::string config_error;
        load_current_agent_config(options, &current_config, &config_error);
        std::string proxy_env_exports = build_proxy_env_exports(current_config.proxy);

        // Launch runner in background
        std::string cmd = "cd /userdata/agent/benchmark && "
            "export OPENROUTER_API_KEY=$(grep 'api_key.*sk-or' /userdata/agent/agent.toml | sed 's/.*\"\\(sk-or[^\"]*\\)\".*/\\1/') && "
            + proxy_env_exports +
            "python3 -c 'import sys;sys.path.insert(0,\".\");from runner.main import cli;sys.exit(cli())' "
            "run --suite " + shell_quote(suite_path) + " "
            "--agent-url http://127.0.0.1:8080 "
            "--judge-model bytedance-seed/seed-2.0-lite "
            "> /tmp/benchmark_run.log 2>&1; "
            "echo '{\"status\":\"idle\"}' > /userdata/agent/benchmark/state.json &";
        system(cmd.c_str());
        response.body = "{\"ok\":true,\"status\":\"running\"}";
        return response;
    }
    // ===== End benchmark routes =====

    if (request.method == "GET" && request.path == "/api/agent/status") {
        return handle_get_agent_status();
    }

    if (request.method == "GET" && request.path == "/api/agent/logs") {
        return handle_get_agent_log();
    }

    if (request.method == "GET" && request.path == "/api/config") {
        return handle_get_config(options);
    }

    if (request.method == "POST" && request.path == "/api/config") {
        return handle_post_config(options, request.body);
    }

    if (request.method == "POST" && request.path == "/api/wifi/scan") {
        return handle_wifi_scan(options);
    }

    if (request.method == "POST" && request.path == "/api/wifi/connect") {
        return handle_wifi_connect(options, request.body);
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
              << " [--config=PATH] [--wifi-config=PATH] [--wifi-iface=NAME]" << std::endl;
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
        } else {
            return false;
        }
    }
    return true;
}

}

int main(int argc, char** argv) {
    Options options;
    if (!parse_args(argc, argv, &options)) {
        print_usage(argv[0]);
        return 1;
    }

    if (!is_allowed_bind_address(options.bind_address)) {
        std::cerr << "Refusing to bind config_web to " << options.bind_address
                  << "; allowed addresses are " << kUsbBindAddress
                  << " and " << kLoopbackBindAddress << std::endl;
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
