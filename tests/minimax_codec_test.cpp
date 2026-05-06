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
    CHECK(std::string(chunks[0].begin(), chunks[0].end()) == "Hello");
}

TEST_CASE("stream parser waits for object completion across chunk boundaries") {
    StreamParser parser;
    const char* part1 = "{\"data\":{\"audio\":\"4865";
    const char* part2 = "6c6c6f\"}}";
    auto first = parser.feed(part1, std::strlen(part1));
    CHECK(first.empty());

    auto second = parser.feed(part2, std::strlen(part2));
    REQUIRE(second.size() == 1);
    CHECK(std::string(second[0].begin(), second[0].end()) == "Hello");
}

TEST_CASE("stream parser handles escaped quotes inside unrelated strings") {
    StreamParser parser;
    const char* json = "{\"trace\":\"brace \\\"}\\\" inside\",\"data\":{\"audio\":\"41\"}}";
    auto chunks = parser.feed(json, std::strlen(json));
    REQUIRE(chunks.size() == 1);
    CHECK(chunks[0].size() == 1);
    CHECK(chunks[0][0] == 'A');
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
    CHECK(chunks[0].size() == 1);
    CHECK(chunks[0][0] == 'A');
}

TEST_CASE("stream parser emits multiple audio payloads from one input") {
    StreamParser parser;
    std::string stream =
        "{\"data\":{\"audio\":\"41\"}}"
        "{\"data\":{\"audio\":\"42\"}}"
        "{\"data\":{\"audio\":\"43\"}}";
    auto chunks = parser.feed(stream.c_str(), stream.size());
    REQUIRE(chunks.size() == 3);
    CHECK(chunks[0][0] == 'A');
    CHECK(chunks[1][0] == 'B');
    CHECK(chunks[2][0] == 'C');
}

TEST_CASE("stream parser reset clears partial buffered state") {
    StreamParser parser;
    const char* part = "{\"data\":{\"audio\":\"41";
    auto first = parser.feed(part, std::strlen(part));
    CHECK(first.empty());

    parser.reset();

    const char* whole = "{\"data\":{\"audio\":\"42\"}}";
    auto second = parser.feed(whole, std::strlen(whole));
    REQUIRE(second.size() == 1);
    CHECK(second[0][0] == 'B');
}
