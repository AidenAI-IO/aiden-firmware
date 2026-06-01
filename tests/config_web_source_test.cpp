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

    CHECK(html.find("Agent 状态") != std::string::npos);
    CHECK(html.find("agentProcessStatus") != std::string::npos);
    CHECK(html.find("agentPortStatus") != std::string::npos);
    CHECK(html.find("renderAgentStatus") != std::string::npos);
    CHECK(html.find("startup_error") != std::string::npos);
}
