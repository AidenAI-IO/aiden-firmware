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
    CHECK(source.find("config.screenshot_keep_n") != std::string::npos);
    CHECK(source.find("config.screenshot_prune_interval") != std::string::npos);
    CHECK(source.find("screenshot_keep_n must be >= 0") != std::string::npos);
    CHECK(source.find("screenshot_prune_interval must be >= 0") != std::string::npos);

    CHECK(html.find("agent_screenshot_keep_n") != std::string::npos);
    CHECK(html.find("agent_screenshot_prune_interval") != std::string::npos);
    CHECK(html.find("['screenshot_keep_n','number']") != std::string::npos);
    CHECK(html.find("['screenshot_prune_interval','number']") != std::string::npos);
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
    CHECK(source.find("telemetry.base_url is required when telemetry.enabled is true") != std::string::npos);
    CHECK(source.find("telemetry.public_key is required when telemetry.enabled is true") != std::string::npos);
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
    CHECK(html.find("['tags','list']") != std::string::npos);
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
    CHECK(source.find("hid.pointer_mode must be absolute or touchscreen") != std::string::npos);
    CHECK(html.find("hid_pointer_mode") != std::string::npos);
    CHECK(html.find("['pointer_mode','text']") != std::string::npos);
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
