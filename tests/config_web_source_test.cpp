#include <doctest.h>

#include "config_web_test_assets.h"

#include <algorithm>
#include <fstream>
#include <set>
#include <sstream>
#include <string>
#include <utility>
#include <vector>

namespace {

struct LlmDiffMessage {
    std::string role;
    std::string content;
    std::vector<std::string> content_parts;
    std::vector<std::pair<std::string, std::string> > tool_calls;
    std::string tool_call_id;
};

struct LlmDiffOp {
    std::string kind;
    int old_index;
    int new_index;
};

struct LlmDiffStats {
    int added;
    int removed;
    int modified;
};

void append_llm_diff_line(std::string* text, const std::string& line) {
    if (!text->empty()) {
        text->append("\n");
    }
    text->append(line);
}

std::string llm_message_text(const LlmDiffMessage& message) {
    if (!message.content_parts.empty()) {
        std::string text;
        for (std::vector<std::string>::const_iterator it = message.content_parts.begin();
             it != message.content_parts.end(); ++it) {
            if (it->empty()) {
                continue;
            }
            append_llm_diff_line(&text, *it);
        }
        return text;
    }
    return message.content;
}

std::string llm_message_diff_text(const LlmDiffMessage& message) {
    std::string text = llm_message_text(message);
    for (std::vector<std::pair<std::string, std::string> >::const_iterator it = message.tool_calls.begin();
         it != message.tool_calls.end(); ++it) {
        append_llm_diff_line(&text, "tool_call " + it->first + " " + it->second);
    }
    if (!message.tool_call_id.empty()) {
        append_llm_diff_line(&text, "tool_call_id " + message.tool_call_id);
    }
    return text;
}

std::string serialize_llm_diff_message(const LlmDiffMessage& message) {
    return message.role + "\001" + llm_message_diff_text(message);
}

bool llm_messages_can_line_diff(const LlmDiffMessage* old_message, const LlmDiffMessage* new_message) {
    if (!old_message || !new_message) {
        return false;
    }
    if (old_message->role != new_message->role) {
        return false;
    }
    return llm_message_diff_text(*old_message) != llm_message_diff_text(*new_message);
}

bool llm_messages_can_line_diff(const LlmDiffMessage& old_message, const LlmDiffMessage& new_message) {
    return llm_messages_can_line_diff(&old_message, &new_message);
}

std::vector<LlmDiffOp> build_llm_message_diff_ops(const std::vector<LlmDiffMessage>& old_messages,
                                                  const std::vector<LlmDiffMessage>& new_messages) {
    // Mirror the frontend cost model without requiring Node or a browser in host-native tests.
    const int add_cost = 2;
    const int remove_cost = 2;
    const int modify_cost = 3;
    const int n = static_cast<int>(old_messages.size());
    const int m = static_cast<int>(new_messages.size());
    std::vector<std::string> old_serialized;
    std::vector<std::string> new_serialized;
    old_serialized.reserve(old_messages.size());
    new_serialized.reserve(new_messages.size());
    for (std::vector<LlmDiffMessage>::const_iterator it = old_messages.begin(); it != old_messages.end(); ++it) {
        old_serialized.push_back(serialize_llm_diff_message(*it));
    }
    for (std::vector<LlmDiffMessage>::const_iterator it = new_messages.begin(); it != new_messages.end(); ++it) {
        new_serialized.push_back(serialize_llm_diff_message(*it));
    }

    std::vector<std::vector<int> > cost(n + 1, std::vector<int>(m + 1, 0));
    for (int i = n - 1; i >= 0; --i) {
        cost[i][m] = cost[i + 1][m] + remove_cost;
    }
    for (int j = m - 1; j >= 0; --j) {
        cost[n][j] = cost[n][j + 1] + add_cost;
    }
    for (int i = n - 1; i >= 0; --i) {
        for (int j = m - 1; j >= 0; --j) {
            if (old_serialized[i] == new_serialized[j]) {
                cost[i][j] = cost[i + 1][j + 1];
            } else {
                int best = std::min(cost[i + 1][j] + remove_cost, cost[i][j + 1] + add_cost);
                if (llm_messages_can_line_diff(old_messages[i], new_messages[j])) {
                    best = std::min(best, cost[i + 1][j + 1] + modify_cost);
                }
                cost[i][j] = best;
            }
        }
    }

    std::vector<LlmDiffOp> ops;
    int i = 0;
    int j = 0;
    while (i < n && j < m) {
        if (old_serialized[i] == new_serialized[j] && cost[i][j] == cost[i + 1][j + 1]) {
            ops.push_back({"same", i, j});
            ++i;
            ++j;
        } else if (llm_messages_can_line_diff(old_messages[i], new_messages[j]) &&
                   cost[i][j] == cost[i + 1][j + 1] + modify_cost) {
            ops.push_back({"modified", i, j});
            ++i;
            ++j;
        } else if (cost[i][j] == cost[i + 1][j] + remove_cost) {
            ops.push_back({"removed", i, -1});
            ++i;
        } else {
            ops.push_back({"added", -1, j});
            ++j;
        }
    }
    while (i < n) {
        ops.push_back({"removed", i, -1});
        ++i;
    }
    while (j < m) {
        ops.push_back({"added", -1, j});
        ++j;
    }
    return ops;
}

LlmDiffStats compute_llm_diff_stats(const std::vector<LlmDiffMessage>& old_messages,
                                    const std::vector<LlmDiffMessage>& new_messages) {
    LlmDiffStats stats = {0, 0, 0};
    const std::vector<LlmDiffOp> ops = build_llm_message_diff_ops(old_messages, new_messages);
    for (std::vector<LlmDiffOp>::const_iterator it = ops.begin(); it != ops.end(); ++it) {
        if (it->kind == "added") {
            ++stats.added;
        } else if (it->kind == "removed") {
            ++stats.removed;
        } else if (it->kind == "modified") {
            ++stats.modified;
        }
    }
    return stats;
}

std::string changed_llm_diff_ops_summary(const std::vector<LlmDiffOp>& ops) {
    std::ostringstream out;
    bool first = true;
    for (std::vector<LlmDiffOp>::const_iterator it = ops.begin(); it != ops.end(); ++it) {
        if (it->kind == "same") {
            continue;
        }
        if (!first) {
            out << "|";
        }
        first = false;
        out << it->kind << ":" << (it->old_index < 0 ? "-" : std::to_string(it->old_index)) << "->"
            << (it->new_index < 0 ? "-" : std::to_string(it->new_index));
    }
    return out.str();
}

}  // namespace

TEST_CASE("config web agent tests do not reference legacy energy threshold") {
    const std::string path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream in(path.c_str());
    REQUIRE(in.good());

    std::ostringstream buffer;
    buffer << in.rdbuf();
    const std::string source = buffer.str();

    const std::string legacy_key = "\"energy_" "threshold\"";
    CHECK(source.find(legacy_key) == std::string::npos);
    CHECK(source.find("\"vad_speech_threshold\"") != std::string::npos);
}

TEST_CASE("config web delegates provider tests to the agent runtime") {
    const std::string path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream in(path.c_str());
    REQUIRE(in.good());

    std::ostringstream buffer;
    buffer << in.rdbuf();
    const std::string source = buffer.str();

    CHECK(source.find("run_agent_provider_config_test") != std::string::npos);
    CHECK(source.find("provider_default_url") == std::string::npos);
    CHECK(source.find("is_known_config_test_provider_type") == std::string::npos);
    CHECK(source.find("is_streaming_tts_provider") == std::string::npos);
    CHECK(source.find("flatten_provider_reference") == std::string::npos);
    CHECK(source.find("resolve_config_test_api_key") == std::string::npos);
    CHECK(source.find("no URL to test (provider unknown and base_url empty)") == std::string::npos);
    CHECK(source.find("api_key_valid") == std::string::npos);
}

TEST_CASE("config web exposes agent runtime status") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("\"agent_status\"") != std::string::npos);
    CHECK(source.find("\"public_port\"") != std::string::npos);
    CHECK(source.find("AIDEN_PUBLIC_AGENT_WEB_PORT") != std::string::npos);
    CHECK(source.find("\"/api/agent/status\"") != std::string::npos);
    CHECK(source.find("query_agent_status") != std::string::npos);
    CHECK(source.find("check_tcp_port") != std::string::npos);
    CHECK(source.find("/var/log/agent/agent.log") == std::string::npos);

    CHECK(html.find("id=\"agentProcessStatus\"") != std::string::npos);
    CHECK(html.find("agentProcessStatus") != std::string::npos);
    CHECK(html.find("agentPortStatus") != std::string::npos);
    CHECK(html.find("renderAgentStatus") != std::string::npos);
    CHECK(html.find("startup_error") != std::string::npos);
    CHECK(source.find("rfind_event(\"[agent] [supervisor] process_starting\")") !=
          std::string::npos);
    CHECK(source.find("rfind_event(\"[agent] [supervisor] binary_wait\")") !=
          std::string::npos);
    CHECK(source.find("boundary == log.size() || log[boundary] == ' '") !=
          std::string::npos);
}

TEST_CASE("config web listens on all interfaces by default") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string script_path = std::string(AIDEN_SOURCE_DIR) + "/overlay/etc/init.d/S56config_web";
    std::ifstream script_in(script_path.c_str());
    REQUIRE(script_in.good());

    std::ostringstream script_buffer;
    script_buffer << script_in.rdbuf();
    const std::string script = script_buffer.str();

    CHECK(source.find("std::string bind_address = \"0.0.0.0\"") != std::string::npos);
    CHECK(source.find("const char* kAnyBindAddress = \"0.0.0.0\"") != std::string::npos);
    CHECK(source.find("bind_address == kAnyBindAddress") != std::string::npos);
    CHECK(script.find("--bind=0.0.0.0") != std::string::npos);
    CHECK(script.find("CONFIG_DIR=${CONFIG_DIR:-/userdata/agent}") != std::string::npos);
    CHECK(script.find("CONFIG_PATH=${CONFIG_PATH:-$CONFIG_DIR/agent.toml}") != std::string::npos);
    CHECK(script.find(". \"$BOOT_CONF\"") != std::string::npos);
}

TEST_CASE("config web agent status review constraints") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("run_shell_command_with_timeout") != std::string::npos);
    CHECK(source.find("kAgentStatusCommandTimeoutMs") != std::string::npos);
    CHECK(source.find("agent status command timed out") != std::string::npos);
    CHECK(source.find("run_shell_command(std::string(kAgentInitScript) + \" status 2>&1\")") == std::string::npos);

    CHECK(source.find("lower.find(\"waiting for\")") == std::string::npos);

    CHECK(source.find("parse_agent_host") != std::string::npos);
    CHECK(source.find("status.port_host = parse_agent_host(addr)") != std::string::npos);
    CHECK(source.find("check_tcp_port(status.port_host, status.port)") != std::string::npos);
}

TEST_CASE("config web exposes live agent logs") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("\"/api/agent/logs\"") != std::string::npos);
    CHECK(source.find("\"/api/logs/export\"") != std::string::npos);
    CHECK(source.find("handle_get_agent_log") != std::string::npos);
    CHECK(source.find("handle_export_support_logs") != std::string::npos);
    CHECK(source.find("read_agent_log_snapshot") != std::string::npos);
    CHECK(source.find("latest_episode_yaml_path") != std::string::npos);
    CHECK(source.find("copy_regular_file_tail") != std::string::npos);
    CHECK(source.find("open_latest_llm_log") != std::string::npos);
    CHECK(source.find("copy_regular_fd_tail") != std::string::npos);
    CHECK(source.find("kSupportLogHttpMaxBytes") != std::string::npos);
    CHECK(source.find("fallback_tar_path") != std::string::npos);
    CHECK(source.find("archive is not gzip") != std::string::npos);
    CHECK(source.find("\"langfuse.yaml\"") != std::string::npos);
    CHECK(source.find("\"agent.log\"") != std::string::npos);
    CHECK(source.find("\"http.log\"") != std::string::npos);
    CHECK(source.find("\"agent_log\"") != std::string::npos);
    CHECK(source.find("\"size_bytes\"") != std::string::npos);
    CHECK(source.find("\"truncated\"") != std::string::npos);
    CHECK(source.find("tail -f") == std::string::npos);

    CHECK(html.find("Live Agent Log") != std::string::npos);
    CHECK(html.find("agentLogText") != std::string::npos);
    CHECK(html.find("agentLogMeta") != std::string::npos);
    CHECK(html.find("refreshAgentLog") != std::string::npos);
    CHECK(html.find("/api/logs/agent") != std::string::npos);
    CHECK(html.find("exportLogs") != std::string::npos);
    CHECK(html.find("fetch('/api/logs/support')") != std::string::npos);
    CHECK(html.find("Failed to export logs.") != std::string::npos);
    CHECK(html.find("/api/logs/support") != std::string::npos);
    CHECK(html.find("aiden-logs.tar.gz") != std::string::npos);
    CHECK(html.find("setInterval(()=>refreshAgentLog(false),2000)") != std::string::npos);
    CHECK(html.find("text.match(/^\\d{4}-") != std::string::npos);
    CHECK(html.find("severity[1] === 'ERROR'") != std::string::npos);
    CHECK(html.find("severity[1] === 'WARN'") != std::string::npos);
    CHECK(html.find("return 'log-error'") != std::string::npos);
    CHECK(html.find("return 'log-warn'") != std::string::npos);
    CHECK(html.find("if(text.indexOf(' [ERROR] ')") == std::string::npos);
}

TEST_CASE("config web links to ttyd browser terminal") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("id=\"terminalLink\"") != std::string::npos);
    CHECK(html.find("Terminal") != std::string::npos);
    CHECK(html.find("function configureTerminalLink(port = 8080)") != std::string::npos);
    CHECK(html.find("new URL(window.location.href)") != std::string::npos);
    CHECK(html.find("terminalUrl.hostname==='192.168.42.1'?'3000':String(port||8080)") != std::string::npos);
    CHECK(html.find("terminalUrl.pathname='/webtty/'") != std::string::npos);
    CHECK(html.find("configureTerminalLink(status.public_port||port)") != std::string::npos);
    CHECK(html.find("configureTerminalLink();") != std::string::npos);
    CHECK(html.find("let metaOk = true") != std::string::npos);
}

TEST_CASE("config web auto-scrolls agent logs only while pinned to bottom") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("agentLogPaused") == std::string::npos);
    CHECK(html.find("agentLogAutoScroll:true") != std::string::npos);
    CHECK(html.find("function isAgentLogAtBottom(el)") != std::string::npos);
    CHECK(html.find("function setAgentLogAutoScroll(enabled)") != std::string::npos);
    CHECK(html.find("function syncAgentLogAutoScroll()") != std::string::npos);
    CHECK(html.find("function toggleAgentLogAutoScroll()") != std::string::npos);
    CHECK(html.find("agentLogText.addEventListener('scroll',syncAgentLogAutoScroll)") != std::string::npos);
    CHECK(html.find("textEl.scrollTop = textEl.scrollHeight") != std::string::npos);
    CHECK(html.find("appState.agentLogAutoScroll ? 'primary' : 'ghost'") != std::string::npos);
    CHECK(html.find("appState.agentLogPaused&&!showBanner") == std::string::npos);
}

TEST_CASE("config web preserves agent log selection during background refresh") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("agentLogPendingSnapshot") != std::string::npos);
    CHECK(html.find("function agentLogSelectionActive(el)") != std::string::npos);
    CHECK(html.find("function applyPendingAgentLogSnapshotIfIdle()") != std::string::npos);
    CHECK(html.find("function refreshAgentLog(showBanner)") != std::string::npos);
    CHECK(html.find("document.addEventListener('selectionchange',applyPendingAgentLogSnapshotIfIdle)") != std::string::npos);
}

TEST_CASE("config web shares one localized test toast state") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("function beginTestToast(owner, view)") != std::string::npos);
    CHECK(html.find("function clearTestToast()") != std::string::npos);
    CHECK(html.find("function updateTestToast(token, view)") != std::string::npos);
}

TEST_CASE("config web colors live log lines by frontend classification") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("function classifyLine(line)") != std::string::npos);
    CHECK(html.find("function renderLogText(") != std::string::npos);
    CHECK(html.find("split(/\\r?\\n/)") != std::string::npos);
    CHECK(html.find("document.createElement('span')") != std::string::npos);
    CHECK(html.find("span.className = 'log-line '") != std::string::npos);
    CHECK(html.find("classifyLine(line)") != std::string::npos);

    CHECK(html.find("log-error") != std::string::npos);
    CHECK(html.find("log-warn") != std::string::npos);
    CHECK(html.find("log-update") != std::string::npos);
    CHECK(html.find("log-proxy") != std::string::npos);
    CHECK(html.find("log-cache") != std::string::npos);
    CHECK(html.find("log-http") != std::string::npos);
    CHECK(html.find("log-success") != std::string::npos);

    CHECK(html.find("failed") != std::string::npos);
    CHECK(html.find("error") != std::string::npos);
    CHECK(html.find("pq:") != std::string::npos);
    CHECK(html.find("panic") != std::string::npos);
    CHECK(html.find("fallback") != std::string::npos);
    CHECK(html.find("not found") != std::string::npos);
    CHECK(html.find("skip") != std::string::npos);
    CHECK(html.find("warn") != std::string::npos);
    CHECK(html.find("[update]") != std::string::npos);
    CHECK(html.find("[proxy]") != std::string::npos);
    CHECK(html.find("cache hit") != std::string::npos);
    CHECK(html.find("[gin]") != std::string::npos);
    CHECK(html.find("\\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\\b") != std::string::npos);
    CHECK(html.find("page created") != std::string::npos);
    CHECK(html.find("html updated") != std::string::npos);
    CHECK(html.find("new resource") != std::string::npos);

    CHECK(html.find(".log-line.log-error{color:#ff5555;border-left-color:#ff5555}") != std::string::npos);
    CHECK(html.find(".log-line.log-warn{color:#ffaa00;border-left-color:#ffaa00}") != std::string::npos);
    CHECK(html.find(".log-line.log-success{color:#00ff88}") != std::string::npos);
    CHECK(html.find(".log-line.log-proxy{color:#808080}") != std::string::npos);
    CHECK(html.find(".log-line.log-http{color:#00d9ff}") != std::string::npos);
    CHECK(html.find(".log-line.log-cache{color:#bb88ff}") != std::string::npos);
    CHECK(html.find(".log-line.log-update{color:#44bbff}") != std::string::npos);
}

TEST_CASE("config web exposes the LLM HTTP log viewer") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    std::string source = source_buffer.str();
    const std::string static_source_path =
        std::string(AIDEN_SOURCE_DIR) + "/src/config_web_static_assets.cpp";
    std::ifstream static_source_in(static_source_path.c_str());
    REQUIRE(static_source_in.good());
    std::ostringstream static_source_buffer;
    static_source_buffer << static_source_in.rdbuf();
    source += static_source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    const std::string llm_html = read_config_web_llm_asset_bundle();

    // Backend wires the routes and serves the standalone page.
    CHECK(source.find("\"/llm-logs\"") != std::string::npos);
    CHECK(source.find("\"/api/llm-logs\"") != std::string::npos);
    CHECK(source.find("/api/llm-logs/export/") != std::string::npos);
    CHECK(source.find("/api/llm-logs/import/") != std::string::npos);
    CHECK(source.find("handle_get_llm_logs") != std::string::npos);
    CHECK(source.find("handle_export_llm_log_file") != std::string::npos);
    CHECK(source.find("handle_import_llm_log_file") != std::string::npos);
    CHECK(source.find("is_llm_log_name") != std::string::npos);
    CHECK(source.find("serve_config_web_static_asset") != std::string::npos);
    CHECK(source.find("/api/llm-logs/file/") == std::string::npos);
    CHECK(source.find("handle_get_llm_log_file") == std::string::npos);
    CHECK(source.find("read_llm_log_file") == std::string::npos);
    CHECK(source.find("LlmLogReadResult") == std::string::npos);
    CHECK(source.find("kLlmLogMaxReadSize") == std::string::npos);

    // Main config page links to the viewer.
    CHECK(html.find("href=\"/llm-logs\"") != std::string::npos);

    // Sub-page implements grouping, full message rendering, and inline diff.
    CHECK(llm_html.find("function groupEntries") != std::string::npos);
    CHECK(llm_html.find("function renderMessages") != std::string::npos);
    CHECK(llm_html.find("function renderDiff") != std::string::npos);
    CHECK(llm_html.find("function renderToolCalls") != std::string::npos);
    CHECK(llm_html.find("downloadSelectedFile") != std::string::npos);
    CHECK(llm_html.find("handleImportSelection") != std::string::npos);
    CHECK(llm_html.find("importFileInput") != std::string::npos);
    CHECK(llm_html.find("confirm(") != std::string::npos);
    CHECK(llm_html.find("already exists. Replace it?") != std::string::npos);
    CHECK(llm_html.find("Export Raw") != std::string::npos);
    CHECK(llm_html.find("Import Raw") != std::string::npos);
    CHECK(llm_html.find("function streamLogEntries") != std::string::npos);
    CHECK(llm_html.find("function processLogChunk") != std::string::npos);
    CHECK(llm_html.find("function formatLogTimestamp") != std::string::npos);
    CHECK(llm_html.find("formatLogTimestamp(g.request.ts)") != std::string::npos);
    CHECK(llm_html.find("substring(11, 19)") == std::string::npos);
    CHECK(llm_html.find("response.body.getReader()") != std::string::npos);
    CHECK(llm_html.find("new TextDecoder()") != std::string::npos);
    CHECK(llm_html.find("fetch('/api/logs/llm/' + encodeURIComponent(name))") != std::string::npos);
    CHECK(llm_html.find("request('/api/llm-logs/file/'") == std::string::npos);
    CHECK(llm_html.find("data.content") == std::string::npos);
    CHECK(llm_html.find("tool_calls") != std::string::npos);
    CHECK(llm_html.find(".msg-head,.diff-msg-head{") != std::string::npos);
    CHECK(llm_html.find("font-size:12px;line-height:1.2") != std::string::npos);
    CHECK(llm_html.find("function renderMessageHead") != std::string::npos);
    CHECK(llm_html.find("class=\"msg-index\">#' + index") != std::string::npos);
    CHECK(llm_html.find("renderMessageHead('msg-head role-'") != std::string::npos);
    CHECK(llm_html.find("renderMessages([msg], false, 'response-msg')") != std::string::npos);
    CHECK(llm_html.find("function messageNeedsCollapse") != std::string::npos);
    CHECK(llm_html.find("diff-line.collapsed") != std::string::npos);
    CHECK(llm_html.find("msg-content diff-line") != std::string::npos);
    CHECK(llm_html.find("diffMsgBlock(newMsgs[op.newIndex], 'same',") != std::string::npos);
    CHECK(llm_html.find("diffMsgBlock(oldMsgs[op.oldIndex], 'removed',") != std::string::npos);
    CHECK(llm_html.find("diffMsgBlock(newMsgs[op.newIndex], 'added',") != std::string::npos);
    CHECK(llm_html.find("diffMsgBlock(newMsgs[op.newIndex], 'modified'") != std::string::npos);
    CHECK(llm_html.find("function buildLineDiffRows") != std::string::npos);
    CHECK(llm_html.find("function compactDiffContext") != std::string::npos);
    CHECK(llm_html.find("function renderLineDiffContent") != std::string::npos);
    CHECK(llm_html.find("modified++;") != std::string::npos);
    CHECK(llm_html.find("const addCost = 2, removeCost = 2, modifyCost = 3;") != std::string::npos);
    CHECK(llm_html.find("const oldDiffText = oldMsgs.map(messageDiffText);") != std::string::npos);
    CHECK(llm_html.find("const newDiffText = newMsgs.map(messageDiffText);") != std::string::npos);
    CHECK(llm_html.find("const canLineDiff = (i, j) =>") != std::string::npos);
    CHECK(llm_html.find("cost[i][j] === cost[i + 1][j + 1] + modifyCost") != std::string::npos);
    CHECK(llm_html.find("line-diff-row") != std::string::npos);
    CHECK(llm_html.find("renderMessageHead('diff-msg-head role-'") != std::string::npos);
    CHECK(llm_html.find("content + renderToolCalls(m)") != std::string::npos);
    CHECK(llm_html.find("function renderMessageContent") != std::string::npos);
    CHECK(llm_html.find("function renderContentPart") != std::string::npos);
    CHECK(llm_html.find("function imageUrlFromPart") != std::string::npos);
    CHECK(llm_html.find("function isRenderableImageDataUrl") != std::string::npos);
    CHECK(llm_html.find("data:image/") != std::string::npos);
    CHECK(llm_html.find(";base64,") != std::string::npos);
    CHECK(llm_html.find("class=\"msg-image\"") != std::string::npos);
    CHECK(llm_html.find("<img src=\"' + escAttr(url)") != std::string::npos);

    // Messages view appends a Response section so request and response render
    // on the same screen. Covers OpenAI JSON, SSE streams, and raw fallbacks.
    CHECK(llm_html.find("function renderMessagesView") != std::string::npos);
    CHECK(llm_html.find("function renderDiffView") != std::string::npos);
    CHECK(llm_html.find("function renderResponseBlock") != std::string::npos);
    CHECK(llm_html.find("function extractResponseMessage") != std::string::npos);
    CHECK(llm_html.find("renderDiff(requestMessages(prevReq), requestMessages(req)) + renderResponseBlock(g)") != std::string::npos);
    CHECK(llm_html.find("function requestMessages") != std::string::npos);
    CHECK(llm_html.find("function normalizeAnthropicMessage") != std::string::npos);
    CHECK(llm_html.find("part.type === 'tool_use'") != std::string::npos);
    CHECK(llm_html.find("p.type === 'tool_result'") != std::string::npos);
    CHECK(llm_html.find("p.source && p.source.type === 'base64'") != std::string::npos);
    CHECK(llm_html.find("response-section") != std::string::npos);
}

TEST_CASE("config web LLM diff pairs adjacent message replacements") {
    const std::vector<LlmDiffMessage> old_messages = {
        {"system", "system prompt"},
        {"user", "old user one"},
        {"user", "old user two"},
        {"assistant", "unchanged assistant"},
    };
    const std::vector<LlmDiffMessage> new_messages = {
        {"system", "system prompt"},
        {"user", "new user one"},
        {"user", "new user two"},
        {"assistant", "unchanged assistant"},
    };

    const std::vector<LlmDiffOp> ops = build_llm_message_diff_ops(old_messages, new_messages);
    CHECK(changed_llm_diff_ops_summary(ops) == "modified:1->1|modified:2->2");

    const LlmDiffStats stats = compute_llm_diff_stats(old_messages, new_messages);
    CHECK(stats.added == 0);
    CHECK(stats.removed == 0);
    CHECK(stats.modified == 2);
}

TEST_CASE("config web LLM diff mirror compares normalized message text") {
    LlmDiffMessage old_message;
    old_message.role = "user";
    old_message.content_parts.push_back("old text part");

    LlmDiffMessage new_message;
    new_message.role = "user";
    new_message.content_parts.push_back("new text part");

    CHECK(llm_messages_can_line_diff(old_message, new_message));

    const std::vector<LlmDiffOp> ops = build_llm_message_diff_ops({old_message}, {new_message});
    CHECK(changed_llm_diff_ops_summary(ops) == "modified:0->0");
}

TEST_CASE("config web LLM diff mirror compares tool metadata") {
    LlmDiffMessage old_tool_call;
    old_tool_call.role = "assistant";
    old_tool_call.content = "same";
    old_tool_call.tool_calls.push_back(std::make_pair("lookup", "{\"q\":\"old\"}"));

    LlmDiffMessage new_tool_call = old_tool_call;
    new_tool_call.tool_calls[0].second = "{\"q\":\"new\"}";

    CHECK(llm_messages_can_line_diff(old_tool_call, new_tool_call));
    CHECK(changed_llm_diff_ops_summary(build_llm_message_diff_ops({old_tool_call}, {new_tool_call})) == "modified:0->0");

    LlmDiffMessage old_tool_result;
    old_tool_result.role = "tool";
    old_tool_result.content = "same";
    old_tool_result.tool_call_id = "call-old";

    LlmDiffMessage new_tool_result = old_tool_result;
    new_tool_result.tool_call_id = "call-new";

    CHECK(llm_messages_can_line_diff(old_tool_result, new_tool_result));
    CHECK(changed_llm_diff_ops_summary(build_llm_message_diff_ops({old_tool_result}, {new_tool_result})) == "modified:0->0");
}

TEST_CASE("config web LLM diff mirror rejects missing messages") {
    LlmDiffMessage message;
    message.role = "user";
    message.content = "content";

    CHECK_FALSE(llm_messages_can_line_diff(static_cast<const LlmDiffMessage*>(0), &message));
    CHECK_FALSE(llm_messages_can_line_diff(&message, static_cast<const LlmDiffMessage*>(0)));
}

TEST_CASE("config web exposes audio archive switch") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("section-audio_archive") != std::string::npos);
    CHECK(html.find("Name: \"audio_archive\"") != std::string::npos);
    CHECK(html.find("{Key: \"enabled\", Widget: WidgetBoolean") != std::string::npos);
    CHECK(html.find("audioArchiveModeHint") != std::string::npos);
    CHECK(html.find("isAudioArchiveAvailable") != std::string::npos);
    CHECK(html.find("agent.input_mode = stt") != std::string::npos);
    CHECK(html.find("testSection('audio_archive')") == std::string::npos);
    CHECK(html.find("[audio_archive]") != std::string::npos);
    CHECK(html.find("save STT voice recording WAV") != std::string::npos);
}

TEST_CASE("config web exposes quick capture settings") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) +
                                    "/src/agent/internal/agent/config_meta.go";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());
    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();
    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("Name: \"quick_capture\"") != std::string::npos);
    CHECK(html.find("section-quick_capture") != std::string::npos);
    CHECK(source.find("Name: \"quick_capture\"") != std::string::npos);
    CHECK(html.find("screen_memory_ttl") != std::string::npos);
    CHECK(html.find("GPIO32/GPIO33 wakeup remains independent") != std::string::npos);
}

TEST_CASE("config web exposes persistent frame STREAMON settings") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) +
                                    "/src/agent/internal/agent/config_meta.go";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());
    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();
    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("Name: \"frame_service\"") != std::string::npos);
    CHECK(html.find("section-frame_service") != std::string::npos);
    CHECK(html.find("keep_streamon") != std::string::npos);
}

TEST_CASE("config web docs list the model fields") {
    const std::string doc_path = std::string(AIDEN_SOURCE_DIR) + "/docs/04-agent/configuration.md";
    std::ifstream doc_in(doc_path.c_str());
    REQUIRE(doc_in.good());

    std::ostringstream doc_buffer;
    doc_buffer << doc_in.rdbuf();
    const std::string doc = doc_buffer.str();

    const char* model_fields[] = {
        "provider",
        "model",
        "api_key",
        "temperature",
        "max_response_tokens",
        "context_window",
        "model_max_output_tokens",
        NULL,
    };
    for (int i = 0; model_fields[i]; ++i) {
        CHECK_MESSAGE(doc.find(model_fields[i]) != std::string::npos, model_fields[i]);
    }
    CHECK(doc.find("`context_window = 0` means auto-discover") != std::string::npos);
}

TEST_CASE("config web exposes ota update and live ota logs") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("\"/api/ota/update\"") != std::string::npos);
    CHECK(source.find("\"/api/ota/check-now\"") != std::string::npos);
    CHECK(source.find("\"/api/ota/logs\"") != std::string::npos);
    CHECK(source.find("handle_post_ota_update") != std::string::npos);
    CHECK(source.find("handle_get_ota_log") != std::string::npos);
    CHECK(source.find("read_ota_log_snapshot") != std::string::npos);
    CHECK(source.find("kOtaWebUpdateLogPath") != std::string::npos);
    CHECK(source.find("/userdata/ota/config_web_ota_update.log") != std::string::npos);
    CHECK(source.find("kOtaWebUpdateLogMaxBytes") != std::string::npos);
    CHECK(source.find("/var/log/ota/ota.log") != std::string::npos);
    CHECK(source.find("AIDEN_CONFIG_WEB_OTA_HEALTH_LOG") != std::string::npos);
    CHECK(source.find("\"ota_health_log\"") != std::string::npos);
    CHECK(source.find("/oem/usr/bin/ota") != std::string::npos);
    CHECK(source.find(" update") != std::string::npos);
    CHECK(source.find("kOtaWebUpdateLockPath") != std::string::npos);
    CHECK(source.find("flock(lock_fd, LOCK_EX | LOCK_NB)") != std::string::npos);
    CHECK(source.find("ota update already running") != std::string::npos);
    CHECK(source.find("prepare_ota_update_log_file") != std::string::npos);
    CHECK(source.find("ota_log_start_size_bytes") != std::string::npos);
    CHECK(source.find("static_cast<double>(start_size_bytes)") != std::string::npos);
    CHECK(source.find("cJSON_AddItemToObject(response, \"ota_log\"") == std::string::npos);
    CHECK(source.find("echo '[config_web] ota update requested'") == std::string::npos);
    CHECK(source.find("close(lock_fd)") != std::string::npos);
    CHECK(source.find("tail -f") == std::string::npos);

    CHECK(html.find("OTA Update") != std::string::npos);
    CHECK(html.find("fwActions") != std::string::npos);
    CHECK(html.find("otaUpdateBtn") != std::string::npos);
    CHECK(html.find("actionBanner") != std::string::npos);
    CHECK(html.find("actionDetails") != std::string::npos);
    const std::string::size_type fw_actions_pos = html.find("id=\"fwActions\"");
    const std::string::size_type ota_button_pos = html.find("id=\"otaUpdateBtn\"");
    const std::string::size_type action_banner_pos = html.find("id=\"actionBanner\"");
    const std::string::size_type action_details_pos = html.find("id=\"actionDetails\"");
    const std::string::size_type ota_log_panel_pos = html.find("id=\"otaLogPanel\"");
    const std::string::size_type wifi_heading_pos = html.find("Wi-Fi Configuration");
    REQUIRE(fw_actions_pos != std::string::npos);
    REQUIRE(ota_button_pos != std::string::npos);
    REQUIRE(action_banner_pos != std::string::npos);
    REQUIRE(action_details_pos != std::string::npos);
    REQUIRE(ota_log_panel_pos != std::string::npos);
    REQUIRE(wifi_heading_pos != std::string::npos);
    CHECK(fw_actions_pos < ota_button_pos);
    CHECK(ota_button_pos < wifi_heading_pos);
    CHECK(action_banner_pos < wifi_heading_pos);
    CHECK(action_details_pos < wifi_heading_pos);
    CHECK(action_details_pos < ota_log_panel_pos);
    CHECK(ota_log_panel_pos < wifi_heading_pos);
    CHECK(html.find("OTA Live Log") != std::string::npos);
    CHECK(html.find("otaLogPanel") != std::string::npos);
    CHECK(html.find("id=\"otaLogPanel\" class=\"ota-log-panel\"") != std::string::npos);
    CHECK(html.find("id=\"otaLogPanel\" class=\"card ota-log-panel\"") == std::string::npos);
    CHECK(html.find(".ota-log-panel{display:none;margin-top:12px}") != std::string::npos);
    CHECK(html.find("showOtaLogPanel") != std::string::npos);
    CHECK(html.find("updateActionDetailsVisibility") != std::string::npos);
    CHECK(html.find("if(!appState.otaLogVisible&&!showBanner){return;}") != std::string::npos);
    CHECK(html.find("otaLogPending:false") != std::string::npos);
    CHECK(html.find("otaLogStartSize:0") != std::string::npos);
    CHECK(html.find("otaLogHasNewProgress") != std::string::npos);
    CHECK(html.find("extractOtaExitCode") != std::string::npos);
    CHECK(html.find("[config_web] [ota] update_exited exit_code=") != std::string::npos);
    CHECK(html.find("OTA update failed (rc=") != std::string::npos);
    CHECK(html.find("Recent OTA log:\\n") != std::string::npos);
    CHECK(html.find("setOtaLogPending") != std::string::npos);
    CHECK(html.find("OTA update started, waiting for log output...") != std::string::npos);
    CHECK(html.find("setOtaLogPending(t('ota.update_started_waiting'),Number(payload.ota_log_start_size_bytes||0));") != std::string::npos);
    CHECK(html.find("renderOtaLog(snapshot, {preservePending:true})") != std::string::npos);
    CHECK(html.find("payload.ota_health_log") != std::string::npos);
    CHECK(html.find("extractOtaExitCode((payload.ota_log||{}).log||'')") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate(){const btn=byId('otaUpdateBtn');setOtaLogPending(") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate(){const btn=byId('otaUpdateBtn');showOtaLogPanel();") == std::string::npos);
    CHECK(html.find("function hideOtaLogPanel()") != std::string::npos);
    CHECK(html.find("function setDetails(text,options)") != std::string::npos);
    CHECK(html.find("if(!(options&&options.keepOtaLog)){hideOtaLogPanel();}") != std::string::npos);
    CHECK(html.find("setDetails('',{keepOtaLog:true})") != std::string::npos);
    CHECK(html.find("setDetails(t('ota.recent_log',{log:String(text||'').slice(-4000)}),{keepOtaLog:true})") != std::string::npos);
    CHECK(html.find("setDetails(err.message,{keepOtaLog:true})") != std::string::npos);
    CHECK(html.find("otaLogText") != std::string::npos);
    CHECK(html.find("otaLogMeta") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate") != std::string::npos);
    CHECK(html.find("refreshOtaLog") != std::string::npos);
    CHECK(html.find("/api/ota/updates") != std::string::npos);
    CHECK(html.find("/api/ota/check-now") == std::string::npos);
    CHECK(html.find("/api/ota/status") != std::string::npos);
    CHECK(html.find("setInterval(()=>refreshOtaLog(false),2000)") != std::string::npos);
}

TEST_CASE("config web exposes running firmware version and ota health status separately") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("\"health_status\"") != std::string::npos);
    CHECK(source.find("\"health_error\"") != std::string::npos);
    CHECK(source.find("\"previous_version\"") != std::string::npos);
    CHECK(source.find("current_slot_from_cmdline") != std::string::npos);
    CHECK(source.find("running_slot_matches_target") != std::string::npos);

    CHECK(html.find("fwHealth") != std::string::npos);
    CHECK(html.find("renderFirmwareInfo") != std::string::npos);
    CHECK(html.find("OTA status") != std::string::npos);
    CHECK(html.find("health_error") != std::string::npos);
    CHECK(html.find("previous_version") != std::string::npos);
}

TEST_CASE("ota documentation index references current docs paths") {
    const std::string doc_path = std::string(AIDEN_SOURCE_DIR) + "/docs/08-ota/README.md";
    std::ifstream doc_in(doc_path.c_str());
    REQUIRE(doc_in.good());

    std::ostringstream doc_buffer;
    doc_buffer << doc_in.rdbuf();
    const std::string doc = doc_buffer.str();

    const std::vector<std::pair<std::string, std::string> > links = {
        std::make_pair("[OTA Architecture and Runtime](architecture.md)", "architecture.md"),
        std::make_pair("[OTA Key Management](key-management.md)", "key-management.md"),
        std::make_pair("[Device Acceptance Process](device-acceptance.md)", "device-acceptance.md"),
        std::make_pair("[A/B and `abctl` Verification](verification.md)", "verification.md"),
        std::make_pair("[OTA Dedicated Storage Partition](no-space-plan.md)", "no-space-plan.md"),
        std::make_pair("[GitHub Proxy Configuration](ota-github-proxy.md)", "ota-github-proxy.md"),
        std::make_pair("[External Developer Guide](ota-external-developers.md)", "ota-external-developers.md"),
        std::make_pair("[Distribution Quick Examples](ota-quick-examples.md)", "ota-quick-examples.md"),
        std::make_pair("[Release Channel Strategy](ota-release-channels.md)", "ota-release-channels.md"),
    };
    for (std::vector<std::pair<std::string, std::string> >::const_iterator it = links.begin();
         it != links.end(); ++it) {
        CHECK(doc.find(it->first) != std::string::npos);
        const std::string target_path = std::string(AIDEN_SOURCE_DIR) + "/docs/08-ota/" + it->second;
        std::ifstream target_in(target_path.c_str());
        CHECK(target_in.good());
    }
}

TEST_CASE("config web exposes screenshot pruning config fields") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("{Key: \"screenshot_keep_n\"") != std::string::npos);
    CHECK(html.find("{Key: \"screenshot_prune_interval\"") != std::string::npos);
    CHECK(html.find("{Key: \"screen_stable_timeout_ms\"") != std::string::npos);
    CHECK(html.find("{Key: \"screen_stable_ms\"") != std::string::npos);
    CHECK(html.find("{Key: \"screen_stable_diff_threshold\"") != std::string::npos);
}

TEST_CASE("config web exposes model spec override fields") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("{Key: \"max_response_tokens\"") != std::string::npos);
    CHECK(html.find("{Key: \"context_window\"") != std::string::npos);
    CHECK(html.find("{Key: \"model_max_output_tokens\"") != std::string::npos);
    CHECK(html.find("0 = auto") != std::string::npos);
}

TEST_CASE("config web exposes brave search provider") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("\"brave\"") != std::string::npos);
    CHECK(source.find("\"brave-free\"") != std::string::npos);
    CHECK(source.find("provider == \"brave\" || provider == \"brave-free\"") != std::string::npos);
    // The search provider enum now lives in the agent config metadata (the
    // single source of truth the config web UI consumes via config-meta),
    // not a hard-coded table in the HTML. Compatibility aliases stay in the
    // parser, but UI metadata only exposes canonical provider values.
    const std::string meta_path = std::string(AIDEN_SOURCE_DIR) + "/src/agent/internal/agent/config_meta.go";
    std::ifstream meta_in(meta_path.c_str());
    REQUIRE(meta_in.good());
    std::ostringstream meta_buffer;
    meta_buffer << meta_in.rdbuf();
    const std::string meta = meta_buffer.str();
    CHECK(meta.find("\"brave-free\"") == std::string::npos);
    CHECK(html.find("search:{provider:['duckduckgo','brave','brave-free','tavily']}") == std::string::npos);
    CHECK(source.find("const char* allowed[] = {\"duckduckgo\", \"brave\", \"brave-free\", \"tavily\", NULL};") != std::string::npos);
}

TEST_CASE("config web renders finite choice fields as selects") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("field.widget==='select'||field.range") != std::string::npos);
    CHECK(html.find("return 'select'") != std::string::npos);
    CHECK(html.find("WidgetSelect") != std::string::npos);

    // tts.model moved onto a [tts_providers] record, so it has no flat input on
    // the page at all -- the provider dialog renders it from the metadata. The
    // widget choice is still asserted there: providerRecordFieldsHtml emits a
    // <select> only for a field whose metadata carries enum options, and
    // tts_providers.model is a text widget with a conditional selectWhen.
    CHECK(html.find("<input id=\"tts_model\"") == std::string::npos);
    CHECK(html.find("<select id=\"tts_model\"") == std::string::npos);
    CHECK(html.find("function providerRecordFieldsHtml(section,record)") != std::string::npos);

    CHECK(html.find("input,select,textarea") != std::string::npos);
    CHECK(html.find("input:focus,select:focus,textarea:focus") != std::string::npos);
    CHECK(html.find("input:disabled,select:disabled,textarea:disabled") != std::string::npos);
    // Select options are now built from the agent config-meta payload rather
    // than a hard-coded table; the UI fetches /api/config/meta and derives
    // enum/range options in buildConfigMeta().
    CHECK(html.find("let selectFieldOptions=") != std::string::npos);
    CHECK(html.find("function buildConfigMeta(meta)") != std::string::npos);
    CHECK(html.find("loadConfigMeta") != std::string::npos);
    CHECK(html.find("'/api/config/schema'") != std::string::npos);
    CHECK(html.find("hydrateSelectOptions") != std::string::npos);
    CHECK(html.find("filterSelectOptions") != std::string::npos);
    CHECK(html.find("option.providers") != std::string::npos);
    CHECK(html.find("conditionalSelectRules") != std::string::npos);
    CHECK(html.find("syncConditionalSelectFields") != std::string::npos);
    CHECK(html.find("field.selectWhen") != std::string::npos);
    CHECK(html.find("conditionalPlaceholderRules") != std::string::npos);
    CHECK(html.find("applyConditionalPlaceholders") != std::string::npos);
    CHECK(html.find("field.placeholderWhen") != std::string::npos);
    CHECK(html.find("ensureSelectOption") != std::string::npos);
    CHECK(html.find("let fieldDefaults=") != std::string::npos);
    CHECK(html.find("function fieldDefaultPlaceholder") != std::string::npos);
    CHECK(html.find("applyDefaultPlaceholder(name,field)") != std::string::npos);
    CHECK(html.find("getAttribute('placeholder')") != std::string::npos);
    CHECK(html.find("Default: ") != std::string::npos);
    // rangeOptions is now invoked with metadata-provided bounds, not literals.
    CHECK(html.find("rangeOptions(field.range.min,field.range.max,field.range.step") != std::string::npos);
    // The legacy hard-coded option table must be gone.
    CHECK(html.find("const selectFieldOptions={") == std::string::npos);
}

TEST_CASE("config web keeps the keyboard layout selector but drops probe calibration") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());
    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    // The keyboard_layout selector stays: users pick the layout manually.
    CHECK(html.find("{Key: \"keyboard_layout\", Widget: WidgetSelect") != std::string::npos);

    // The probe/calibration flow is fully removed from both source and UI.
    CHECK(source.find("/api/hid/keyboard-layout/probe") == std::string::npos);
    CHECK(source.find("/api/tools/keyboard_layout_probe") == std::string::npos);
    CHECK(source.find("/api/hid/keyboard-layout/calibrate") == std::string::npos);
    CHECK(source.find("keyboard_layout_from_probe_observation") == std::string::npos);
    CHECK(html.find("keyboardLayoutProbeBtn") == std::string::npos);
    CHECK(html.find("keyboardLayoutCalibrationModal") == std::string::npos);
    CHECK(html.find("keyboardLayoutObserved") == std::string::npos);
    CHECK(html.find("startKeyboardLayoutProbe") == std::string::npos);
    CHECK(html.find("applyKeyboardLayoutCalibration") == std::string::npos);
}

TEST_CASE("config web degrades gracefully when config metadata is unavailable") {
    const std::string html = read_config_web_asset_bundle();

    // A failed config-meta fetch must not abort the whole page: Wi-Fi, logs and
    // polling stay live, only the agent.toml sections are disabled. The init
    // path therefore tracks a metaOk flag instead of returning early.
    CHECK(html.find("let metaOk = true") != std::string::npos);
    CHECK(html.find("metaOk = false") != std::string::npos);
    // The early `return` that killed Wi-Fi/log init on meta failure is gone.
    CHECK(html.find("setDetails(err.message);return;}hydrateSelectOptions") == std::string::npos);
    // Wi-Fi scan and agent-log refresh run regardless of metaOk.
    CHECK(html.find("awaitscanWifi(false);awaitrefreshAgentLog(false);") != std::string::npos);
    // Only agent.toml sections are disabled on failure; system_env stays editable.
    CHECK(html.find("function disableAgentConfigEditing()") != std::string::npos);
    CHECK(html.find("if(card.id==='section-system_env')return;") != std::string::npos);
    CHECK(html.find("if(!metaOk)disableAgentConfigEditing();") != std::string::npos);

    // The metadata endpoint fails closed with a 503, which must carry the
    // correct HTTP status text (not the generic 500 reason phrase).
    const std::string cpp_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream cpp_in(cpp_path.c_str());
    REQUIRE(cpp_in.good());
    std::ostringstream cpp_buffer;
    cpp_buffer << cpp_in.rdbuf();
    const std::string source = cpp_buffer.str();
    CHECK(source.find("case 404: response.status_text = \"Not Found\";") != std::string::npos);
    CHECK(source.find("case 503: response.status_text = \"Service Unavailable\";") != std::string::npos);
}

TEST_CASE("config web collapses wifi list after a successful connection") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("wifiListExpanded:false") != std::string::npos);
    CHECK(html.find("function connectedWifiSsid()") != std::string::npos);
    CHECK(html.find("function visibleWifiNames(names)") != std::string::npos);
    CHECK(html.find("function toggleWifiListExpanded()") != std::string::npos);
    CHECK(html.find("Show other Wi-Fi") != std::string::npos);
    CHECK(html.find("Hide other Wi-Fi") != std::string::npos);
    CHECK(html.find("const visibleNames=visibleWifiNames(names);") != std::string::npos);
    CHECK(html.find("names.forEach(function(name)") == std::string::npos);
    CHECK(html.find("function initialReadyMessage(metaOk)") != std::string::npos);
    CHECK(html.find("t('config.wifi_connected')") != std::string::npos);
    CHECK(html.find("setBanner(initialReadyMessage(metaOk),!metaOk);") != std::string::npos);
}

TEST_CASE("config web keeps saved wifi networks at the top when expanded") {
    const std::string html = read_config_web_asset_bundle();

    const std::string::size_type saved_pos =
        html.find("savedWifiNetworks().slice().sort(function(a,b)");
    const std::string::size_type connected_pos =
        html.find("addWifiName(names,seen,connectedWifiSsid());", saved_pos);
    const std::string::size_type scanned_pos =
        html.find("(appState.networks||[]).forEach(function(name)", connected_pos);
    REQUIRE(saved_pos != std::string::npos);
    REQUIRE(connected_pos != std::string::npos);
    REQUIRE(scanned_pos != std::string::npos);
    CHECK(saved_pos < connected_pos);
    CHECK(connected_pos < scanned_pos);

    CHECK(html.find("return names.filter(function(name){return name===connected;});") !=
          std::string::npos);
    CHECK(html.find("return saved[name]||name===connected;") == std::string::npos);
    CHECK(html.find("const hiddenCount=Math.max(0,totalCount-visibleCount);") !=
          std::string::npos);
    CHECK(html.find("totalCount<=1") != std::string::npos);
    CHECK(html.find("t('wifi.show_others',{count:hiddenCount})") != std::string::npos);
}

TEST_CASE("config web shows saved wifi modal and connects automatically") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("left.dataset.action=savedNet?'connect-saved-wifi':'open-wifi-modal'") != std::string::npos);
    CHECK(html.find("function connectSavedWifi(ssid)") != std::string::npos);
    CHECK(html.find("openWifiModal(ssid,false);if(!saved)return;") != std::string::npos);
    CHECK(html.find("setTimeout(function(){connectWifi(ssid);},0);") !=
          std::string::npos);
    CHECK(html.find("saved.psk") == std::string::npos);
    CHECK(html.find("wifiPasswordInput').value=''") != std::string::npos);
    CHECK(html.find("if(psk!==undefined){requestBody.psk=psk;}") != std::string::npos);
    CHECK(html.find("if(focusPassword!==false){byId('wifiPasswordInput').focus();}") !=
          std::string::npos);
    CHECK(html.find("btn.disabled=true;btn.textContent=t('wifi.connecting');") != std::string::npos);
    CHECK(html.find("btn.disabled=false;btn.textContent=t('action.connect');") != std::string::npos);
    CHECK(html.find("const connected=!!payload.ok&&connectedWifiSsid()===ssid;") !=
          std::string::npos);
    CHECK(html.find("if(connected){closeWifiModal();") != std::string::npos);
    CHECK(html.find("t('wifi.connect_failed_detail',{ssid:ssid})") != std::string::npos);
    CHECK(html.find("renderWifiList();closeWifiModal();") == std::string::npos);
}

TEST_CASE("config web does not show a star for disconnected saved wifi") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("state='✓';") != std::string::npos);
    CHECK(html.find("state='★';") == std::string::npos);
    CHECK(html.find("forget.textContent=t('action.forget');") != std::string::npos);
}

TEST_CASE("config web hides successful forget details but reports runtime apply failure") {
    const std::string html = read_config_web_asset_bundle();

    const std::string::size_type forget_start =
        html.find("async function forgetWifi(ssid)");
    const std::string::size_type forget_end =
        html.find("export { connectedWifiSsid", forget_start);
    REQUIRE(forget_start != std::string::npos);
    REQUIRE(forget_end != std::string::npos);
    const std::string forget_source =
        html.substr(forget_start, forget_end - forget_start);
    CHECK(forget_source.find("if(payload.ok!==false)") != std::string::npos);
    CHECK(forget_source.find("setBanner(t('wifi.forgot',{ssid:ssid}),false);setDetails('');") !=
          std::string::npos);
    CHECK(forget_source.find("t('wifi.forgot_apply_failed',{ssid:ssid})") !=
          std::string::npos);
    CHECK(forget_source.find("payload.wifi_apply&&payload.wifi_apply.output") !=
          std::string::npos);
    CHECK(forget_source.find("t('wifi.forget_apply_failed_detail')") !=
          std::string::npos);
}

TEST_CASE("config web tolerates metadata sections without rendered controls") {
    const std::string html = read_config_web_asset_bundle();

    // Config metadata may expose sections that do not have a static editor
    // card yet. Page-level locking must skip those missing DOM nodes instead
    // of aborting initialization.
    CHECK(html.find("const form=document.querySelector('[data-config-section=\"'+section+'\"]');if(form){form.querySelectorAll('[data-section=\"'+section+'\"]')") != std::string::npos);
    CHECK(html.find("form.querySelectorAll('[data-section-lock]')") != std::string::npos);
    CHECK(html.find("const btn=byId('save-'+section);if(btn)btn.disabled=actualLocked;") != std::string::npos);
    CHECK(html.find("const card=byId('section-'+section);if(card)card.classList.remove('editing');") != std::string::npos);
    CHECK(html.find("const section = target.dataset.sectionTarget;") != std::string::npos);
    CHECK(html.find("data-action=\"enter-edit-section\" data-section=\"model\"") == std::string::npos);
}

TEST_CASE("config web tolerates metadata fields without rendered controls") {
    const std::string html = read_config_web_asset_bundle();

    // A new metadata field may be added before the static HTML form is updated.
    // Editing and saving an existing section must skip missing controls and
    // preserve any already-loaded value for those fields.
    CHECK(html.find("if(!el)return;snap[item[0]]=") != std::string::npos);
    CHECK(html.find("if(!el)return;if(snap[item[0]]!==undefined)") != std::string::npos);
    CHECK(html.find("function readSection(section){const values=Object.assign({},(appState.config&&appState.config[section])||{});") != std::string::npos);
    CHECK(html.find("if(!el)return;const field=el.closest?el.closest('.field'):el.parentNode;") != std::string::npos);
    CHECK(html.find("if(field&&field.classList.contains('hidden')){return;}") != std::string::npos);
    CHECK(html.find("if(type==='number'){values[key]=0;}") == std::string::npos);
}

TEST_CASE("config web preserves loaded secret values when password inputs are left blank") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("const values=Object.assign({},(appState.config&&appState.config[section])||{});") != std::string::npos);
    CHECK(html.find("function isSecretField(el)") != std::string::npos);
    CHECK(html.find("if(isSecretField(el)&&raw===''){return;}") != std::string::npos);
    CHECK(html.find("if(el.type==='password'&&raw===''){delete values[key];return;}") == std::string::npos);
}


TEST_CASE("config web updates dependent field visibility from selected values") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find(".field.hidden{display:none}") != std::string::npos);
    CHECK(html.find("function setFieldVisible(section,key,visible)") != std::string::npos);
    CHECK(html.find("function applyFieldVisibility(preserveUnknown,changedPath)") != std::string::npos);
    // Visibility is now driven by declarative rules from config-meta
    // (visibleWhen), evaluated generically rather than hard-coded per field.
    CHECK(html.find("visibilityRules.forEach") != std::string::npos);
    CHECK(html.find("function evalCondition(cond)") != std::string::npos);
    CHECK(html.find("function evalRule(rule)") != std::string::npos);
    CHECK(html.find("function bindFieldVisibility()") != std::string::npos);
    CHECK(html.find("document.addEventListener('change',function(event)") != std::string::npos);
    CHECK(html.find("applyFieldVisibility(false,path)") != std::string::npos);
    CHECK(html.find("fillConfigForm(config") != std::string::npos);
    // Filling the form has to re-evaluate visibility, or a loaded config shows
    // fields its own values should have hidden. Scoped to fillConfigForm's body
    // rather than anchored on its closing brace, which is not part of the rule.
    const size_t fill_form_at = html.find("fillConfigForm(config");
    REQUIRE(fill_form_at != std::string::npos);
    CHECK(html.substr(fill_form_at, 500).find("if(!deferProviderScopedFields)applyFieldVisibility(true)") != std::string::npos);
    // The legacy imperative visibility chain must be gone.
    CHECK(html.find("setFieldVisible('model','base_url',modelProvider!=='openrouter')") == std::string::npos);
    CHECK(html.find("const sttTencent=sttProvider==='tencent'") == std::string::npos);
}

TEST_CASE("config web fills Tencent ASR STT defaults from metadata") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("function isTencentASRProvider(value)") != std::string::npos);
    CHECK(html.find("function fieldDefaultValue(section,key,fallback)") != std::string::npos);
    CHECK(html.find("function applySTTTencentASRDefaults()") != std::string::npos);
    CHECK(html.find("applySTTTencentASRDefaults();applyAudioArchiveAvailability();") != std::string::npos);
    // The Tencent credential set moved onto an [stt_providers] record, so the
    // defaults are filled on the record's fields inside the provider dialog.
    // Reading [stt] provider here would never match: that field holds a record
    // NAME now, so the fill would silently stop happening.
    CHECK(html.find("isTencentASRProvider(fieldValue('stt_providers','type'))") != std::string::npos);
    CHECK(html.find("byId('stt_providers_'+key)") != std::string::npos);
    CHECK(html.find("fieldDefaultValue('stt_providers',key,fallback)") != std::string::npos);
    CHECK(html.find("fillIfEmpty('region','ap-shanghai')") != std::string::npos);
    CHECK(html.find("fillIfEmpty('engine_model_type','16k_zh')") != std::string::npos);
    CHECK(html.find("p==='tencent-asr'||p==='tencent_asr'||p==='tencent'") != std::string::npos);
}

TEST_CASE("config web uses board-side recording for STT tests") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("id=\"test-stt\"") != std::string::npos);
    CHECK(html.find("data-action=\"toggle-stt-test\"") != std::string::npos);
    CHECK(html.find("testSection('stt')") == std::string::npos);
    CHECK(html.find("function toggleSTTTest()") != std::string::npos);
    CHECK(html.find("function startSTTTest()") != std::string::npos);
    CHECK(html.find("function stopSTTTest()") != std::string::npos);
    CHECK(html.find("'/api/config/test/stt/start'") != std::string::npos);
    CHECK(html.find("'/api/config/test/stt/stop'") != std::string::npos);
    CHECK(html.find("method: 'POST'") != std::string::npos);
    CHECK(html.find("activate microphone") != std::string::npos);
    CHECK(html.find("recognition result") != std::string::npos);

    CHECK(source.find("/api/config/test/stt/start") != std::string::npos);
    CHECK(source.find("/api/config/test/stt/stop") != std::string::npos);
}

TEST_CASE("config web exposes a single system env editor backed by the env file") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    const std::string parser_path = std::string(AIDEN_SOURCE_DIR) + "/src/system_env_parser.cpp";
    std::ifstream parser_in(parser_path.c_str());
    REQUIRE(parser_in.good());

    std::ostringstream parser_buffer;
    parser_buffer << parser_in.rdbuf();
    const std::string parser = parser_buffer.str();

    CHECK(source.find("system_env_path = \"/userdata/system/env\"") != std::string::npos);
    CHECK(source.find("save_system_env_with_proxy") == std::string::npos);
    CHECK(source.find("cJSON* proxy = add_object(root, \"proxy\")") == std::string::npos);
    CHECK(source.find("SystemProxy* system_proxy") == std::string::npos);
    CHECK(source.find("section == \"proxy\"") == std::string::npos);
    CHECK(source.find("proxy_keys") == std::string::npos);
    CHECK(source.find("clear_agent_proxy_for_system_env") == std::string::npos);
    CHECK(source.find("loaded.proxy") == std::string::npos);
    CHECK(source.find("\"system_env\"") != std::string::npos);
    CHECK(source.find("read_file_contents(options.system_env_path.c_str(), kMaxSystemEnvSize)") != std::string::npos);
    CHECK(source.find("--system-env=") != std::string::npos);
    CHECK(source.find("load_system_env_proxy(tmp_template") == std::string::npos);
    CHECK(source.find("sh -n") == std::string::npos);
    CHECK(parser.find("parse_system_env_content") != std::string::npos);
    CHECK(parser.find("has_duplicate_proxy_scheme") != std::string::npos);
    CHECK(parser.find("has_proxy_whitespace") != std::string::npos);
    CHECK(parser.find("has_embedded_proxy_assignment") != std::string::npos);
    CHECK(parser.find("duplicate scheme") != std::string::npos);
    CHECK(parser.find("proxy URL contains whitespace") != std::string::npos);

    CHECK(html.find("system_env") != std::string::npos);
    CHECK(html.find("system_env_content") != std::string::npos);
    CHECK(html.find("textarea.system-env-editor{min-height:180px}") != std::string::npos);
    CHECK(html.find("<textarea id=\"system_env_content\" class=\"system-env-editor\"") != std::string::npos);
    CHECK(html.find("id=\"comment-system_env\"") != std::string::npos);
    CHECK(html.find("data-action=\"toggle-system-env-comment\"") != std::string::npos);
    CHECK(html.find("function toggleSystemEnvComment()") != std::string::npos);
    CHECK(html.find("function handleSystemEnvEditorKeydown(event)") != std::string::npos);
    CHECK(html.find("event.key==='/'") != std::string::npos);
    CHECK(html.find("function isCommentedSystemEnvLine(line){return /^\\s*#/.test(line);}") != std::string::npos);
    CHECK(html.find("function replaceSystemEnvSelection(el,range,text)") != std::string::npos);
    CHECK(html.find("document.execCommand('insertText',false,text)") != std::string::npos);
    CHECK(html.find("saveSystemEnv") != std::string::npos);
    CHECK(html.find("system_proxy") == std::string::npos);
    CHECK(html.find("section-proxy") == std::string::npos);
    CHECK(html.find("proxy_http_proxy") == std::string::npos);
    CHECK(html.find("proxy:[[") == std::string::npos);
    CHECK(html.find("Proxy example") == std::string::npos);
    CHECK(html.find("http_proxy=http://127.0.0.1:7890") == std::string::npos);
    CHECK(html.find("NO_PROXY=localhost,127.0.0.1,::1") == std::string::npos);

    const size_t agent_card = html.find("id=\"section-device\"");
    const size_t microsd_card = html.find("<h2>microSD Configuration</h2>");
    const size_t telemetry_section = html.find("id=\"section-telemetry\"");
    const size_t system_env_section = html.find("id=\"section-system_env\"");
    REQUIRE(agent_card != std::string::npos);
    REQUIRE(microsd_card != std::string::npos);
    REQUIRE(telemetry_section != std::string::npos);
    REQUIRE(system_env_section != std::string::npos);
    CHECK(agent_card < telemetry_section);
    CHECK(telemetry_section < microsd_card);
    CHECK(microsd_card < system_env_section);
    CHECK(html.find("<h2>Storage (microSD)</h2>") == std::string::npos);
    CHECK(html.find("<div class=\"card section-card\" id=\"section-system_env\"") != std::string::npos);
}

TEST_CASE("config web documents provider-specific reasoning effort levels") {
    const std::string html = read_config_web_asset_bundle();

    // The hint must mention that minimal is OpenRouter and Ark, and that Ark
    // does not support none. The select options themselves come from config-meta
    // and are filtered per provider.
    const std::string hint =
        "Empty = auto (disable reasoning only for no-tool requests). "
        "Levels are provider-specific: minimal is OpenRouter and Volcengine Ark only, "
        "none is not supported by Ark.";
    CHECK(html.find(hint) != std::string::npos);

    CHECK(html.find("'config.fields.'+section+'.'+field.key+'.'+part") != std::string::npos);
}

TEST_CASE("config web exposes telemetry settings section") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("std::string provider_original = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : \"\";") != std::string::npos);
    CHECK(source.find("std::string provider = lowercase_copy(provider_original);") != std::string::npos);
    CHECK(source.find("public_key_env") == std::string::npos);
    CHECK(source.find("secret_key_env") == std::string::npos);
    CHECK(source.find("section == \"telemetry\"") != std::string::npos);

    CHECK(html.find("section-telemetry") != std::string::npos);
    CHECK(html.find("<h3>[telemetry]</h3>") != std::string::npos);
    CHECK(html.find("{Key: \"base_url\", Widget: WidgetText") != std::string::npos);
    CHECK(html.find("{Key: \"public_key\", Widget: WidgetText") != std::string::npos);
    CHECK(html.find("{Key: \"secret_key\", Widget: WidgetText") != std::string::npos);
    CHECK(html.find("{Key: \"tags\", Widget: WidgetList") != std::string::npos);
    CHECK(html.find("public_key_env") == std::string::npos);
    CHECK(html.find("secret_key_env") == std::string::npos);
    CHECK(html.find("parseListValue") != std::string::npos);
    CHECK(html.find("field.widget==='textarea'||field.widget==='list'") != std::string::npos);
}

TEST_CASE("config web keeps termination policy settings internal") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("section-termination_policy") == std::string::npos);
    CHECK(html.find("termination_policy_enabled") == std::string::npos);
    CHECK(html.find("enterEditSection('termination_policy')") == std::string::npos);
    CHECK(html.find("save-termination_policy") == std::string::npos);
}

TEST_CASE("config web does not expose benchmark settings section") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("add_object(root, \"benchmark\")") == std::string::npos);
    CHECK(source.find("config.benchmark") == std::string::npos);
    CHECK(source.find("cJSON_GetObjectItem(root, \"benchmark\")") == std::string::npos);
    CHECK(source.find("section == \"benchmark\"") == std::string::npos);
    CHECK(source.find("defaults to bytedance-seed/seed-2.0-lite") == std::string::npos);

    CHECK(html.find("section-benchmark") == std::string::npos);
    CHECK(html.find("<h3>[benchmark]</h3>") == std::string::npos);
    CHECK(html.find("benchmark_judge_model") == std::string::npos);
    CHECK(html.find("benchmark_api_key") == std::string::npos);
    CHECK(html.find("benchmark_benchmark_dir") == std::string::npos);
    CHECK(html.find("sectionValues['has_'+key]") != std::string::npos);
    CHECK(html.find("save-benchmark") == std::string::npos);
    CHECK(html.find("enterEditSection('benchmark')") == std::string::npos);
}

TEST_CASE("config web exposes log settings section") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("section-log") != std::string::npos);
    CHECK(html.find("<h3>[log]</h3>") != std::string::npos);
    CHECK(html.find("{Key: \"llm_http_retention_days\"") != std::string::npos);
    CHECK(html.find("save-log") != std::string::npos);
    CHECK(html.find("data-action=\"enter-edit-section\" data-section-target=\"log\"") != std::string::npos);
}

TEST_CASE("config web exposes live activity settings section") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("config-update --config=") != std::string::npos);
    CHECK(source.find("config.live_activity.relay_url") == std::string::npos);
    CHECK(source.find("has_relay_api_key") == std::string::npos);
    CHECK(source.find("has_private_key_pem") == std::string::npos);

    CHECK(html.find("section-live_activity") != std::string::npos);
    CHECK(html.find("<h3>[live_activity]</h3>") != std::string::npos);
    CHECK(html.find("live_activity_relay_url") == std::string::npos);
    CHECK(html.find("live_activity_relay_api_key") == std::string::npos);
    CHECK(html.find("{Key: \"board_id\"") == std::string::npos);
    CHECK(html.find("{Key: \"phone_id\"") == std::string::npos);
    CHECK(html.find("live_activity_private_key_path") == std::string::npos);
    CHECK(html.find("live_activity_private_key_pem") == std::string::npos);
    CHECK(html.find("live_activity_timeout_sec") == std::string::npos);
    CHECK(html.find("function isSecretField(el)") != std::string::npos);
    CHECK(html.find("save-live_activity") != std::string::npos);
    CHECK(html.find("data-action=\"enter-edit-section\" data-section-target=\"live_activity\"") != std::string::npos);

}

TEST_CASE("config web does not restart ota for system env changes") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("kAgentInitScript") != std::string::npos);
    CHECK(source.find("kOtaInitScript") == std::string::npos);
    CHECK(source.find("schedule_agent_restart") != std::string::npos);
    CHECK(source.find("schedule_ota_restart") == std::string::npos);
    CHECK(source.find("bool system_env_changed = original_system_env != updated_system_env;") == std::string::npos);
    CHECK(source.find("system env saved; services restarting") == std::string::npos);
    CHECK(source.find("system env saved; agent restarting") != std::string::npos);
    CHECK(source.find("config saved; agent and ota restarting") == std::string::npos);
}

TEST_CASE("config web DHCP invokes dhcpcd without hook") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    // Must invoke dhcpcd for DHCP
    CHECK(source.find("dhcpcd -n ") != std::string::npos);
    // Must NOT reference the removed aiden.script hook
    CHECK(source.find("-s /etc/udhcpc/aiden.script") == std::string::npos);
    // Should use -n for foreground one-shot DHCP
    CHECK(source.find("dhcpcd -n \" + shell_quote(options.wifi_interface)") != std::string::npos);
}

TEST_CASE("config web waits for an IPv4 lease before confirming a connection") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    // A dedicated helper polls for the DHCP-assigned IPv4 address; `dhcpcd -n`
    // returns immediately so the lease is not present when it exits.
    CHECK(source.find("bool wait_for_ip(const Options& options") != std::string::npos);
    // The helper must consult both wpa_cli status and ifconfig for the address.
    CHECK(source.find("find_key_value_line(status.output, \"ip_address\")") != std::string::npos);
    CHECK(source.find("extract_ifconfig_ipv4(status.output)") != std::string::npos);
    // DHCP success must depend on actually obtaining an address, not just the
    // dhcpcd exit code.
    CHECK(source.find("dhcp.exit_code == 0 && wait_for_ip(options, log, 15)") != std::string::npos);
}

TEST_CASE("config web wifi status falls back to ifconfig for the IPv4 address") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    // wpa_cli status never reports ip_address= on this device (DHCP is run by
    // dhcpcd, not wpa_supplicant), so query_wifi_status must back-fill the IPv4
    // address from ifconfig; otherwise the connect-confirmation gate rolls back
    // a working connection because status.ip_address is always empty.
    CHECK(source.find("extract_ifconfig_ipv4(ifc.output)") != std::string::npos);
    // The fallback is gated on a missing wpa_cli ip_address and an associated
    // (COMPLETED) link, so a stale ifconfig inet cannot falsely confirm.
    CHECK(source.find("if (status.ip_address.empty() && status.connected && command_exists(\"ifconfig\"))") !=
          std::string::npos);
}

TEST_CASE("config web restores previous wifi config when final apply is not confirmed") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("auto is_connected_to_target = [&](const WifiRuntimeStatus& status)") != std::string::npos);
    CHECK(source.find("if (final_apply.exit_code != 0 || !is_connected_to_target(wifi_status))") != std::string::npos);
    CHECK(source.find("failed to restore previous wifi config") != std::string::npos);
    CHECK(source.find("saved Wi-Fi was not confirmed; restored previous Wi-Fi config.") != std::string::npos);
}

TEST_CASE("config web fails closed when wifi rollback snapshot cannot be read") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("bool read_file_contents_checked(const char* path") != std::string::npos);
    CHECK(source.find("stat(options.wifi_config_path.c_str(), &st)") != std::string::npos);
    CHECK(source.find("static_cast<size_t>(st.st_size) + 1") != std::string::npos);
    CHECK(source.find("if (!read_file_contents_checked(options.wifi_config_path.c_str()") != std::string::npos);
    CHECK(source.find("failed to read existing wifi config for rollback") != std::string::npos);
    CHECK(source.find("had_original_config ? read_file_contents(options.wifi_config_path.c_str())") == std::string::npos);
}

TEST_CASE("config web reports wifi forget runtime apply failures") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("const bool applied = wifi_apply.exit_code == 0;") != std::string::npos);
    CHECK(source.find("cJSON_AddBoolToObject(response, \"ok\", applied ? 1 : 0);") != std::string::npos);
    CHECK(source.find("wifi network forgotten but failed to apply runtime changes") != std::string::npos);
    CHECK(source.find("cJSON_AddBoolToObject(apply, \"ok\", applied ? 1 : 0);") != std::string::npos);
}

TEST_CASE("config web clears legacy wifi fields for explicit empty network lists") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("if (json_is_array(networks))") != std::string::npos);
    CHECK(source.find("wifi->networks.clear();") != std::string::npos);
    CHECK(source.find("aiden::sync_legacy_wifi_fields(wifi);") != std::string::npos);
}

TEST_CASE("config web requires reboot for USB HID configuration changes") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html = read_config_web_asset_bundle();

    CHECK(source.find("\"device_type\"") != std::string::npos);
    CHECK(source.find("kUsbHidInitScript = \"/etc/init.d/S49usbhid\"") == std::string::npos);
    CHECK(source.find("schedule_usbhid_restart") == std::string::npos);
    CHECK(source.find("usbhid_restart_required") != std::string::npos);
    CHECK(source.find("reboot_required") != std::string::npos);
    CHECK(source.find("schedule_usb_reenumerate") == std::string::npos);
    CHECK(source.find("schedule_reboot") != std::string::npos);
    CHECK(source.find("command -v reboot") != std::string::npos);
    CHECK(source.find("make_json_error(503, error.empty() ? \"failed to schedule reboot\" : error)") != std::string::npos);
    CHECK(source.find("\"/api/reboot\"") != std::string::npos);
    CHECK(source.find("config-update --config=") != std::string::npos);
    // config metadata endpoint: agent CLI is the single source of field metadata.
    CHECK(source.find("config-meta --format=json") != std::string::npos);
    CHECK(source.find(" config --config=") != std::string::npos);
    CHECK(source.find("config-defaults --format=json") == std::string::npos);
    CHECK(source.find("cannot load config defaults") == std::string::npos);
    CHECK(source.find("\"/api/config/meta\"") != std::string::npos);
    CHECK(source.find("config metadata unavailable: agent binary not found") != std::string::npos);
    CHECK(html.find("{Key: \"device_type\", Widget: WidgetSelect") != std::string::npos);
    CHECK(html.find("hid_pointer_mode") == std::string::npos);
    CHECK(html.find("{Key: \"keyboard_layout\", Widget: WidgetSelect") != std::string::npos);
    CHECK(html.find("USB HID configuration saved. Reboot the board for it to take effect") != std::string::npos);
    CHECK(html.find("config.hid_reboot_confirm") != std::string::npos);
    CHECK(html.find("Cancelled immediate reboot. Please reboot manually later, then verify USB HID after reboot") != std::string::npos);
    CHECK(html.find("USB will re-enumerate automatically") == std::string::npos);
    CHECK(html.find("window.confirm") != std::string::npos);
    CHECK(html.find("/api/device/reboot") != std::string::npos);
    CHECK(html.find("reboot command sent") != std::string::npos);
}

TEST_CASE("config web usbhid init script does not orchestrate dependent service restarts") {
    const std::string script_path = std::string(AIDEN_SOURCE_DIR) + "/overlay/etc/init.d/S49usbhid";
    std::ifstream script_in(script_path.c_str());
    REQUIRE(script_in.good());

    std::ostringstream script_buffer;
    script_buffer << script_in.rdbuf();
    const std::string script = script_buffer.str();

    CHECK(script.find("CONFIG_WEB_INIT") == std::string::npos);
    CHECK(script.find("USB_DHCP_INIT") == std::string::npos);
    CHECK(script.find("AGENT_INIT") == std::string::npos);
    CHECK(script.find("S56config_web") == std::string::npos);
    CHECK(script.find("S55aiden_usb_dhcp") == std::string::npos);
    CHECK(script.find("S53agent") == std::string::npos);
    CHECK(script.find("AIDEN_USBHID_ALLOW_HOT_REBIND") == std::string::npos);
    CHECK(script.find("force-restart") == std::string::npos);
    CHECK(script.find("USB HID hot restart is unsafe") != std::string::npos);
    CHECK(script.find("use reboot") != std::string::npos);
    CHECK(script.find("restart|reload) restart") != std::string::npos);
    CHECK(script.find("restart|reload) stop; start") == std::string::npos);
    CHECK(script.find("[device]") != std::string::npos);
    CHECK(script.find("device_type") != std::string::npos);
}

TEST_CASE("config web metadata renderer preserves field ids used by type guards") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("control.id=section+'_'+field.key") != std::string::npos);
    CHECK(html.find("control.setAttribute('data-section',section)") != std::string::npos);
    CHECK(html.find("renderConfigFields(meta)") != std::string::npos);
}

// A `//` comment runs to the end of its line, so any JavaScript sharing a line
// with one never reaches the browser. The page is emitted as minified one-liners,
// which makes it easy to append code to a commented line and silently delete it:
// that is how ModelProvidersManager and syncModelProvidersFromConfig were once dropped,
// leaving the page stuck on "Page initialization failed."
TEST_CASE("config web html keeps javascript off line-comment lines") {
    const std::string js = read_config_web_config_scripts();
    REQUIRE(js.find("syncModelProvidersFromConfig") != std::string::npos);

    const char* code_markers[] = {
        "function ", "const ", "let ", "var ", "();", NULL,
    };

    std::istringstream js_in(js);
    std::string line;
    int line_number = 0;
    while (std::getline(js_in, line)) {
        ++line_number;
        size_t comment = 0;
        while ((comment = line.find("//", comment)) != std::string::npos) {
            // Skip URL schemes such as https:// that are not comments at all.
            if (comment > 0 && line[comment - 1] == ':') {
                comment += 2;
                continue;
            }
            break;
        }
        if (comment == std::string::npos) {
            continue;
        }
        const std::string commented_out = line.substr(comment + 2);
        for (int i = 0; code_markers[i]; ++i) {
            CHECK_MESSAGE(commented_out.find(code_markers[i]) == std::string::npos,
                          "emitted JS line " << line_number << " hides code after a // comment: "
                                             << code_markers[i] << " in: "
                                             << commented_out.substr(0, 80));
        }
    }
}

// loadConfig() calls syncModelProvidersFromConfig(), which drives ModelProvidersManager and
// ModelSelector. Each must be defined on a line the browser can execute, or page
// init throws a ReferenceError that the init try/catch turns into a banner.
TEST_CASE("config web html defines provider ui symbols on executable lines") {
    const std::string js = read_config_web_config_scripts();

    const char* definitions[] = {
        "function syncModelProvidersFromConfig(",
        "const ModelProvidersManager=createProviderRecordsManager(MODEL_PROVIDER_SPEC)",
        "const ModelSelector = {",
        "ModelProvidersManager.init();",
        "ModelSelector.init();",
        NULL,
    };

    for (int i = 0; definitions[i]; ++i) {
        const std::string needle(definitions[i]);
        const size_t at = js.find(needle);
        CHECK_MESSAGE(at != std::string::npos, "missing definition: " << needle);
        if (at == std::string::npos) {
            continue;
        }
        const size_t line_start = js.rfind('\n', at) == std::string::npos ? 0 : js.rfind('\n', at) + 1;
        const std::string prefix = js.substr(line_start, at - line_start);
        CHECK_MESSAGE(prefix.find("//") == std::string::npos,
                      "definition is commented out on its line: " << needle);
    }
}

// Provider-scoped model fields (including model.api_mode) must be populated
// only after the configured provider records are loaded. Otherwise the first
// visibility refresh sees the named provider as an unknown type, filters out
// provider-specific options, and silently resets Responses provider context to
// the default Chat Completions option.
TEST_CASE("config web loads provider records before applying scoped model fields") {
    const std::string js = read_config_web_config_scripts();
    const size_t apply_payload = js.find("function applyPayload(");
    const size_t deferred_fill = js.find("fillConfigForm(appState.config,!!deferProviderScopedFields)", apply_payload);
    const size_t load_config = js.find("async function loadConfig()");
    const size_t sync_model = js.find("syncModelProvidersFromConfig();", load_config);
    const size_t scoped_refresh = js.find("applyFieldVisibility(true);return payload;", load_config);

    REQUIRE(apply_payload != std::string::npos);
    REQUIRE(deferred_fill != std::string::npos);
    REQUIRE(load_config != std::string::npos);
    REQUIRE(sync_model != std::string::npos);
    REQUIRE(scoped_refresh != std::string::npos);
    CHECK(deferred_fill > apply_payload);
    CHECK(sync_model < scoped_refresh);
}

TEST_CASE("config web keeps provider management beside each provider select") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("id=\"section-providers\"") == std::string::npos);
    CHECK(html.find("id=\"section-tts-providers\"") == std::string::npos);
    CHECK(html.find("id=\"section-stt-providers\"") == std::string::npos);
    CHECK(html.find("id=\"providersList\"") == std::string::npos);
    CHECK(html.find("id=\"ttsProvidersList\"") == std::string::npos);
    CHECK(html.find("id=\"sttProvidersList\"") == std::string::npos);

    const char* action_ids[] = {
        "addProviderBtn", "editProviderBtn", "deleteProviderBtn",
        "addTtsProviderBtn", "editTtsProviderBtn", "deleteTtsProviderBtn",
        "addSttProviderBtn", "editSttProviderBtn", "deleteSttProviderBtn",
        NULL,
    };
    for (int i = 0; action_ids[i]; ++i) {
        CHECK_MESSAGE(html.find("id=\"" + std::string(action_ids[i]) + "\"") !=
                          std::string::npos,
                      action_ids[i]);
    }
    CHECK(html.find("class=\"provider-picker\"") != std::string::npos);
}

TEST_CASE("config web collapses the model picker around the current model") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("<details id=\"modelSelectorDetails\"") != std::string::npos);
    CHECK(html.find("class=\"model-selector-details\" data-section-lock") != std::string::npos);
    CHECK(html.find("<summary>") != std::string::npos);
    CHECK(html.find("id=\"modelSelectorSummary\"") != std::string::npos);
    CHECK(html.find("function syncModelSelectorSummary()") != std::string::npos);
    CHECK(html.find("const isSectionEditing=runtimeFunction('isSectionEditing')") != std::string::npos);
    CHECK(html.find("ModelSelector.selectModel=editOnlyModelSelectorMethod(ModelSelector.selectModel)") != std::string::npos);
}

TEST_CASE("config web provider deletion keeps active references valid") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("async function deleteSelectedProvider(kind)") != std::string::npos);
    CHECK(html.find("remainingNames.length===0") != std::string::npos);
    CHECK(html.find("Add or select another provider before deleting it.") != std::string::npos);
    CHECK(html.find("context.refPatch(name,replacement)") != std::string::npos);
    CHECK(html.find("context.manager.records=nextRecords") != std::string::npos);
    CHECK(html.find("context.manager.save(patch)") != std::string::npos);
}

// The [model] provider select offers configured [model_providers.*] entries only.
// A bare provider type carries no credentials, so
// offering the metadata enum let the user pick a provider that cannot
// authenticate; adding a record is a button beside the select instead.
TEST_CASE("config web html lists only configured providers in the provider select") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("function injectProviderOptions(spec)") != std::string::npos);
    // syncModelProvidersFromConfig must call it, otherwise the select never updates.
    const size_t sync_at = js.find("function syncModelProvidersFromConfig(");
    REQUIRE(sync_at != std::string::npos);
    const std::string sync_body = js.substr(sync_at, 800);
    CHECK(sync_body.find("injectProviderOptions(MODEL_PROVIDER_SPEC)") != std::string::npos);
    // Clearing the manager on an empty config keeps stale cards from lingering.
    CHECK(sync_body.find("ModelProvidersManager.load({})") != std::string::npos);
    // Provider-scoped options (reasoning_effort) are hydrated from the resolved
    // provider type, which is only knowable once the providers map is loaded.
    // Without this re-apply, loading a config that references a named provider
    // dropped minimal/none from reasoning_effort until the user re-picked.
    const size_t inject_call = sync_body.find("injectProviderOptions(MODEL_PROVIDER_SPEC)");
    const size_t visibility_call = sync_body.find("applyFieldVisibility(true)");
    CHECK(visibility_call != std::string::npos);
    CHECK(inject_call < visibility_call);

    const size_t inject_at = js.find("function injectProviderOptions(spec)");
    REQUIRE(inject_at != std::string::npos);
    const std::string inject_body = js.substr(inject_at, 1000);
    // Options come from the configured providers, never from the metadata enum.
    CHECK(inject_body.find("providerRecordOptions(manager.records)") != std::string::npos);
    CHECK(inject_body.find("baseProviderOptions") == std::string::npos);
    // The provider the config already references has to stay selectable, or
    // loading an existing agent.toml would silently retarget the model.
    CHECK(inject_body.find("!known[current]") != std::string::npos);
    // An empty placeholder leads for a fresh config.
    CHECK(inject_body.find("options.unshift({value:'',label:'-- Select Provider --'") !=
          std::string::npos);
    CHECK(inject_body.find("ADD_PROVIDER_OPTION") == std::string::npos);
    CHECK(js.find("'add-model-provider':") != std::string::npos);
    CHECK(js.find("'provider.add'") != std::string::npos);
}

TEST_CASE("config web html uses canonical provider map and type field names") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("appState.config.model_providers") != std::string::npos);
    CHECK(html.find("configKey:'model_providers'") != std::string::npos);
    CHECK(html.find("payload.config&&payload.config[self.spec.configKey]") != std::string::npos);
    CHECK(html.find("resolveModelProviderType(providerRef)") != std::string::npos);
    CHECK(html.find("hydrateSelectField(section,'type'") != std::string::npos);
}

// Adding a provider is an explicit action beside the select, so the select never
// has to carry a sentinel value that could leak into model.provider.
TEST_CASE("config web opens the provider dialog through delegated actions") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("'add-model-provider': () => ModelProvidersManager.addRecord()") != std::string::npos);
    CHECK(js.find("event.target.closest('[data-action]')") != std::string::npos);
    CHECK(js.find("ADD_PROVIDER_OPTION") == std::string::npos);

    const size_t listener_at = js.find("function initProviders()");
    REQUIRE(listener_at != std::string::npos);
    CHECK(js.find("rememberModelProvider()") != std::string::npos);
    CHECK(js.find("updateProviderActionState('model')") != std::string::npos);
}

// Paths that repopulate the select without firing a change event (snapshot
// restore on cancel, refill from the save response) keep the remembered value in
// sync. Keeping one writer that reads the DOM makes that invariant explicit.
TEST_CASE("config web html keeps the remembered provider in sync with the select") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("function rememberModelProvider(){") != std::string::npos);
    const size_t remember_at = js.find("function rememberModelProvider(){");
    REQUIRE(remember_at != std::string::npos);
    const std::string remember_body = js.substr(remember_at, 200);
    CHECK(remember_body.find("lastModelProviderValue=el.value") != std::string::npos);

    // One writer: nothing else may assign the remembered value directly. Count
    // real assignments only -- `lastModelProviderValue===` is a comparison, and
    // the page has two of those (restore and the readSection guard).
    size_t assignments = 0;
    const std::string name("lastModelProviderValue");
    for (size_t at = js.find(name); at != std::string::npos; at = js.find(name, at + 1)) {
        size_t after = at + name.size();
        while (after < js.size() && js[after] == ' ') {
            ++after;
        }
        if (after >= js.size() || js[after] != '=') {
            continue;  // a read
        }
        if (after + 1 < js.size() && js[after + 1] == '=') {
            continue;  // == or ===, a comparison
        }
        ++assignments;
    }
    // Exactly two: the `let` declaration and the write inside the writer.
    CHECK(assignments == 2);

    // Both event-free repopulate paths have to call it.
    const size_t fill_at = js.find("function fillConfigForm(config");
    REQUIRE(fill_at != std::string::npos);
    CHECK(js.substr(fill_at, 400).find("rememberModelProvider()") != std::string::npos);

    const size_t cancel_at = js.find("function cancelEditSection(section){");
    REQUIRE(cancel_at != std::string::npos);
    const size_t cancel_end = js.find("function enterSystemEnvEdit(", cancel_at);
    REQUIRE(cancel_end != std::string::npos);
    CHECK(js.substr(cancel_at, cancel_end - cancel_at).find("rememberModelProvider()") !=
          std::string::npos);
}

// reasoning_effort scopes its levels to built-in provider types. With the
// select now offering named references, the filter has to resolve the
// reference to its type or every named provider would lose minimal/none.
TEST_CASE("config web html resolves named providers when filtering option scopes") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("function resolveModelProviderType(value)") != std::string::npos);
    CHECK(js.find("host==='openrouter.ai'||host.endsWith('.openrouter.ai')") !=
          std::string::npos);
    const size_t filter_at = js.find("function providerFilterValue(section)");
    REQUIRE(filter_at != std::string::npos);
    const std::string filter_body = js.substr(filter_at, 700);
    // Named model and voice references must be compared using the selected
    // record's built-in provider type. Provider record dialogs instead filter
    // against their own `type` field.
    CHECK(filter_body.find("isProviderRecordSection(section)") != std::string::npos);
    CHECK(filter_body.find("fieldValue(section,'type')") != std::string::npos);
    CHECK(filter_body.find("section==='model'") != std::string::npos);
    CHECK(filter_body.find("TtsProvidersManager.records[raw]") != std::string::npos);
    CHECK(filter_body.find("SttProvidersManager.records[raw]") != std::string::npos);
    CHECK(js.find("function filterSelectOptions(section,options){const provider=providerFilterValue(section);") !=
          std::string::npos);
    const size_t conditional_at = js.find("function syncConditionalSelectField(item,preserveUnknown)");
    REQUIRE(conditional_at != std::string::npos);
    const std::string conditional_body = js.substr(conditional_at, 400);
    CHECK(conditional_body.find("providerFilterValue(item.section)") != std::string::npos);
    CHECK(conditional_body.find("fieldValue(item.section,'provider')") == std::string::npos);

    // The two readers of model.provider must disagree, deliberately:
    //
    //   - Option scoping (above) resolves the named reference to its type, or a
    //     named provider loses reasoning_effort's minimal/none.
    //   - Field visibility matches the RAW value so generic field conditions do
    //     not silently change semantics when model.provider is a named record.
    //
    // Routing evalCondition through resolveProviderType would read as a natural
    // unification of the two, but would also make every provider comparison use
    // the resolved type instead of the submitted value.
    const size_t eval_at = js.find("function evalCondition(cond){");
    REQUIRE(eval_at != std::string::npos);
    const size_t eval_end = js.find("function evalRule(rule){", eval_at);
    REQUIRE(eval_end != std::string::npos);
    const std::string eval_body = js.substr(eval_at, eval_end - eval_at);
    CHECK(eval_body.find("fieldValue(parts[0]") != std::string::npos);
    CHECK(eval_body.find("resolveProviderType") == std::string::npos);
    CHECK(eval_body.find("providerFilterValue") == std::string::npos);
}

TEST_CASE("config web config form imports fieldValue from config metadata") {
    const std::string path =
        std::string(AIDEN_SOURCE_DIR) + "/src/config_web/web/assets/js/config/config-form.js";
    std::ifstream in(path.c_str());
    REQUIRE(in.good());

    std::ostringstream buffer;
    buffer << in.rdbuf();
    const std::string source = buffer.str();

    CHECK(source.find("import {fieldValue} from './config-meta.js';") != std::string::npos);
    CHECK(source.find("function isAudioArchiveAvailable(){return fieldValue(") != std::string::npos);
}

TEST_CASE("config web manages realtime voice as persistent provider records") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("const VoiceModelProvidersManager=createProviderRecordsManager(VOICE_MODEL_PROVIDER_SPEC)") !=
          std::string::npos);
    CHECK(js.find("config.voice_model_providers||{}") != std::string::npos);
    CHECK(js.find("injectProviderOptions(VOICE_MODEL_PROVIDER_SPEC)") != std::string::npos);
    CHECK(js.find("bindVoiceProviderSelect(VOICE_MODEL_PROVIDER_SPEC)") != std::string::npos);
    CHECK(js.find("resetVoiceModelProviderFields") == std::string::npos);
    CHECK(js.find("clearOnSave") == std::string::npos);
}

// A stopped agent daemon makes the /api/models proxy return 503. That is a
// normal state, not a configuration error, so the model selector degrades to
// the custom-model input instead of a red failure box the user cannot act on.
TEST_CASE("config web html degrades model selector when the agent is offline") {
    const std::string js = read_config_web_config_scripts();

    const size_t load_at = js.find("loadModels:");
    REQUIRE(load_at != std::string::npos);
    const size_t load_end = js.find("renderModelSelector:", load_at);
    REQUIRE(load_end != std::string::npos);
    const std::string load_body = js.substr(load_at, load_end - load_at);
    CHECK(load_body.find("err.status===503") != std::string::npos);
    CHECK(load_body.find("this.renderModelSelector(true)") != std::string::npos);
    CHECK(load_body.find("const requestId=++this.requestId") != std::string::npos);
    CHECK(load_body.find("requestId!==this.requestId") != std::string::npos);
    // Non-503 failures must still surface as errors.
    CHECK(load_body.find("model-selector-error") != std::string::npos);

    const std::string render_body = js.substr(load_end, 1200);
    CHECK(render_body.find("renderModelSelector:function(agentOffline)") != std::string::npos);
    // Offline must not short-circuit before the custom-model row is emitted.
    CHECK(render_body.find("this.availableModels.length===0&&!agentOffline") != std::string::npos);

    const std::string notice = "Model list needs a running agent. Enter the model ID manually.";
    CHECK(js.find(notice) != std::string::npos);
    CHECK(js.find("'provider.offline_models'") != std::string::npos);
}

// The common manager owns dialog naming and rename/save mechanics. Model
// providers customize only the behavior that is genuinely model-specific.
TEST_CASE("config web keeps model provider behavior in common manager hooks") {
    const std::string js = read_config_web_config_scripts();

    const size_t factory_at = js.find("function createProviderRecordsManager(spec)");
    REQUIRE(factory_at != std::string::npos);
    const size_t model_spec_at = js.find("const MODEL_PROVIDER_SPEC=", factory_at);
    REQUIRE(model_spec_at != std::string::npos);
    const std::string factory = js.substr(factory_at, model_spec_at - factory_at);
    const size_t model_spec_end = js.find("const TTS_PROVIDER_SPEC=", model_spec_at);
    REQUIRE(model_spec_end != std::string::npos);
    const std::string model_spec = js.substr(model_spec_at, model_spec_end - model_spec_at);

    CHECK(factory.find("this.spec.nameBase") != std::string::npos);
    CHECK(factory.find("self.spec.onRename") != std::string::npos);
    CHECK(factory.find("self.spec.afterSave") != std::string::npos);
    CHECK(factory.find("this.spec.credentialHint") != std::string::npos);

    // Renaming a record must migrate the per-provider remembered model before
    // the common manager retargets the model.provider reference.
    CHECK(model_spec.find("onRename:") != std::string::npos);
    CHECK(model_spec.find("rememberModelChoice(oldName)") != std::string::npos);
    CHECK(model_spec.find("renameRememberedModelProvider(oldName,newName)") != std::string::npos);
    CHECK(model_spec.find("rememberModelProvider()") != std::string::npos);

    // Saving provider records refreshes the available model list. This stays a
    // model hook rather than leaking ModelSelector into the generic manager.
    CHECK(model_spec.find("afterSave:") != std::string::npos);
    CHECK(model_spec.find("refreshModelSelectorForCurrentProvider()") != std::string::npos);

    // A custom OpenAI endpoint keeps the existing host-derived default name.
    CHECK(model_spec.find("nameBase:") != std::string::npos);
    CHECK(model_spec.find("type==='openai'") != std::string::npos);
    CHECK(model_spec.find("record.base_url") != std::string::npos);
    CHECK(model_spec.find("hostLabel(record.base_url)") != std::string::npos);

    // Suffixes and editable names remain generic behavior shared by all three
    // record kinds.
    CHECK(factory.find("id=\"'+section+'_record_name\"") != std::string::npos);
    CHECK(factory.find("this.nameDirty=true") != std::string::npos);
    CHECK(factory.find("for(let i=2;i<1000;i++)") != std::string::npos);
}

// Provider credentials use one api_key field, but stored values never return
// to the browser. The server reports only whether a credential is configured
// and an empty edit keeps the stored value unchanged.
TEST_CASE("config web html keeps provider credentials write only") {
    const std::string js = read_config_web_config_scripts();

    // The separate env-var input is gone.
    CHECK(js.find("id=\"providerTokenEnv\"") == std::string::npos);
    CHECK(js.find("Token Environment Variable") == std::string::npos);

    const size_t save_at = js.find("saveDialog:function(editName)");
    REQUIRE(save_at != std::string::npos);
    const size_t save_end = js.find("refPatch:function", save_at);
    REQUIRE(save_end != std::string::npos);
    const std::string save_body = js.substr(save_at, save_end - save_at);
    // The web sends the same single api_key representation that agent.toml uses.
    CHECK(js.find("function assignProviderAPIKey(record,apiKeyRaw)") != std::string::npos);
    CHECK(js.find("record.api_key=apiKeyRaw") != std::string::npos);
    CHECK(save_body.find("provider.token_env") == std::string::npos);
    // A bare $ names no variable, so it must not save silently.
    CHECK(js.find("alert(t('provider.env_required'))") != std::string::npos);

    // Reopening the dialog never renders a stored key or environment name.
	const size_t fields_at = js.find("function providerRecordFieldHtml(section,record,item,opts)");
	REQUIRE(fields_at != std::string::npos);
	const size_t fields_end = js.find("function providerRecordFieldsHtml", fields_at);
    REQUIRE(fields_end != std::string::npos);
    const std::string fields_body = js.substr(fields_at, fields_end - fields_at);
    CHECK(fields_body.find("value=\"\"") != std::string::npos);
    CHECK(fields_body.find("recordSecretConfigured(record,key)") != std::string::npos);
    CHECK(fields_body.find("escRecordHtml(shown)") == std::string::npos);

    const size_t card_at = js.find("cardHtml:function(name,record)");
    REQUIRE(card_at != std::string::npos);
    const size_t card_end = js.find("addRecord:function", card_at);
    REQUIRE(card_end != std::string::npos);
    const std::string card_body = js.substr(card_at, card_end - card_at);
    CHECK(card_body.find("Token Env:") == std::string::npos);
    CHECK(card_body.find("providerCredentialConfigured(record)") != std::string::npos);
    CHECK(card_body.find("record.api_key") == std::string::npos);
}

// Type choices and base_url visibility come from ConfigMeta. Keeping either
// list in JavaScript recreates the cross-language synchronization problem this
// manager is intended to remove.
TEST_CASE("config web model provider dialog is fully metadata driven") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("PROVIDER_BASE_URL_TYPES") == std::string::npos);
    CHECK(js.find("providerBaseUrlAllowed") == std::string::npos);
    CHECK(js.find("<option value=\"openai\"") == std::string::npos);
    CHECK(js.find("<option value=\"anthropic\"") == std::string::npos);
    CHECK(js.find("provider.type === 'openai'") == std::string::npos);

    const size_t factory_at = js.find("function createProviderRecordsManager(spec)");
    REQUIRE(factory_at != std::string::npos);
    const size_t factory_end = js.find("const MODEL_PROVIDER_SPEC=", factory_at);
    REQUIRE(factory_end != std::string::npos);
    const std::string factory = js.substr(factory_at, factory_end - factory_at);
    CHECK(factory.find("hydrateSelectField(section,'type'") != std::string::npos);
    CHECK(factory.find("applyFieldVisibility(true)") != std::string::npos);
    CHECK(factory.find("field.classList.contains('hidden')") != std::string::npos);
}

TEST_CASE("config web keeps the Anthropic credential hint as a model hook") {
    const std::string js = read_config_web_config_scripts();

    const size_t model_spec_at = js.find("const MODEL_PROVIDER_SPEC=");
    REQUIRE(model_spec_at != std::string::npos);
    const size_t model_spec_end = js.find("const TTS_PROVIDER_SPEC=", model_spec_at);
    REQUIRE(model_spec_end != std::string::npos);
    const std::string model_spec = js.substr(model_spec_at, model_spec_end - model_spec_at);
    CHECK(model_spec.find("credentialHint:") != std::string::npos);
    CHECK(model_spec.find("providerAPIKeyPlaceholder(type,credentialConfigured)") != std::string::npos);
    CHECK(model_spec.find("providerAPIKeyHelp(type)") != std::string::npos);
    CHECK(js.find("provider.anthropic_api_key_placeholder") != std::string::npos);
    CHECK(js.find("provider.anthropic_api_key_help") != std::string::npos);
}

// Model, TTS, and STT get the same named-record UX. One factory serves all
// three: copies of this logic would be multiple places to fix every
// rename/mask/save bug, so assert the shared factory and the two specs rather
// than per-kind implementations.
TEST_CASE("config web builds all provider records from one factory") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("function createProviderRecordsManager(spec)") != std::string::npos);
    CHECK(html.find("const ModelProvidersManager=createProviderRecordsManager(MODEL_PROVIDER_SPEC)") !=
          std::string::npos);
    CHECK(html.find("const TtsProvidersManager=createProviderRecordsManager(TTS_PROVIDER_SPEC)") !=
          std::string::npos);
    CHECK(html.find("const SttProvidersManager=createProviderRecordsManager(STT_PROVIDER_SPEC)") !=
          std::string::npos);
    CHECK(html.find("const ModelProvidersManager = {") == std::string::npos);
    // Each spec must name its own config key and reference field, or a save would
    // write the records under the wrong key and erase the other kind's.
    CHECK(html.find("section:'model_providers'") != std::string::npos);
    CHECK(html.find("configKey:'model_providers'") != std::string::npos);
    CHECK(html.find("refFieldId:'model_provider'") != std::string::npos);
    CHECK(html.find("configKey:'tts_providers'") != std::string::npos);
    CHECK(html.find("refFieldId:'tts_provider'") != std::string::npos);
    CHECK(html.find("configKey:'stt_providers'") != std::string::npos);
    CHECK(html.find("refFieldId:'stt_provider'") != std::string::npos);

    // Both inline add actions are wired through the same manager factory. The
    // old standalone record lists are intentionally gone.
    CHECK(html.find("id=\"ttsProvidersList\"") == std::string::npos);
    CHECK(html.find("id=\"sttProvidersList\"") == std::string::npos);
    CHECK(html.find("id=\"addTtsProviderBtn\"") != std::string::npos);
    CHECK(html.find("id=\"addSttProviderBtn\"") != std::string::npos);

    CHECK(html.find("ModelProvidersManager.init();") != std::string::npos);
    CHECK(html.find("TtsProvidersManager.init();SttProvidersManager.init();") != std::string::npos);
    // Records have to be loaded on every config load, or the cards render empty
    // after a reload even though agent.toml has records.
    CHECK(html.find("syncVoiceProvidersFromConfig();") != std::string::npos);

    // The selector labels must use the canonical record type. Reading the old
    // provider field here would make every canonical record render without its
    // type even though GET /api/config correctly returned it.
    const size_t options_at = html.find("function providerRecordOptions(records)");
    REQUIRE(options_at != std::string::npos);
    const size_t options_end = html.find("function injectProviderOptions", options_at);
    REQUIRE(options_end != std::string::npos);
    const std::string options_body = html.substr(options_at, options_end - options_at);
    CHECK(options_body.find(".type||''") != std::string::npos);
    CHECK(options_body.find(".provider||''") == std::string::npos);
}

// Model credentials belong to [model_providers.*]. Keeping a second API key
// input on [model] lets the flat section overwrite or disagree with the selected
// provider record.
TEST_CASE("config web keeps model credentials in provider records") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("id=\"model_api_key\"") == std::string::npos);
	const size_t fields_at = html.find("function providerRecordFieldHtml(section,record,item,opts)");
    REQUIRE(fields_at != std::string::npos);
    const size_t fields_end = html.find("function providerRecordOptions", fields_at);
    REQUIRE(fields_end != std::string::npos);
    const std::string fields_body = html.substr(fields_at, fields_end - fields_at);
    CHECK(fields_body.find("const id=section+'_'+key") != std::string::npos);
    CHECK(fields_body.find("recordSectionFields[section]") != std::string::npos);
}

TEST_CASE("config web collapses advanced provider fields without dropping them") {
    const std::string js = read_config_web_config_scripts();

    CHECK(js.find("!!field.advanced") != std::string::npos);
    CHECK(js.find("if(item[3])advanced+=html") != std::string::npos);
    CHECK(js.find("class=\"provider-advanced\"") != std::string::npos);
    CHECK(js.find("providerRecordFieldHtml(section,record,item,opts)") != std::string::npos);
}

// The credentials moved onto records, so the flat [tts]/[stt] cards must not keep
// inputs for them: a second editor for one credential disagrees with the record
// the moment either is used, and readSection would post the stale flat copy.
TEST_CASE("config web drops flat voice credential inputs") {
    const std::string html = read_config_web_asset_bundle();

    const char* gone[] = {
        "tts_api_key", "tts_voice_id", "tts_reference_id", "tts_emotion",
        "stt_api_key", "stt_app_id", "stt_secret_id", "stt_secret_key",
        "stt_region", "stt_base_url",
        NULL,
    };
    for (int i = 0; gone[i]; ++i) {
        const std::string marker = "id=\"" + std::string(gone[i]) + "\"";
        CHECK_MESSAGE(html.find(marker) == std::string::npos, gone[i]);
    }

    // The reference plus the genuinely global settings stay.
    CHECK(html.find("id=\"tts_provider\" data-section=\"tts\"") != std::string::npos);
    CHECK(html.find("{Key: \"speed\", Widget: WidgetSelect") != std::string::npos);
    CHECK(html.find("id=\"stt_provider\" data-section=\"stt\"") != std::string::npos);
    CHECK(html.find("{Key: \"language\", Widget: WidgetSelect") != std::string::npos);
}

// A "*_providers" metadata section describes a record map, not a form section.
// Leaving it in sectionFields makes fillConfigForm / readSection / saveSection
// walk a map as if it were scalar fields.
TEST_CASE("config web keeps provider record sections out of the form loops") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("function isProviderRecordSection(name)") != std::string::npos);
    CHECK(html.find("_providers'") != std::string::npos);

    const size_t build_at = html.find("function buildConfigMeta(meta)");
    REQUIRE(build_at != std::string::npos);
    const size_t build_end = html.find("function evalCondition", build_at);
    REQUIRE(build_end != std::string::npos);
    const std::string build_body = html.substr(build_at, build_end - build_at);
    CHECK(build_body.find("isRecordSection=isProviderRecordSection(name)") != std::string::npos);
    CHECK(build_body.find("recordSectionFields[name]=fieldList") != std::string::npos);
    // The record's own fields must not land in sectionFields.
    CHECK(build_body.find("sectionFields[name].push") == std::string::npos);
}

// Verified by running the real page in jsdom: seeding the dialog's type select
// with only the record's current value (ensureSelectOption) left a dropdown
// holding one option -- none at all for a new record -- so the provider type
// could not be chosen and every save failed "Provider is required". No grep of
// the emitted JS can catch that, hence this guard on the mechanism.
TEST_CASE("config web hydrates the provider dialog type select from metadata") {
    const std::string html = read_config_web_asset_bundle();

    const size_t hydrate_at = html.find("hydrateDialog:function(record)");
    REQUIRE(hydrate_at != std::string::npos);
    const size_t hydrate_end = html.find("readDialog:function", hydrate_at);
    REQUIRE(hydrate_end != std::string::npos);
    const std::string hydrate_body = html.substr(hydrate_at, hydrate_end - hydrate_at);

    CHECK(hydrate_body.find("hydrateSelectField(section,'type'") != std::string::npos);
    CHECK(hydrate_body.find("ensureBlankTypeOption(section)") != std::string::npos);
    // Seeding the single current value is what the bug looked like.
    CHECK(hydrate_body.find("ensureSelectOption(typeEl") == std::string::npos);

    CHECK(html.find("function ensureBlankTypeOption(section)") != std::string::npos);

    // The dialog fields come from the metadata, so provider knowledge stays in
    // the agent's ConfigMeta instead of being duplicated per dialog in JS.
    CHECK(html.find("function providerRecordFieldsHtml(section,record)") != std::string::npos);
    CHECK(html.find("recordSectionFields[section]") != std::string::npos);
}

// Field ids in the dialog follow the page's section_key convention, which is what
// lets the existing visibility engine (fieldValue -> byId(section+'_'+key)) drive
// it unchanged. A rule keyed on tts_providers.type reads the record's own type
// select; keying it on tts.provider would compare against a record NAME and never
// match, showing every field for every provider type.
TEST_CASE("config web dialog field ids drive the existing visibility engine") {
    const std::string html = read_config_web_asset_bundle();

	const size_t fields_at = html.find("function providerRecordFieldHtml(section,record,item,opts)");
    REQUIRE(fields_at != std::string::npos);
    const size_t fields_end = html.find("function createProviderRecordsManager", fields_at);
    REQUIRE(fields_end != std::string::npos);
    const std::string fields_body = html.substr(fields_at, fields_end - fields_at);
    CHECK(fields_body.find("const id=section+'_'+key") != std::string::npos);

    // The dialog re-runs visibility after rendering and on every type change, or
    // a field scoped to another provider type would stay on screen and be saved.
    CHECK(html.find("applyFieldVisibility(true);this.autoFillName();") != std::string::npos);
    CHECK(html.find("onTypeChange:function()") != std::string::npos);

    // A hidden field must not be written into the record: it belongs to a
    // different provider type and would be dead config at best.
    const size_t read_at = html.find("readDialog:function()");
    REQUIRE(read_at != std::string::npos);
    const size_t read_end = html.find("saveDialog:function", read_at);
    REQUIRE(read_end != std::string::npos);
    const std::string read_body = html.substr(read_at, read_end - read_at);
    CHECK(read_body.find("classList.contains('hidden')") != std::string::npos);
}

// Renaming any provider record has to move its reference in the same request.
TEST_CASE("config web patches the provider reference on rename") {
    const std::string html = read_config_web_asset_bundle();

    const size_t patch_at = html.find("refPatch:function(oldName,newName)");
    REQUIRE(patch_at != std::string::npos);
    const size_t patch_end = html.find("save:function", patch_at);
    REQUIRE(patch_end != std::string::npos);
    const std::string patch_body = html.substr(patch_at, patch_end - patch_at);
    CHECK(patch_body.find("this.spec.refSection") != std::string::npos);
    CHECK(patch_body.find("copy.provider=newName") != std::string::npos);
    CHECK(patch_body.find("this.spec.onRename") == std::string::npos);

    CHECK(html.find("this.save(addProviderRename(renamedFrom?this.refPatch(renamedFrom,name):null,"
                    "section,renamedFrom,name),selectInto,rename)") != std::string::npos);
    CHECK(html.find("if(rename){const el=byId(self.spec.refFieldId)") != std::string::npos);
    CHECK(html.find("if(self.spec.onRename)self.spec.onRename(") != std::string::npos);
}

TEST_CASE("config web sends provider rename metadata for masked secrets") {
    const std::string html = read_config_web_asset_bundle();

    CHECK(html.find("function addProviderRename(config,section,oldName,newName)") !=
          std::string::npos);
    CHECK(html.find("addProviderRename(renamedFrom?this.refPatch(renamedFrom,name):null,"
                    "section,renamedFrom,name)") != std::string::npos);
}

TEST_CASE("config web locks the system env editor before saving") {
    const std::string js = read_config_web_config_scripts();
    const size_t save_at = js.find("async function saveSystemEnv()");
    REQUIRE(save_at != std::string::npos);
    const size_t save_end = js.find("export { setSystemEnvLocked", save_at);
    REQUIRE(save_end != std::string::npos);
    const std::string save_body = js.substr(save_at, save_end - save_at);

    const size_t lock_at = save_body.find("setSystemEnvLocked(true)");
    const size_t request_at = save_body.find("await request(");
    REQUIRE(lock_at != std::string::npos);
    REQUIRE(request_at != std::string::npos);
    CHECK(lock_at < request_at);
    CHECK(save_body.find("setSystemEnvLocked(false)") != std::string::npos);
    CHECK(save_body.find("finally{byId('save-system_env').disabled=false;}") == std::string::npos);
}

// The [tts]/[stt] provider select offers configured record names. A bare
// provider type already in agent.toml is kept
// as an option so a pre-records config still round-trips instead of silently
// losing its provider.
TEST_CASE("config web offers voice record names in the provider select") {
    const std::string html = read_config_web_asset_bundle();

    const size_t inject_at = html.find("function injectProviderOptions(spec)");
    REQUIRE(inject_at != std::string::npos);
    const size_t inject_end = html.find("function injectNamedProviderOptions", inject_at);
    REQUIRE(inject_end != std::string::npos);
    const std::string inject_body = html.substr(inject_at, inject_end - inject_at);

    CHECK(inject_body.find("providerRecordOptions(manager.records)") != std::string::npos);
    CHECK(inject_body.find("label:'-- Select Provider --'") != std::string::npos);
    CHECK(inject_body.find("ADD_PROVIDER_OPTION") == std::string::npos);
    // An unknown current value is preserved as an option.
    CHECK(inject_body.find("!known[current]") != std::string::npos);

    // Selection changes only update the current record management state; adding
    // records is handled by the explicit inline buttons.
    CHECK(html.find("function bindVoiceProviderSelect(spec)") != std::string::npos);
    CHECK(html.find("updateProviderActionState(spec.refSection)") != std::string::npos);
    CHECK(html.find("'add-tts-provider': () => TtsProvidersManager.addRecord()") != std::string::npos);
}
