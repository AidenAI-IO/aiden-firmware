#include "doctest.h"
#include "llm_api_url.h"
#include <string>

using aiden::build_chat_completions_url;

TEST_CASE("build_chat_completions_url uses default base when config is empty") {
    CHECK(build_chat_completions_url("", "https://api.openai.com/v1") ==
          "https://api.openai.com/v1/chat/completions");
}

TEST_CASE("build_chat_completions_url uses configured base_url") {
    CHECK(build_chat_completions_url("https://proxy.example/v1", "https://api.openai.com/v1") ==
          "https://proxy.example/v1/chat/completions");
}

TEST_CASE("build_chat_completions_url trims trailing slash from base_url") {
    CHECK(build_chat_completions_url("https://proxy.example/v1/", "https://api.openai.com/v1") ==
          "https://proxy.example/v1/chat/completions");
}
