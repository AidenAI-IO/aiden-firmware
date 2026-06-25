#include <doctest.h>

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

TEST_CASE("config web exposes agent runtime status") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"agent_status\"") != std::string::npos);
    CHECK(source.find("\"/api/agent/status\"") != std::string::npos);
    CHECK(source.find("query_agent_status") != std::string::npos);
    CHECK(source.find("check_tcp_port") != std::string::npos);
    CHECK(source.find("/var/log/agent/agent.log") != std::string::npos);

    CHECK(html.find("<h2>Agent") != std::string::npos);
    CHECK(html.find("agentProcessStatus") != std::string::npos);
    CHECK(html.find("agentPortStatus") != std::string::npos);
    CHECK(html.find("renderAgentStatus") != std::string::npos);
    CHECK(html.find("startup_error") != std::string::npos);
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

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"/api/agent/logs\"") != std::string::npos);
    CHECK(source.find("handle_get_agent_log") != std::string::npos);
    CHECK(source.find("read_agent_log_snapshot") != std::string::npos);
    CHECK(source.find("\"agent_log\"") != std::string::npos);
    CHECK(source.find("\"size_bytes\"") != std::string::npos);
    CHECK(source.find("\"truncated\"") != std::string::npos);
    CHECK(source.find("tail -f") == std::string::npos);

    CHECK(html.find("Live Agent Log") != std::string::npos);
    CHECK(html.find("agentLogText") != std::string::npos);
    CHECK(html.find("agentLogMeta") != std::string::npos);
    CHECK(html.find("refreshAgentLog") != std::string::npos);
    CHECK(html.find("/api/agent/logs") != std::string::npos);
    CHECK(html.find("setInterval(function(){refreshAgentLog(false);},2000)") != std::string::npos);
}

TEST_CASE("config web auto-scrolls agent logs only while pinned to bottom") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("agentLogPaused") == std::string::npos);
    CHECK(html.find("agentLogAutoScroll:true") != std::string::npos);
    CHECK(html.find("function isAgentLogAtBottom(el)") != std::string::npos);
    CHECK(html.find("function setAgentLogAutoScroll(enabled)") != std::string::npos);
    CHECK(html.find("function syncAgentLogAutoScroll()") != std::string::npos);
    CHECK(html.find("function toggleAgentLogAutoScroll()") != std::string::npos);
    CHECK(html.find("byId('agentLogText').addEventListener('scroll',syncAgentLogAutoScroll)") != std::string::npos);
    CHECK(html.find("if(appState.agentLogAutoScroll){textEl.scrollTop=textEl.scrollHeight;}") != std::string::npos);
    CHECK(html.find("btn.className='button '+(appState.agentLogAutoScroll?'primary':'ghost')") != std::string::npos);
    CHECK(html.find("appState.agentLogPaused&&!showBanner") == std::string::npos);
}

TEST_CASE("config web preserves agent log selection during background refresh") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("agentLogPendingSnapshot:null") != std::string::npos);
    CHECK(html.find("function agentLogBodyText(snapshot)") != std::string::npos);
    CHECK(html.find("function agentLogBodyEquals(a,b)") != std::string::npos);
    CHECK(html.find("function agentLogSelectionActive(el)") != std::string::npos);
    CHECK(html.find("function applyPendingAgentLogSnapshotIfIdle()") != std::string::npos);
    CHECK(html.find("const contentChanged=!agentLogBodyEquals(previous,snapshot);") != std::string::npos);
    CHECK(html.find("if(contentChanged){renderLogText(textEl,text,snapshot.exists?'Log is empty':'Log file is unavailable');}") != std::string::npos);
    CHECK(html.find("if(!showBanner&&agentLogSelectionActive(byId('agentLogText')))") != std::string::npos);
    CHECK(html.find("if(!agentLogBodyEquals(appState.agentLog,snapshot)){appState.agentLogPendingSnapshot=snapshot;}") != std::string::npos);
    CHECK(html.find("document.addEventListener('selectionchange',applyPendingAgentLogSnapshotIfIdle)") != std::string::npos);
}

TEST_CASE("config web colors live log lines by frontend classification") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("function classifyLine(line)") != std::string::npos);
    CHECK(html.find("function renderLogText(el,text,emptyText)") != std::string::npos);
    CHECK(html.find("split(/\\\\r?\\\\n/)") != std::string::npos);
    CHECK(html.find("document.createElement('span')") != std::string::npos);
    CHECK(html.find("span.className='log-line '+classifyLine(line);") != std::string::npos);

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
    CHECK(html.find("\\\\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\\\\b") != std::string::npos);
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
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    const std::string llm_html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_llm_html.h";
    std::ifstream llm_in(llm_html_path.c_str());
    REQUIRE(llm_in.good());

    std::ostringstream llm_buffer;
    llm_buffer << llm_in.rdbuf();
    const std::string llm_html = llm_buffer.str();

    // Backend wires the routes and serves the standalone page.
    CHECK(source.find("\"/llm-logs\"") != std::string::npos);
    CHECK(source.find("\"/api/llm-logs\"") != std::string::npos);
    CHECK(source.find("/api/llm-logs/export/") != std::string::npos);
    CHECK(source.find("/api/llm-logs/import/") != std::string::npos);
    CHECK(source.find("handle_get_llm_logs") != std::string::npos);
    CHECK(source.find("handle_export_llm_log_file") != std::string::npos);
    CHECK(source.find("handle_import_llm_log_file") != std::string::npos);
    CHECK(source.find("is_llm_log_name") != std::string::npos);
    CHECK(source.find("CONFIG_WEB_LLM_HTML") != std::string::npos);
    CHECK(source.find("/api/llm-logs/file/") == std::string::npos);
    CHECK(source.find("handle_get_llm_log_file") == std::string::npos);
    CHECK(source.find("read_llm_log_file") == std::string::npos);
    CHECK(source.find("LlmLogReadResult") == std::string::npos);
    CHECK(source.find("kLlmLogMaxReadSize") == std::string::npos);

    // Main config page links to the viewer.
    CHECK(html.find("href=\\\"/llm-logs\\\"") != std::string::npos);

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
    CHECK(llm_html.find("response.body.getReader()") != std::string::npos);
    CHECK(llm_html.find("new TextDecoder()") != std::string::npos);
    CHECK(llm_html.find("fetch('/api/llm-logs/export/' + encodeURIComponent(name))") != std::string::npos);
    CHECK(llm_html.find("request('/api/llm-logs/file/'") == std::string::npos);
    CHECK(llm_html.find("data.content") == std::string::npos);
    CHECK(llm_html.find("tool_calls") != std::string::npos);
    CHECK(llm_html.find(".msg-head,.diff-msg-head{") != std::string::npos);
    CHECK(llm_html.find("font-size:12px;line-height:1.2") != std::string::npos);
    CHECK(llm_html.find("function renderMessageHead") != std::string::npos);
    CHECK(llm_html.find("class=\\\"msg-index\\\">#' + index") != std::string::npos);
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
    CHECK(llm_html.find("class=\\\"msg-image\\\"") != std::string::npos);
    CHECK(llm_html.find("<img src=\\\"' + escAttr(url)") != std::string::npos);

    // Messages view appends a Response section so request and response render
    // on the same screen. Covers OpenAI JSON, SSE streams, and raw fallbacks.
    CHECK(llm_html.find("function renderMessagesView") != std::string::npos);
    CHECK(llm_html.find("function renderDiffView") != std::string::npos);
    CHECK(llm_html.find("function renderResponseBlock") != std::string::npos);
    CHECK(llm_html.find("function extractResponseMessage") != std::string::npos);
    CHECK(llm_html.find("renderDiff(prevReq.messages || [], req.messages || []) + renderResponseBlock(g)") != std::string::npos);
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
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"audio_archive\"") != std::string::npos);
    CHECK(source.find("audio_archive.enabled") != std::string::npos);
    CHECK(html.find("section-audio_archive") != std::string::npos);
    CHECK(html.find("audio_archive_enabled") != std::string::npos);
    CHECK(html.find("audioArchiveModeHint") != std::string::npos);
    CHECK(html.find("isAudioArchiveAvailable") != std::string::npos);
    CHECK(html.find("agent.input_mode = stt") != std::string::npos);
    CHECK(html.find("testSection('audio_archive')") == std::string::npos);
    CHECK(html.find("[audio_archive]") != std::string::npos);
    CHECK(html.find("保存 STT 语音录音 WAV") != std::string::npos);
}

TEST_CASE("config web docs list token_env in model fields") {
    const std::string doc_path = std::string(AIDEN_SOURCE_DIR) + "/docs/04-agent/configuration.md";
    std::ifstream doc_in(doc_path.c_str());
    REQUIRE(doc_in.good());

    std::ostringstream doc_buffer;
    doc_buffer << doc_in.rdbuf();
    const std::string doc = doc_buffer.str();

    const char* model_fields[] = {
        "provider",
        "token_env",
        "model",
        "api_key",
        "base_url",
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

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"/api/ota/update\"") != std::string::npos);
    CHECK(source.find("\"/api/ota/check-now\"") != std::string::npos);
    CHECK(source.find("\"/api/ota/logs\"") != std::string::npos);
    CHECK(source.find("handle_post_ota_update") != std::string::npos);
    CHECK(source.find("handle_get_ota_log") != std::string::npos);
    CHECK(source.find("read_ota_log_snapshot") != std::string::npos);
    CHECK(source.find("kOtaWebUpdateLogPath") != std::string::npos);
    CHECK(source.find("/tmp/config_web_ota_update.log") != std::string::npos);
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
    CHECK(source.find("cJSON_AddNumberToObject(response, \"ota_log_start_size_bytes\", 0)") != std::string::npos);
    CHECK(source.find("cJSON_AddItemToObject(response, \"ota_log\"") == std::string::npos);
    CHECK(source.find("echo '[config_web] ota update requested'") == std::string::npos);
    CHECK(source.find("close(lock_fd)") != std::string::npos);
    CHECK(source.find("tail -f") == std::string::npos);

    CHECK(html.find("OTA 更新") != std::string::npos);
    CHECK(html.find("fwActions") != std::string::npos);
    CHECK(html.find("otaUpdateBtn") != std::string::npos);
    CHECK(html.find("actionBanner") != std::string::npos);
    CHECK(html.find("actionDetails") != std::string::npos);
    const std::string::size_type fw_actions_pos = html.find("id=\\\"fwActions\\\"");
    const std::string::size_type ota_button_pos = html.find("id=\\\"otaUpdateBtn\\\"");
    const std::string::size_type action_banner_pos = html.find("id=\\\"actionBanner\\\"");
    const std::string::size_type action_details_pos = html.find("id=\\\"actionDetails\\\"");
    const std::string::size_type ota_log_panel_pos = html.find("id=\\\"otaLogPanel\\\"");
    const std::string::size_type wifi_heading_pos = html.find("Wi-Fi 配置");
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
    CHECK(html.find("OTA 实时日志") != std::string::npos);
    CHECK(html.find("otaLogPanel") != std::string::npos);
    CHECK(html.find("id=\\\"otaLogPanel\\\" class=\\\"ota-log-panel\\\"") != std::string::npos);
    CHECK(html.find("id=\\\"otaLogPanel\\\" class=\\\"card ota-log-panel\\\"") == std::string::npos);
    CHECK(html.find(".ota-log-panel{display:none;margin-top:12px}") != std::string::npos);
    CHECK(html.find("showOtaLogPanel") != std::string::npos);
    CHECK(html.find("updateActionDetailsVisibility") != std::string::npos);
    CHECK(html.find("if(!appState.otaLogVisible&&!showBanner){return;}") != std::string::npos);
    CHECK(html.find("otaLogPending:false") != std::string::npos);
    CHECK(html.find("otaLogStartSize:0") != std::string::npos);
    CHECK(html.find("otaLogHasNewProgress") != std::string::npos);
    CHECK(html.find("extractOtaExitCode") != std::string::npos);
    CHECK(html.find("[config_web] ota update exited rc=") != std::string::npos);
    CHECK(html.find("OTA 更新失败（rc=") != std::string::npos);
    CHECK(html.find("最近 OTA 日志：\\\\n") != std::string::npos);
    CHECK(html.find("setOtaLogPending") != std::string::npos);
    CHECK(html.find("OTA 更新已开始，等待日志输出...") != std::string::npos);
    CHECK(html.find("setOtaLogPending('OTA 更新已开始，等待日志输出...',Number(payload.ota_log_start_size_bytes||0));") != std::string::npos);
    CHECK(html.find("renderOtaLog(snapshot, {preservePending:true})") != std::string::npos);
    CHECK(html.find("payload.ota_health_log") != std::string::npos);
    CHECK(html.find("extractOtaExitCode((payload.ota_log||{}).log||'')") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate(){const btn=byId('otaUpdateBtn');setOtaLogPending(") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate(){const btn=byId('otaUpdateBtn');showOtaLogPanel();") == std::string::npos);
    CHECK(html.find("function hideOtaLogPanel()") != std::string::npos);
    CHECK(html.find("function setDetails(text,options)") != std::string::npos);
    CHECK(html.find("if(!(options&&options.keepOtaLog)){hideOtaLogPanel();}") != std::string::npos);
    CHECK(html.find("setDetails('',{keepOtaLog:true})") != std::string::npos);
    CHECK(html.find("setDetails('最近 OTA 日志：\\\\n'+String(text||'').slice(-4000),{keepOtaLog:true})") != std::string::npos);
    CHECK(html.find("setDetails(err.message,{keepOtaLog:true})") != std::string::npos);
    CHECK(html.find("otaLogText") != std::string::npos);
    CHECK(html.find("otaLogMeta") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate") != std::string::npos);
    CHECK(html.find("refreshOtaLog") != std::string::npos);
    CHECK(html.find("/api/ota/update") != std::string::npos);
    CHECK(html.find("/api/ota/check-now") == std::string::npos);
    CHECK(html.find("/api/ota/logs") != std::string::npos);
    CHECK(html.find("setInterval(function(){refreshOtaLog(false);},2000)") != std::string::npos);
}

TEST_CASE("config web exposes running firmware version and ota health status separately") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"health_status\"") != std::string::npos);
    CHECK(source.find("\"health_error\"") != std::string::npos);
    CHECK(source.find("\"previous_version\"") != std::string::npos);
    CHECK(source.find("current_slot_from_cmdline") != std::string::npos);
    CHECK(source.find("running_slot_matches_target") != std::string::npos);

    CHECK(html.find("fwHealth") != std::string::npos);
    CHECK(html.find("renderFirmwareInfo") != std::string::npos);
    CHECK(html.find("OTA 状态") != std::string::npos);
    CHECK(html.find("health_error") != std::string::npos);
    CHECK(html.find("previous_version") != std::string::npos);
}

TEST_CASE("ota open sources documentation references current docs paths") {
    const std::string doc_path = std::string(AIDEN_SOURCE_DIR) + "/docs/08-ota/OTA_OPEN_SOURCES.md";
    std::ifstream doc_in(doc_path.c_str());
    REQUIRE(doc_in.good());

    std::ostringstream doc_buffer;
    doc_buffer << doc_in.rdbuf();
    const std::string doc = doc_buffer.str();

    CHECK(doc.find("docs/08-ota/ota-external-developers.md") != std::string::npos);
    CHECK(doc.find("docs/08-ota/ota-quick-examples.md") != std::string::npos);
    CHECK(doc.find("docs/08-ota/ota-release-channels.md") != std::string::npos);
    CHECK(doc.find("docs/ota-external-developers.md") == std::string::npos);
    CHECK(doc.find("docs/ota-quick-examples.md") == std::string::npos);
    CHECK(doc.find("docs/ota-release-channels.md") == std::string::npos);
}

TEST_CASE("config web exposes screenshot pruning config fields") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"screenshot_keep_n\"") != std::string::npos);
    CHECK(source.find("\"screenshot_prune_interval\"") != std::string::npos);
    CHECK(source.find("\"screen_stable_timeout_ms\"") != std::string::npos);
    CHECK(source.find("\"screen_stable_ms\"") != std::string::npos);
    CHECK(source.find("\"screen_stable_diff_threshold\"") != std::string::npos);
    CHECK(source.find("\"force_simple_loop\"") != std::string::npos);
    CHECK(source.find("config.screenshot_keep_n") != std::string::npos);
    CHECK(source.find("config.screenshot_prune_interval") != std::string::npos);
    CHECK(source.find("config.screen_stable_timeout_ms") != std::string::npos);
    CHECK(source.find("config.screen_stable_ms") != std::string::npos);
    CHECK(source.find("config.screen_stable_diff_threshold") != std::string::npos);
    CHECK(source.find("config.force_simple_loop") != std::string::npos);

    CHECK(html.find("agent_screenshot_keep_n") != std::string::npos);
    CHECK(html.find("agent_screenshot_prune_interval") != std::string::npos);
    CHECK(html.find("agent_screen_stable_timeout_ms") != std::string::npos);
    CHECK(html.find("agent_screen_stable_ms") != std::string::npos);
    CHECK(html.find("agent_screen_stable_diff_threshold") != std::string::npos);
    CHECK(html.find("agent_force_simple_loop") != std::string::npos);
}

TEST_CASE("config web exposes model spec override fields") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("\"context_window\"") != std::string::npos);
    CHECK(source.find("\"max_response_tokens\"") != std::string::npos);
    CHECK(source.find("\"model_max_output_tokens\"") != std::string::npos);
    CHECK(source.find("config.model.context_window") != std::string::npos);
    CHECK(source.find("config.model.max_response_tokens") != std::string::npos);
    CHECK(source.find("config.model.model_max_output_tokens") != std::string::npos);

    CHECK(html.find("model_max_response_tokens") != std::string::npos);
    CHECK(html.find("model_context_window") != std::string::npos);
    CHECK(html.find("model_model_max_output_tokens") != std::string::npos);
    CHECK(html.find("0 = auto") != std::string::npos);
}

TEST_CASE("config web exposes brave search provider") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

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
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    const char* select_ids[] = {
        "agent_input_mode",
        "agent_trigger_mode",
        "agent_vad_backend",
        "agent_vad_speech_threshold",
        "model_provider",
        "tts_provider",
        "tts_speed",
        "stt_provider",
        "hid_pointer_mode",
        "search_provider",
        "telemetry_provider",
        NULL,
    };
    for (int i = 0; select_ids[i]; ++i) {
        const std::string select_marker = "<select id=\\\"" + std::string(select_ids[i]) + "\\\"";
        const std::string input_marker = "<input id=\\\"" + std::string(select_ids[i]) + "\\\"";
        CHECK_MESSAGE(html.find(select_marker) != std::string::npos, select_ids[i]);
        CHECK_MESSAGE(html.find(input_marker) == std::string::npos, select_ids[i]);
    }

    CHECK(html.find("input,select,textarea") != std::string::npos);
    CHECK(html.find("input:focus,select:focus,textarea:focus") != std::string::npos);
    CHECK(html.find("input:disabled,select:disabled,textarea:disabled") != std::string::npos);
    // Select options are now built from the agent config-meta payload rather
    // than a hard-coded table; the UI fetches /api/config/meta and derives
    // enum/range options in buildConfigMeta().
    CHECK(html.find("let selectFieldOptions=") != std::string::npos);
    CHECK(html.find("function buildConfigMeta(meta)") != std::string::npos);
    CHECK(html.find("loadConfigMeta") != std::string::npos);
    CHECK(html.find("'/api/config/meta'") != std::string::npos);
    CHECK(html.find("hydrateSelectOptions") != std::string::npos);
    CHECK(html.find("ensureSelectOption") != std::string::npos);
    CHECK(html.find("let fieldDefaults=") != std::string::npos);
    CHECK(html.find("function fieldDefaultPlaceholder") != std::string::npos);
    CHECK(html.find("applyDefaultPlaceholder(name,field)") != std::string::npos);
    CHECK(html.find("getAttribute('placeholder')") != std::string::npos);
    CHECK(html.find("默认值: ") != std::string::npos);
    // rangeOptions is now invoked with metadata-provided bounds, not literals.
    CHECK(html.find("rangeOptions(field.range.min,field.range.max,field.range.step") != std::string::npos);
    // The legacy hard-coded option table must be gone.
    CHECK(html.find("const selectFieldOptions=") == std::string::npos);
}

TEST_CASE("config web degrades gracefully when config metadata is unavailable") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    // A failed config-meta fetch must not abort the whole page: Wi-Fi, logs and
    // polling stay live, only the agent.toml sections are disabled. The init
    // path therefore tracks a metaOk flag instead of returning early.
    CHECK(html.find("let metaOk=true;") != std::string::npos);
    CHECK(html.find("metaOk=false;") != std::string::npos);
    // The early `return` that killed Wi-Fi/log init on meta failure is gone.
    CHECK(html.find("setDetails(err.message);return;}hydrateSelectOptions") == std::string::npos);
    // Wi-Fi scan and agent-log refresh run regardless of metaOk.
    CHECK(html.find("await scanWifi(false);await refreshAgentLog(false);") != std::string::npos);
    // Only agent.toml sections are disabled on failure; system_env stays editable.
    CHECK(html.find("function disableAgentConfigEditing()") != std::string::npos);
    CHECK(html.find("if(card.id==='section-system_env')return;") != std::string::npos);
    CHECK(html.find("if(!metaOk){disableAgentConfigEditing();}") != std::string::npos);

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
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("wifiListExpanded:false") != std::string::npos);
    CHECK(html.find("function connectedWifiSsid()") != std::string::npos);
    CHECK(html.find("function visibleWifiNames(names)") != std::string::npos);
    CHECK(html.find("function toggleWifiListExpanded()") != std::string::npos);
    CHECK(html.find("显示其他 Wi-Fi") != std::string::npos);
    CHECK(html.find("收起其他 Wi-Fi") != std::string::npos);
    CHECK(html.find("const visibleNames=visibleWifiNames(names);") != std::string::npos);
    CHECK(html.find("names.forEach(function(name)") == std::string::npos);
    CHECK(html.find("function initialReadyMessage(metaOk)") != std::string::npos);
    CHECK(html.find("if(connectedWifiSsid())return 'Wi-Fi 已连接。';") != std::string::npos);
    CHECK(html.find("setBanner(initialReadyMessage(metaOk),!metaOk);") != std::string::npos);
}

TEST_CASE("config web tolerates metadata sections without rendered controls") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    // Config metadata may expose sections that do not have a static editor
    // card yet. Page-level locking must skip those missing DOM nodes instead
    // of aborting initialization.
    CHECK(html.find("const btn=byId('save-'+section);if(btn)btn.disabled=actualLocked;") != std::string::npos);
    CHECK(html.find("const card=byId('section-'+section);if(card)card.classList.remove('editing');") != std::string::npos);
}

TEST_CASE("config web tolerates metadata fields without rendered controls") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    // A new metadata field may be added before the static HTML form is updated.
    // Editing and saving an existing section must skip missing controls and
    // preserve any already-loaded value for those fields.
    CHECK(html.find("if(!el)return;snap[item[0]]=") != std::string::npos);
    CHECK(html.find("if(!el)return;if(snap[item[0]]!==undefined)") != std::string::npos);
    CHECK(html.find("function readSection(section){const values=Object.assign({},(appState.config&&appState.config[section])||{});") != std::string::npos);
    CHECK(html.find("if(!el)return;const raw=el.value;") != std::string::npos);
}

TEST_CASE("config web preserves loaded secret values when password inputs are left blank") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("const values=Object.assign({},(appState.config&&appState.config[section])||{});") != std::string::npos);
    CHECK(html.find("function isSecretField(el)") != std::string::npos);
    CHECK(html.find("if(isSecretField(el)&&raw===''){return;}") != std::string::npos);
    CHECK(html.find("if(el.type==='password'&&raw===''){delete values[key];return;}") == std::string::npos);
}


TEST_CASE("config web updates dependent field visibility from selected values") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find(".field.hidden{display:none}") != std::string::npos);
    CHECK(html.find("function setFieldVisible(section,key,visible)") != std::string::npos);
    CHECK(html.find("function applyFieldVisibility()") != std::string::npos);
    // Visibility is now driven by declarative rules from config-meta
    // (visibleWhen), evaluated generically rather than hard-coded per field.
    CHECK(html.find("visibilityRules.forEach") != std::string::npos);
    CHECK(html.find("function evalCondition(cond)") != std::string::npos);
    CHECK(html.find("function evalRule(rule)") != std::string::npos);
    CHECK(html.find("function bindFieldVisibility()") != std::string::npos);
    CHECK(html.find("watchedFields.forEach") != std::string::npos);
    CHECK(html.find("addEventListener('change',function(){applyFieldVisibility();})") != std::string::npos);
    CHECK(html.find("fillConfigForm(config){Object.keys(sectionFields).forEach") != std::string::npos);
    CHECK(html.find("applyFieldVisibility();}") != std::string::npos);
    // The legacy imperative visibility chain must be gone.
    CHECK(html.find("setFieldVisible('model','base_url',modelProvider!=='openrouter')") == std::string::npos);
    CHECK(html.find("const sttTencent=sttProvider==='tencent'") == std::string::npos);
}

TEST_CASE("config web fills Tencent ASR STT defaults from metadata") {
    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("function isTencentASRProvider(value)") != std::string::npos);
    CHECK(html.find("function fieldDefaultValue(section,key,fallback)") != std::string::npos);
    CHECK(html.find("function applySTTTencentASRDefaults()") != std::string::npos);
    CHECK(html.find("applySTTTencentASRDefaults();applyAudioArchiveAvailability();") != std::string::npos);
    CHECK(html.find("stt_app_id") != std::string::npos);
    CHECK(html.find("id=\\\"stt_engine_model_type\\\" data-section=\\\"stt\\\"") != std::string::npos);
    CHECK(html.find("fillIfEmpty('region','ap-guangzhou')") != std::string::npos);
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

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(html.find("id=\\\"test-stt\\\"") != std::string::npos);
    CHECK(html.find("onclick=\\\"toggleSTTTest()\\\"") != std::string::npos);
    CHECK(html.find("testSection('stt')") == std::string::npos);
    CHECK(html.find("function toggleSTTTest()") != std::string::npos);
    CHECK(html.find("function startSTTTest()") != std::string::npos);
    CHECK(html.find("function stopSTTTest()") != std::string::npos);
    CHECK(html.find("'/api/config/test/stt/start'") != std::string::npos);
    CHECK(html.find("'/api/config/test/stt/stop'") != std::string::npos);
    CHECK(html.find("激活麦克风") != std::string::npos);
    CHECK(html.find("识别结果") != std::string::npos);

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

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

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
    CHECK(html.find("<textarea id=\\\"system_env_content\\\" class=\\\"system-env-editor\\\"") != std::string::npos);
    CHECK(html.find("saveSystemEnv") != std::string::npos);
    CHECK(html.find("system_proxy") == std::string::npos);
    CHECK(html.find("section-proxy") == std::string::npos);
    CHECK(html.find("proxy_http_proxy") == std::string::npos);
    CHECK(html.find("proxy:[[") == std::string::npos);
    CHECK(html.find("Proxy example") == std::string::npos);
    CHECK(html.find("http_proxy=http://127.0.0.1:7890") == std::string::npos);
    CHECK(html.find("NO_PROXY=localhost,127.0.0.1,::1") == std::string::npos);

    const size_t agent_card = html.find("<h2>Agent 配置</h2>");
    const size_t telemetry_section = html.find("id=\\\"section-telemetry\\\"");
    const size_t system_env_section = html.find("id=\\\"section-system_env\\\"");
    REQUIRE(agent_card != std::string::npos);
    REQUIRE(telemetry_section != std::string::npos);
    REQUIRE(system_env_section != std::string::npos);
    CHECK(agent_card < telemetry_section);
    CHECK(telemetry_section < system_env_section);
    CHECK(html.find("<div class=\\\"card section-card\\\" id=\\\"section-system_env\\\"") != std::string::npos);
}

TEST_CASE("config web exposes telemetry settings section") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("config.telemetry.enabled") != std::string::npos);
    CHECK(source.find("add_string_array_to_object(telemetry, \"tags\", config.telemetry.tags)") != std::string::npos);
    CHECK(source.find("set_json_string_vector(&config->telemetry.tags, telemetry, \"tags\")") != std::string::npos);
    CHECK(source.find("std::string provider_original = json_is_string(provider_item) ? trim_copy(provider_item->valuestring) : \"\";") != std::string::npos);
    CHECK(source.find("std::string provider = lowercase_copy(provider_original);") != std::string::npos);
    CHECK(source.find("public_key_env") == std::string::npos);
    CHECK(source.find("secret_key_env") == std::string::npos);
    CHECK(source.find("section == \"telemetry\"") != std::string::npos);

    CHECK(html.find("section-telemetry") != std::string::npos);
    CHECK(html.find("<h3>[telemetry]</h3>") != std::string::npos);
    CHECK(html.find("telemetry_base_url") != std::string::npos);
    CHECK(html.find("telemetry_public_key") != std::string::npos);
    CHECK(html.find("telemetry_secret_key") != std::string::npos);
    CHECK(html.find("telemetry_tags") != std::string::npos);
    CHECK(html.find("public_key_env") == std::string::npos);
    CHECK(html.find("secret_key_env") == std::string::npos);
    CHECK(html.find("parseListValue") != std::string::npos);
    CHECK(html.find("<textarea id=\\\"telemetry_tags\\\"") != std::string::npos);
}

TEST_CASE("config web does not expose benchmark settings section") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

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
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    CHECK(source.find("cJSON* log_config = add_object(root, \"log\")") != std::string::npos);
    CHECK(source.find("config.log.llm_http_retention_days") != std::string::npos);
    CHECK(source.find("cJSON* log_config = cJSON_GetObjectItem(root, \"log\")") != std::string::npos);
    CHECK(source.find("set_json_int(&config->log.llm_http_retention_days, log_config, \"llm_http_retention_days\")") != std::string::npos);
    CHECK(source.find("{\"log\", \"llm_http_retention_days\", CONFIG_FIELD_NUMBER}") != std::string::npos);

    CHECK(html.find("section-log") != std::string::npos);
    CHECK(html.find("<h3>[log]</h3>") != std::string::npos);
    CHECK(html.find("log_llm_http_retention_days") != std::string::npos);
    CHECK(html.find("save-log") != std::string::npos);
    CHECK(html.find("enterEditSection('log')") != std::string::npos);
}

TEST_CASE("config web exposes live activity settings section") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    const std::string toml_header_path = std::string(AIDEN_SOURCE_DIR) + "/src/agent_toml.h";
    std::ifstream toml_header_in(toml_header_path.c_str());
    REQUIRE(toml_header_in.good());

    std::ostringstream toml_header_buffer;
    toml_header_buffer << toml_header_in.rdbuf();
    const std::string toml_header = toml_header_buffer.str();

    const std::string toml_source_path = std::string(AIDEN_SOURCE_DIR) + "/src/agent_toml.cpp";
    std::ifstream toml_source_in(toml_source_path.c_str());
    REQUIRE(toml_source_in.good());

    std::ostringstream toml_source_buffer;
    toml_source_buffer << toml_source_in.rdbuf();
    const std::string toml_source = toml_source_buffer.str();

    CHECK(source.find("\"live_activity\"") != std::string::npos);
    CHECK(source.find("cJSON* live_activity = add_object(root, \"live_activity\")") != std::string::npos);
    CHECK(source.find("config.live_activity.relay_url") != std::string::npos);
    CHECK(source.find("has_relay_api_key") != std::string::npos);
    CHECK(source.find("has_private_key_pem") != std::string::npos);
    CHECK(source.find("preserve_redacted_agent_secrets") != std::string::npos);
    CHECK(source.find("stored.live_activity.relay_api_key") != std::string::npos);
    CHECK(source.find("stored.live_activity.private_key_pem") != std::string::npos);

    CHECK(html.find("section-live_activity") != std::string::npos);
    CHECK(html.find("<h3>[live_activity]</h3>") != std::string::npos);
    CHECK(html.find("live_activity_relay_url") == std::string::npos);
    CHECK(html.find("live_activity_relay_api_key") == std::string::npos);
    CHECK(html.find("live_activity_board_id") != std::string::npos);
    CHECK(html.find("live_activity_phone_id") != std::string::npos);
    CHECK(html.find("live_activity_private_key_path") == std::string::npos);
    CHECK(html.find("live_activity_private_key_pem") == std::string::npos);
    CHECK(html.find("live_activity_timeout_sec") == std::string::npos);
    CHECK(html.find("function isSecretField(el)") != std::string::npos);
    CHECK(html.find("save-live_activity") != std::string::npos);
    CHECK(html.find("enterEditSection('live_activity')") != std::string::npos);

    CHECK(toml_header.find("struct LiveActivityToml") != std::string::npos);
    CHECK(toml_header.find("LiveActivityToml live_activity") != std::string::npos);
    CHECK(toml_source.find("section == \"live_activity\"") != std::string::npos);
    CHECK(toml_source.find("\"relay_api_key\"") != std::string::npos);
    CHECK(toml_source.find("[live_activity]") != std::string::npos);
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
    CHECK(source.find("config saved; agent restarting") != std::string::npos);
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

TEST_CASE("config web preserves hid pointer mode and avoids hot-restarting usbhid") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    const std::string toml_header_path = std::string(AIDEN_SOURCE_DIR) + "/src/agent_toml.h";
    std::ifstream toml_header_in(toml_header_path.c_str());
    REQUIRE(toml_header_in.good());

    std::ostringstream toml_header_buffer;
    toml_header_buffer << toml_header_in.rdbuf();
    const std::string toml_header = toml_header_buffer.str();

    const std::string toml_source_path = std::string(AIDEN_SOURCE_DIR) + "/src/agent_toml.cpp";
    std::ifstream toml_source_in(toml_source_path.c_str());
    REQUIRE(toml_source_in.good());

    std::ostringstream toml_source_buffer;
    toml_source_buffer << toml_source_in.rdbuf();
    const std::string toml_source = toml_source_buffer.str();

    CHECK(toml_header.find("pointer_mode") != std::string::npos);
    CHECK(toml_source.find("\"pointer_mode\"") != std::string::npos);
    CHECK(source.find("kUsbHidInitScript = \"/etc/init.d/S49usbhid\"") == std::string::npos);
    CHECK(source.find("schedule_usbhid_restart") == std::string::npos);
    CHECK(source.find("usbhid_restart_scheduled") != std::string::npos);
    CHECK(source.find("usbhid_restart_required") != std::string::npos);
    CHECK(source.find("reboot_required") != std::string::npos);
    CHECK(source.find("schedule_poweroff") != std::string::npos);
    CHECK(source.find("\"/api/poweroff\"") != std::string::npos);
    CHECK(source.find("config-check --stdin --format=json") != std::string::npos);
    CHECK(source.find("config validation unavailable: agent binary not found") != std::string::npos);
    // config metadata endpoint: agent CLI is the single source of field metadata.
    CHECK(source.find("config-meta --format=json") != std::string::npos);
    CHECK(source.find(" config --config=") != std::string::npos);
    CHECK(source.find("config-defaults --format=json") == std::string::npos);
    CHECK(source.find("cannot load config defaults") == std::string::npos);
    CHECK(source.find("\"/api/config/meta\"") != std::string::npos);
    CHECK(source.find("config metadata unavailable: agent binary not found") != std::string::npos);
    CHECK(html.find("hid_pointer_mode") != std::string::npos);
    CHECK(html.find("<select id=\\\"hid_pointer_mode\\\"") != std::string::npos);
    CHECK(html.find("pointer_mode 需要关机重启后生效") != std::string::npos);
    CHECK(html.find("window.confirm") != std::string::npos);
    CHECK(html.find("/api/poweroff") != std::string::npos);
    CHECK(html.find("poweroff 指令已下发") != std::string::npos);
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
    CHECK(script.find("use poweroff") != std::string::npos);
    CHECK(script.find("restart|reload) restart") != std::string::npos);
    CHECK(script.find("restart|reload) stop; start") == std::string::npos);
}

TEST_CASE("config web resolved config validation keeps required fields and type guards") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("validate_model_section") != std::string::npos);
    CHECK(source.find("validate_known_config_field_types") != std::string::npos);
    CHECK(source.find("validate_search_secret_presence") != std::string::npos);
    CHECK(source.find("validate_required_string(model, \"model\", \"provider\", false") != std::string::npos);
    CHECK(source.find("\"search\", \"has_api_key\", CONFIG_FIELD_BOOL") != std::string::npos);
    CHECK(source.find("\"voice_progress_speech_enabled\", CONFIG_FIELD_BOOL") != std::string::npos);
    CHECK(source.find("\"voice_speech_summary_enabled\", CONFIG_FIELD_BOOL") == std::string::npos);
}

TEST_CASE("config web rendered config controls are registered in field type guards") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string html_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web_html.h";
    std::ifstream html_in(html_path.c_str());
    REQUIRE(html_in.good());

    std::ostringstream html_buffer;
    html_buffer << html_in.rdbuf();
    const std::string html = html_buffer.str();

    std::set<std::string> rendered_fields;
    const std::string section_marker = "data-section=\\\"";
    size_t pos = 0;
    while ((pos = html.find(section_marker, pos)) != std::string::npos) {
        const size_t section_start = pos + section_marker.size();
        const size_t section_end = html.find("\\\"", section_start);
        REQUIRE(section_end != std::string::npos);
        const std::string section = html.substr(section_start, section_end - section_start);
        pos = section_end + 2;

        if (section == "system_env") {
            continue;
        }

        size_t line_start = html.rfind('\n', section_start);
        line_start = line_start == std::string::npos ? 0 : line_start + 1;
        size_t line_end = html.find('\n', section_start);
        line_end = line_end == std::string::npos ? html.size() : line_end;
        const std::string line = html.substr(line_start, line_end - line_start);

        const std::string id_marker = "id=\\\"";
        const size_t id_pos = line.find(id_marker);
        REQUIRE(id_pos != std::string::npos);
        const size_t id_start = id_pos + id_marker.size();
        const size_t id_end = line.find("\\\"", id_start);
        REQUIRE(id_end != std::string::npos);
        const std::string id = line.substr(id_start, id_end - id_start);
        const std::string prefix = section + "_";

        REQUIRE_MESSAGE(id.find(prefix) == 0, "config control id must be section_key: " << id);
        rendered_fields.insert(section + "." + id.substr(prefix.size()));
    }

    REQUIRE(!rendered_fields.empty());
    for (std::set<std::string>::const_iterator it = rendered_fields.begin(); it != rendered_fields.end(); ++it) {
        const std::string& field = *it;
        const size_t dot = field.find('.');
        REQUIRE(dot != std::string::npos);
        const std::string section = field.substr(0, dot);
        const std::string key = field.substr(dot + 1);
        const std::string guard = "{\"" + section + "\", \"" + key + "\", CONFIG_FIELD_";

        CHECK_MESSAGE(source.find(guard) != std::string::npos,
                      "rendered config field is missing from validate_known_config_field_types: " << field);
    }
}
