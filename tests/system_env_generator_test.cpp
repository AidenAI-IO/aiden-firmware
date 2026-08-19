#include "system_env_generator.h"

#include <doctest.h>

#include <string>

TEST_CASE("system environment generator normalizes proxy variables") {
    aiden::GeneratedSystemEnv generated;
    REQUIRE(aiden::generate_systemd_environment(
        "HTTPS_PROXY=http://proxy.example:7890\nOPENAI_API_KEY='key with spaces'\n",
        &generated));
    REQUIRE(generated.valid);
    CHECK(generated.content.find("HTTPS_PROXY=\"http://proxy.example:7890\"") !=
          std::string::npos);
    CHECK(generated.content.find("https_proxy=\"http://proxy.example:7890\"") !=
          std::string::npos);
    CHECK(generated.content.find("NO_PROXY=\"localhost,127.0.0.1,::1") !=
          std::string::npos);
    CHECK(generated.content.find("OPENAI_API_KEY=\"key with spaces\"") !=
          std::string::npos);
}

TEST_CASE("system environment generator rejects execution boundary variables") {
    aiden::GeneratedSystemEnv generated;
    REQUIRE(aiden::generate_systemd_environment("LD_PRELOAD=/tmp/inject.so\n", &generated));
    CHECK_FALSE(generated.valid);
    CHECK(generated.error.find("not approved") != std::string::npos);
    CHECK(generated.content.find("LD_PRELOAD") == std::string::npos);
    CHECK(generated.content.find("AIDEN_EXTERNAL_ACCESS_DISABLED=\"1\"") !=
          std::string::npos);
}

TEST_CASE("system environment generator rejects shell statements atomically") {
    aiden::GeneratedSystemEnv generated;
    REQUIRE(aiden::generate_systemd_environment(
        "OPENAI_API_KEY=valid\ntouch /tmp/pwn\n", &generated));
    CHECK_FALSE(generated.valid);
    CHECK(generated.content.find("OPENAI_API_KEY") == std::string::npos);
    CHECK(generated.error.find("unsupported system env statement") != std::string::npos);
}

TEST_CASE("system environment generator rejects malformed proxy URLs") {
    aiden::GeneratedSystemEnv generated;
    REQUIRE(aiden::generate_systemd_environment(
        "HTTP_PROXY=http://http://proxy.example:7890\n", &generated));
    CHECK_FALSE(generated.valid);
    CHECK(generated.error.find("duplicate scheme") != std::string::npos);
}
