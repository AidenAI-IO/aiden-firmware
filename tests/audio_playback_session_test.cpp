#include "doctest.h"
#include "audio_playback_session.h"

TEST_CASE("playback tail drain grace covers AO queued chunks") {
    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    const auto grace = aiden::playback_tail_drain_grace(fmt, 4096);

    CHECK(grace >= std::chrono::milliseconds(800));
}

TEST_CASE("playback session rejects chunks after final is received") {
    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    aiden::AudioPlaybackSession session(1, fmt);
    REQUIRE(session.start());

    const uint8_t chunk[] = {1, 2, 3, 4};
    REQUIRE(session.push_chunk(chunk, sizeof(chunk), true) ==
            aiden::AidenServiceStatus::OK);
    CHECK(session.push_chunk(chunk, sizeof(chunk), false) ==
          aiden::AidenServiceStatus::SESSION_NOT_FOUND);

    session.stop();
}
