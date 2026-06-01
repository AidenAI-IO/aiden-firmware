#include "system_env_parser.h"

#include <doctest.h>

#include <string>
#include <vector>

TEST_CASE("system env parser accepts multiple export assignments") {
    std::vector<aiden::EnvAssignment> assignments;
    aiden::SystemEnvProxy proxy;
    std::string error;

    const std::string content =
        "export https_proxy=http://proxy.example:7893 http_proxy=http://proxy.example:7893 all_proxy=socks5://proxy.example:7893\n";

    REQUIRE(aiden::parse_system_env_content(content, &assignments, &proxy, &error));
    CHECK(assignments.size() == 3);
    CHECK(proxy.https_proxy == "http://proxy.example:7893");
    CHECK(proxy.http_proxy == "http://proxy.example:7893");
    CHECK(proxy.all_proxy == "socks5://proxy.example:7893");
}

TEST_CASE("system env parser rejects shell control syntax") {
    std::string error;

    CHECK_FALSE(aiden::parse_system_env_content("touch /tmp/pwn; exit 0\n", NULL, NULL, &error));
    CHECK(error.find("unsupported system env statement") != std::string::npos);
}

TEST_CASE("system env parser preserves proxy whitespace for validation") {
    aiden::SystemEnvProxy proxy;
    std::string error;

    REQUIRE(aiden::parse_system_env_content("HTTP_PROXY=' http://proxy.example:18080'\n", NULL, &proxy, &error));
    CHECK(proxy.http_proxy == " http://proxy.example:18080");
    CHECK(aiden::validate_system_proxy_url(proxy.http_proxy).find("whitespace") != std::string::npos);
}

TEST_CASE("system proxy URL validation matches runtime proxy rules") {
    CHECK(aiden::validate_system_proxy_url("http://").find("expected absolute proxy URL") != std::string::npos);
    CHECK(aiden::validate_system_proxy_url("socks4://127.0.0.1:7890").find("unsupported proxy URL scheme") != std::string::npos);
    CHECK(aiden::validate_system_proxy_url("socks5://127.0.0.1:7890").empty());
    CHECK(aiden::validate_system_proxy_url("http://proxy.example:7893 http_proxy=http://proxy.example:7893").find("whitespace") != std::string::npos);
    CHECK(aiden::validate_system_proxy_url("http://%zz").find("invalid URL escape") != std::string::npos);
    CHECK_FALSE(aiden::validate_system_proxy_url("http://proxy.example:bad").empty());
    CHECK_FALSE(aiden::validate_system_proxy_url("http://[::1]zzz").empty());
    CHECK_FALSE(aiden::validate_system_proxy_url(std::string("http://proxy.example") + std::string(1, '\x01')).empty());
    CHECK_FALSE(aiden::validate_system_proxy_url("http://foo\\bar").empty());
    CHECK_FALSE(aiden::validate_system_proxy_url("http://foo^bar").empty());
    CHECK_FALSE(aiden::validate_system_proxy_url("http://foo|bar").empty());
    CHECK_FALSE(aiden::validate_system_proxy_url("http://foo%2fbar").empty());
    CHECK_FALSE(aiden::validate_system_proxy_url("http://foo%00bar").empty());
    CHECK(aiden::validate_system_proxy_url("http://foo%25bar").empty());
}

TEST_CASE("system env proxy filtering preserves unrelated assignments on mixed lines") {
    std::string rendered;
    std::string error;

    const std::string content =
        "# custom env\n"
        "export HTTP_PROXY=http://proxy.example:7890 OPENAI_API_KEY=sk-example\n"
        "AIDEN_MODE=dev\n"
        "# Managed by config_web: system proxy\n";

    REQUIRE(aiden::render_system_env_without_proxy_assignments(
        content,
        "# Managed by config_web: system proxy",
        &rendered,
        &error));

    CHECK(rendered.find("HTTP_PROXY=") == std::string::npos);
    CHECK(rendered.find("OPENAI_API_KEY=sk-example") != std::string::npos);
    CHECK(rendered.find("AIDEN_MODE=dev") != std::string::npos);
    CHECK(rendered.find("# custom env") != std::string::npos);
    CHECK(rendered.find("# Managed by config_web: system proxy") == std::string::npos);
}
