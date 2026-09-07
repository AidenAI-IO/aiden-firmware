#include "doctest.h"
#include "audio_playback_session.h"
#include "audio_player_test_hooks.h"

#include <chrono>
#include <future>

namespace {

class ScopedAudioPlayerOperationBlock {
public:
    explicit ScopedAudioPlayerOperationBlock(aiden::test::AudioPlayerOperation operation)
        : operation_(operation) {
        aiden::test::block_audio_player_operation(operation_);
    }

    ~ScopedAudioPlayerOperationBlock() {
        aiden::test::unblock_audio_player_operation(operation_);
    }

private:
    aiden::test::AudioPlayerOperation operation_;
};

}  // namespace

TEST_CASE("playback tail drain grace covers AO queued chunks") {
    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    const auto grace = aiden::playback_tail_drain_grace(fmt, 4096);

    CHECK(grace >= std::chrono::milliseconds(800));
}

TEST_CASE("volume updates are serialized with active playback") {
    using aiden::test::AudioPlayerOperation;

    aiden::test::reset_audio_player_test_state();

    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    aiden::AudioPlaybackSession session(1, fmt);
    REQUIRE(session.start());
    std::future<bool> volume_result;
    std::promise<void> volume_started;
    std::future<void> volume_started_result = volume_started.get_future();
    ScopedAudioPlayerOperationBlock blocked_play(AudioPlayerOperation::PLAY);

    const uint8_t pcm[] = {0, 0};
    REQUIRE(session.push_chunk(pcm, sizeof(pcm), false) == aiden::AidenServiceStatus::OK);
    REQUIRE(aiden::test::wait_for_audio_player_operation(
        AudioPlayerOperation::PLAY, std::chrono::seconds(1)));

    volume_result = std::async(std::launch::async, [&session, &volume_started] {
        volume_started.set_value();
        return session.set_volume(42);
    });
    REQUIRE(volume_started_result.wait_for(std::chrono::seconds(1)) ==
            std::future_status::ready);

    CHECK_FALSE(aiden::test::wait_for_audio_player_operation(
        AudioPlayerOperation::SET_VOLUME, std::chrono::milliseconds(100)));

    aiden::test::unblock_audio_player_operation(AudioPlayerOperation::PLAY);
    REQUIRE(volume_result.wait_for(std::chrono::seconds(1)) == std::future_status::ready);
    CHECK(volume_result.get());

    session.stop();
    CHECK(aiden::test::max_concurrent_audio_player_operations() == 1);
}

TEST_CASE("player stop is serialized with an active volume update") {
    using aiden::test::AudioPlayerOperation;

    aiden::test::reset_audio_player_test_state();

    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    aiden::AudioPlaybackSession session(2, fmt);
    REQUIRE(session.start());
    std::future<bool> volume_result;
    std::promise<void> volume_started;
    std::future<void> volume_started_result = volume_started.get_future();
    ScopedAudioPlayerOperationBlock blocked_volume(AudioPlayerOperation::SET_VOLUME);

    volume_result = std::async(std::launch::async, [&session, &volume_started] {
        volume_started.set_value();
        return session.set_volume(37);
    });
    REQUIRE(volume_started_result.wait_for(std::chrono::seconds(1)) ==
            std::future_status::ready);
    REQUIRE(aiden::test::wait_for_audio_player_operation(
        AudioPlayerOperation::SET_VOLUME, std::chrono::seconds(1)));

    std::promise<void> stop_started;
    std::future<void> stop_started_result = stop_started.get_future();
    std::future<void> stop_result = std::async(std::launch::async, [&session, &stop_started] {
        stop_started.set_value();
        session.stop();
    });
    REQUIRE(stop_started_result.wait_for(std::chrono::seconds(1)) ==
            std::future_status::ready);

    CHECK_FALSE(aiden::test::wait_for_audio_player_operation(
        AudioPlayerOperation::STOP, std::chrono::milliseconds(100)));

    aiden::test::unblock_audio_player_operation(AudioPlayerOperation::SET_VOLUME);
    REQUIRE(volume_result.wait_for(std::chrono::seconds(1)) == std::future_status::ready);
    CHECK(volume_result.get());
    REQUIRE(stop_result.wait_for(std::chrono::seconds(1)) == std::future_status::ready);
    stop_result.get();

    CHECK(aiden::test::max_concurrent_audio_player_operations() == 1);
}
