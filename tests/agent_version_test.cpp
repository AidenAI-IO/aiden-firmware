#include "doctest.h"
#include "agent_version.h"
#include <string>

TEST_CASE("agent version defaults to unknown without build injection") {
    CHECK(std::string(aiden::agent_version()) == "unknown");
}

TEST_CASE("agent commit time defaults to unknown without build injection") {
    CHECK(std::string(aiden::agent_commit_time()) == "unknown");
}

TEST_CASE("agent version command accepts version subcommand") {
    char arg0[] = "agent_main";
    char arg1[] = "version";
    char* argv[] = {arg0, arg1};

    CHECK(aiden::is_agent_version_command(2, argv));
}

TEST_CASE("agent version command accepts --version flag") {
    char arg0[] = "agent_main";
    char arg1[] = "--version";
    char* argv[] = {arg0, arg1};

    CHECK(aiden::is_agent_version_command(2, argv));
}

TEST_CASE("agent version command accepts -v flag") {
    char arg0[] = "agent_main";
    char arg1[] = "-v";
    char* argv[] = {arg0, arg1};

    CHECK(aiden::is_agent_version_command(2, argv));
}

TEST_CASE("agent version command requires only the version argument") {
    char arg0[] = "agent_main";
    char arg1[] = "version";
    char arg2[] = "agent.conf";
    char* no_args[] = {arg0};
    char* extra_arg[] = {arg0, arg1, arg2};
    char* other_arg[] = {arg0, arg2};

    CHECK_FALSE(aiden::is_agent_version_command(1, no_args));
    CHECK_FALSE(aiden::is_agent_version_command(3, extra_arg));
    CHECK_FALSE(aiden::is_agent_version_command(2, other_arg));
}
