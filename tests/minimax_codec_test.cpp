#include "doctest.h"
#include "minimax_codec.h"
#include <cstring>
#include <string>
#include <vector>

using aiden::minimax::hex_decode;
using aiden::minimax::StreamParser;

TEST_CASE("hex_decode decodes lowercase and uppercase hex") {
    auto lower = hex_decode("48656c6c6f");
    REQUIRE(lower.size() == 5);
    CHECK(std::string(lower.begin(), lower.end()) == "Hello");

    auto upper = hex_decode("00FF10AB");
    REQUIRE(upper.size() == 4);
    CHECK(upper[0] == 0x00);
    CHECK(upper[1] == 0xFF);
    CHECK(upper[2] == 0x10);
    CHECK(upper[3] == 0xAB);
}

TEST_CASE("hex_decode ignores trailing odd nibble") {
    auto bytes = hex_decode("4142F");
    REQUIRE(bytes.size() == 2);
    CHECK(bytes[0] == 'A');
    CHECK(bytes[1] == 'B');
}

TEST_CASE("stream parser emits decoded audio from a single complete object") {
    StreamParser parser;
    const char* json = R"({"data":{"audio":"48656c6c6f"}})";
    auto chunks = parser.feed(json, std::strlen(json));
    REQUIRE(chunks.size() == 1);
    CHECK_FALSE(chunks[0].reset_decoder);
    CHECK(std::string(chunks[0].audio.begin(), chunks[0].audio.end()) == "Hello");
}

TEST_CASE("stream parser waits for object completion across chunk boundaries") {
    StreamParser parser;
    const char* part1 = "{\"data\":{\"audio\":\"4865";
    const char* part2 = "6c6c6f\"}}";
    auto first = parser.feed(part1, std::strlen(part1));
    CHECK(first.empty());

    auto second = parser.feed(part2, std::strlen(part2));
    REQUIRE(second.size() == 1);
    CHECK_FALSE(second[0].reset_decoder);
    CHECK(std::string(second[0].audio.begin(), second[0].audio.end()) == "Hello");
}

TEST_CASE("stream parser handles escaped quotes inside unrelated strings") {
    StreamParser parser;
    const char* json = "{\"trace\":\"brace \\\"}\\\" inside\",\"data\":{\"audio\":\"41\"}}";
    auto chunks = parser.feed(json, std::strlen(json));
    REQUIRE(chunks.size() == 1);
    CHECK_FALSE(chunks[0].reset_decoder);
    CHECK(chunks[0].audio.size() == 1);
    CHECK(chunks[0].audio[0] == 'A');
}

TEST_CASE("stream parser skips objects without audio") {
    StreamParser parser;
    const char* json = R"({"data":{"other":"x"}})";
    auto chunks = parser.feed(json, std::strlen(json));
    CHECK(chunks.empty());
}

TEST_CASE("stream parser skips invalid JSON objects and continues") {
    StreamParser parser;
    std::string stream = "{bad json}{\"data\":{\"audio\":\"41\"}}";
    auto chunks = parser.feed(stream.c_str(), stream.size());
    REQUIRE(chunks.size() == 1);
    CHECK_FALSE(chunks[0].reset_decoder);
    CHECK(chunks[0].audio.size() == 1);
    CHECK(chunks[0].audio[0] == 'A');
}

TEST_CASE("stream parser emits multiple audio payloads from one input") {
    StreamParser parser;
    std::string stream =
        "{\"data\":{\"audio\":\"41\"}}"
        "{\"data\":{\"audio\":\"42\"}}"
        "{\"data\":{\"audio\":\"43\"}}";
    auto chunks = parser.feed(stream.c_str(), stream.size());
    REQUIRE(chunks.size() == 3);
    CHECK_FALSE(chunks[0].reset_decoder);
    CHECK(chunks[0].audio[0] == 'A');
    CHECK_FALSE(chunks[1].reset_decoder);
    CHECK(chunks[1].audio[0] == 'B');
    CHECK_FALSE(chunks[2].reset_decoder);
    CHECK(chunks[2].audio[0] == 'C');
}

TEST_CASE("stream parser emits only delta when payload is cumulative") {
    StreamParser parser;
    const char* first = R"({"data":{"audio":"4142"}})";
    const char* second = R"({"data":{"audio":"41424344"}})";

    auto first_chunks = parser.feed(first, std::strlen(first));
    REQUIRE(first_chunks.size() == 1);
    CHECK_FALSE(first_chunks[0].reset_decoder);
    CHECK(std::string(first_chunks[0].audio.begin(), first_chunks[0].audio.end()) == "AB");

    auto second_chunks = parser.feed(second, std::strlen(second));
    REQUIRE(second_chunks.size() == 1);
    CHECK_FALSE(second_chunks[0].reset_decoder);
    CHECK(std::string(second_chunks[0].audio.begin(), second_chunks[0].audio.end()) == "CD");
}

TEST_CASE("stream parser ignores identical repeated audio payloads") {
    StreamParser parser;
    const char* json = R"({"data":{"audio":"4142"}})";

    auto first_chunks = parser.feed(json, std::strlen(json));
    REQUIRE(first_chunks.size() == 1);
    CHECK_FALSE(first_chunks[0].reset_decoder);
    CHECK(std::string(first_chunks[0].audio.begin(), first_chunks[0].audio.end()) == "AB");

    auto second_chunks = parser.feed(json, std::strlen(json));
    CHECK(second_chunks.empty());
}

TEST_CASE("stream parser ignores regressive snapshots") {
    StreamParser parser;
    const char* first = R"({"data":{"audio":"41424344"}})";
    const char* second = R"({"data":{"audio":"4142"}})";

    auto first_chunks = parser.feed(first, std::strlen(first));
    REQUIRE(first_chunks.size() == 1);
    CHECK(std::string(first_chunks[0].audio.begin(), first_chunks[0].audio.end()) == "ABCD");

    auto second_chunks = parser.feed(second, std::strlen(second));
    CHECK(second_chunks.empty());
}

TEST_CASE("stream parser emits unrelated snapshots without reset") {
    StreamParser parser;
    const char* first = R"({"data":{"audio":"4142"}})";
    const char* second = R"({"data":{"audio":"4344"}})";

    auto first_chunks = parser.feed(first, std::strlen(first));
    REQUIRE(first_chunks.size() == 1);
    CHECK(std::string(first_chunks[0].audio.begin(), first_chunks[0].audio.end()) == "AB");

    auto second_chunks = parser.feed(second, std::strlen(second));
    REQUIRE(second_chunks.size() == 1);
    CHECK_FALSE(second_chunks[0].reset_decoder);
    CHECK(std::string(second_chunks[0].audio.begin(), second_chunks[0].audio.end()) == "CD");
}

