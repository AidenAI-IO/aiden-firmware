#include <doctest.h>

#include <fstream>
#include <sstream>
#include <string>

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

    CHECK(html.find("Agent 实时日志") != std::string::npos);
    CHECK(html.find("agentLogText") != std::string::npos);
    CHECK(html.find("agentLogMeta") != std::string::npos);
    CHECK(html.find("refreshAgentLog") != std::string::npos);
    CHECK(html.find("/api/agent/logs") != std::string::npos);
    CHECK(html.find("setInterval(function(){refreshAgentLog(false);},2000)") != std::string::npos);
}

TEST_CASE("config web docs list token_env in model fields") {
    const std::string doc_path = std::string(AIDEN_SOURCE_DIR) + "/docs/03-services/config-web.md";
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
    CHECK(source.find("/var/log/ota/ota.log") == std::string::npos);
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
    const std::string::size_type fw_actions_pos = html.find("id=\\\"fwActions\\\"");
    const std::string::size_type ota_button_pos = html.find("id=\\\"otaUpdateBtn\\\"");
    const std::string::size_type wifi_heading_pos = html.find("Wi-Fi 配置");
    REQUIRE(fw_actions_pos != std::string::npos);
    REQUIRE(ota_button_pos != std::string::npos);
    REQUIRE(wifi_heading_pos != std::string::npos);
    CHECK(fw_actions_pos < ota_button_pos);
    CHECK(ota_button_pos < wifi_heading_pos);
    CHECK(html.find("OTA 实时日志") != std::string::npos);
    CHECK(html.find("otaLogPanel") != std::string::npos);
    CHECK(html.find("id=\\\"otaLogPanel\\\" class=\\\"card ota-log-panel\\\"") != std::string::npos);
    CHECK(html.find(".ota-log-panel{display:none}") != std::string::npos);
    CHECK(html.find("showOtaLogPanel") != std::string::npos);
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
    CHECK(html.find("triggerOtaUpdate(){const btn=byId('otaUpdateBtn');setOtaLogPending(") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate(){const btn=byId('otaUpdateBtn');showOtaLogPanel();") == std::string::npos);
    CHECK(html.find("otaLogText") != std::string::npos);
    CHECK(html.find("otaLogMeta") != std::string::npos);
    CHECK(html.find("triggerOtaUpdate") != std::string::npos);
    CHECK(html.find("refreshOtaLog") != std::string::npos);
    CHECK(html.find("/api/ota/update") != std::string::npos);
    CHECK(html.find("/api/ota/check-now") == std::string::npos);
    CHECK(html.find("/api/ota/logs") != std::string::npos);
    CHECK(html.find("await refreshOtaLog(false)") == std::string::npos);
    CHECK(html.find("setInterval(function(){refreshOtaLog(false);},2000)") != std::string::npos);
}

TEST_CASE("ota open sources documentation references current docs paths") {
    const std::string doc_path = std::string(AIDEN_SOURCE_DIR) + "/docs/09-ota/OTA_OPEN_SOURCES.md";
    std::ifstream doc_in(doc_path.c_str());
    REQUIRE(doc_in.good());

    std::ostringstream doc_buffer;
    doc_buffer << doc_in.rdbuf();
    const std::string doc = doc_buffer.str();

    CHECK(doc.find("docs/09-ota/ota-external-developers.md") != std::string::npos);
    CHECK(doc.find("docs/09-ota/ota-quick-examples.md") != std::string::npos);
    CHECK(doc.find("docs/09-ota/ota-release-channels.md") != std::string::npos);
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
    CHECK(source.find("config.screenshot_keep_n") != std::string::npos);
    CHECK(source.find("config.screenshot_prune_interval") != std::string::npos);
    CHECK(source.find("config.screen_stable_timeout_ms") != std::string::npos);
    CHECK(source.find("config.screen_stable_ms") != std::string::npos);
    CHECK(source.find("config.screen_stable_diff_threshold") != std::string::npos);

    CHECK(html.find("agent_screenshot_keep_n") != std::string::npos);
    CHECK(html.find("agent_screenshot_prune_interval") != std::string::npos);
    CHECK(html.find("agent_screen_stable_timeout_ms") != std::string::npos);
    CHECK(html.find("agent_screen_stable_ms") != std::string::npos);
    CHECK(html.find("agent_screen_stable_diff_threshold") != std::string::npos);
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
    // not a hard-coded table in the HTML.
    const std::string meta_path = std::string(AIDEN_SOURCE_DIR) + "/src/agent/internal/agent/config_meta.go";
    std::ifstream meta_in(meta_path.c_str());
    REQUIRE(meta_in.good());
    std::ostringstream meta_buffer;
    meta_buffer << meta_in.rdbuf();
    const std::string meta = meta_buffer.str();
    CHECK(meta.find("\"brave-free\"") != std::string::npos);
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
    CHECK(source.find("case 503: response.status_text = \"Service Unavailable\";") != std::string::npos);
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
    CHECK(html.find("saveSystemEnv") != std::string::npos);
    CHECK(html.find("system_proxy") == std::string::npos);
    CHECK(html.find("section-proxy") == std::string::npos);
    CHECK(html.find("proxy_http_proxy") == std::string::npos);
    CHECK(html.find("proxy:[[") == std::string::npos);
    CHECK(html.find("Proxy example") == std::string::npos);
    CHECK(html.find("http_proxy=http://127.0.0.1:7890") == std::string::npos);
    CHECK(html.find("NO_PROXY=localhost,127.0.0.1,::1") == std::string::npos);
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

TEST_CASE("config web restarts ota only when system env changes") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    CHECK(source.find("kAgentInitScript") != std::string::npos);
    CHECK(source.find("kOtaInitScript") != std::string::npos);
    CHECK(source.find("schedule_agent_restart") != std::string::npos);
    CHECK(source.find("schedule_ota_restart") != std::string::npos);
    CHECK(source.find("bool system_env_changed = original_system_env != updated_system_env;") != std::string::npos);
    CHECK(source.find("if (system_env_changed)") != std::string::npos);
    CHECK(source.find("system env saved; services restarting") != std::string::npos);
    CHECK(source.find("config saved; agent restarting") != std::string::npos);
    CHECK(source.find("config saved; agent and ota restarting") == std::string::npos);
}

TEST_CASE("config web DHCP invokes udhcpc without hook") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    // Must invoke udhcpc for DHCP
    CHECK(source.find("udhcpc -i ") != std::string::npos);
    // Must NOT reference the removed aiden.script hook
    CHECK(source.find("-s /etc/udhcpc/aiden.script") == std::string::npos);
    // Should use -n -q for foreground one-shot DHCP
    CHECK(source.find("udhcpc -i \" + shell_quote(options.wifi_interface) + \" -n -q") != std::string::npos);
}

TEST_CASE("config web custom benchmark suite import endpoints") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    // Endpoints registered.
    CHECK(source.find("\"/benchmark/suites/import\"") != std::string::npos);
    CHECK(source.find("\"/benchmark/suites/delete\"") != std::string::npos);

    // Custom suites isolated under suites/custom/ and listing flags them.
    CHECK(source.find("/userdata/agent/benchmark/suites/custom") != std::string::npos);
    CHECK(source.find("/suites/custom/") != std::string::npos);
    CHECK(source.find("\"custom\"") != std::string::npos);

    // Validation delegated to runner.suite.load_suite (single source of truth).
    CHECK(source.find("from runner.suite import load_suite") != std::string::npos);

    // Name sanitisation: only [A-Za-z0-9_-], length-bounded.
    CHECK(source.find("isalnum(static_cast<unsigned char>(c)) || c == '-' || c == '_'") != std::string::npos);
    CHECK(source.find("safe_name.size() > 64") != std::string::npos);

    // Delete must only touch suites/custom/ (no arbitrary path).
    const std::string del_marker = "std::string dest_path = \"/userdata/agent/benchmark/suites/custom/\" + safe_name + \".json\";";
    CHECK(source.find(del_marker) != std::string::npos);
}

TEST_CASE("user files route generates report before serving it") {
    const std::string source_path = std::string(AIDEN_SOURCE_DIR) + "/src/config_web.cpp";
    std::ifstream source_in(source_path.c_str());
    REQUIRE(source_in.good());

    std::ostringstream source_buffer;
    source_buffer << source_in.rdbuf();
    const std::string source = source_buffer.str();

    const std::string route_marker = "request.method == \"GET\" && request.path == \"/user_files\"";
    const size_t route_pos = source.find(route_marker);
    REQUIRE(route_pos != std::string::npos);

    const size_t generate_pos = source.find("ensure_user_files_report", route_pos);
    const size_t read_pos = source.find("fopen(path.c_str(), \"r\")", route_pos);
    REQUIRE(read_pos != std::string::npos);

    CHECK(source.find("kUserFilesGenerateCommandTimeoutMs") != std::string::npos);
    CHECK(generate_pos != std::string::npos);
    CHECK(generate_pos < read_pos);
}

TEST_CASE("build image packages user files report tools") {
    const std::string build_path = std::string(AIDEN_SOURCE_DIR) + "/_build_image.sh";
    std::ifstream build_in(build_path.c_str());
    REQUIRE(build_in.good());

    std::ostringstream build_buffer;
    build_buffer << build_in.rdbuf();
    const std::string build_script = build_buffer.str();

    CHECK(build_script.find("agent_tools") != std::string::npos);
    CHECK(build_script.find("generate_agent_files_report.py") != std::string::npos);
    CHECK(build_script.find("agent_files_template.html") != std::string::npos);
    CHECK(build_script.find("view_agent_files.sh") != std::string::npos);
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
