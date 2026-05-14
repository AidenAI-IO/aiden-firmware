#include "doctest.h"
#include "service_status.h"
#include "frame_service_protocol.h"
#include <string>

using aiden::AidenServiceStatus;
using aiden::service_status_to_string;
using aiden::service_status_from_string;

TEST_CASE("service_status_to_string returns stable protocol strings") {
    CHECK(std::string(service_status_to_string(AidenServiceStatus::OK))                == "OK");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::NO_NEW_FRAME))      == "NO_NEW_FRAME");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::FRAME_NOT_FOUND))   == "FRAME_NOT_FOUND");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::SESSION_NOT_FOUND)) == "SESSION_NOT_FOUND");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::SERVICE_RECOVERING))== "SERVICE_RECOVERING");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::TIMEOUT))           == "TIMEOUT");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::TRANSPORT_ERROR))   == "TRANSPORT_ERROR");
    CHECK(std::string(service_status_to_string(AidenServiceStatus::INTERNAL_ERROR))    == "INTERNAL_ERROR");
}

TEST_CASE("service_status_from_string parses known protocol strings") {
    AidenServiceStatus s = AidenServiceStatus::INTERNAL_ERROR;

    REQUIRE(service_status_from_string("OK", &s));
    CHECK(s == AidenServiceStatus::OK);

    REQUIRE(service_status_from_string("SESSION_NOT_FOUND", &s));
    CHECK(s == AidenServiceStatus::SESSION_NOT_FOUND);

    REQUIRE(service_status_from_string("TIMEOUT", &s));
    CHECK(s == AidenServiceStatus::TIMEOUT);

    REQUIRE(service_status_from_string("TRANSPORT_ERROR", &s));
    CHECK(s == AidenServiceStatus::TRANSPORT_ERROR);
}

TEST_CASE("service_status_from_string rejects unknown and null inputs") {
    AidenServiceStatus s = AidenServiceStatus::OK;

    CHECK_FALSE(service_status_from_string("UNKNOWN", &s));
    CHECK(s == AidenServiceStatus::OK);  // unchanged on failure

    CHECK_FALSE(service_status_from_string(nullptr, &s));
    CHECK_FALSE(service_status_from_string("OK", nullptr));
}

TEST_CASE("FrameServiceStatus alias resolves to AidenServiceStatus") {
    // FrameServiceStatus is a typedef alias; verify the wire strings are identical
    // so frame_service and audio_service share the same protocol vocabulary.
    using aiden::FrameServiceStatus;
    CHECK(std::string(service_status_to_string(FrameServiceStatus::OK)) == "OK");
    CHECK(std::string(service_status_to_string(FrameServiceStatus::TIMEOUT)) == "TIMEOUT");
}
