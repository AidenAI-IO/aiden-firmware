#include "doctest.h"

#define private public
#include "audio_playback_session.h"
#undef private

#include <atomic>
#include <chrono>
#include <thread>
#include <vector>

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

TEST_CASE("backpressure writers fail when final state is published") {
    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    aiden::AudioPlaybackSession session(2, fmt);

    const uint8_t chunk[] = {1, 2, 3, 4};
    const size_t max_queue_chunks = 128;
    {
        std::lock_guard<std::mutex> lock(session.mutex_);
        for (size_t i = 0; i < max_queue_chunks; ++i) {
            session.queue_.push(std::vector<uint8_t>(chunk, chunk + sizeof(chunk)));
        }
    }

    const size_t writer_count = 8;
    std::atomic<size_t> ready(0);
    std::vector<aiden::AidenServiceStatus> statuses(
        writer_count, aiden::AidenServiceStatus::INTERNAL_ERROR);
    std::unique_lock<std::mutex> session_lock(session.mutex_);
    std::vector<std::thread> writers;
    writers.reserve(writer_count);
    for (size_t i = 0; i < writer_count; ++i) {
        writers.push_back(std::thread([&session, &chunk, &ready, &statuses, i]() {
            ready.fetch_add(1, std::memory_order_release);
            statuses[i] = session.push_chunk(chunk, sizeof(chunk), false);
        }));
    }
    while (ready.load(std::memory_order_acquire) != writer_count) {
        std::this_thread::yield();
    }
    session_lock.unlock();
    std::this_thread::sleep_for(std::chrono::milliseconds(20));

    // Publish the final state under the same mutex as push_chunk(). Removing
    // one queued chunk models playback making room for the final chunk.
    session_lock.lock();
    CHECK(session.queue_.size() == max_queue_chunks);
    session.queue_.pop();
    session.queue_.push(std::vector<uint8_t>(chunk, chunk + sizeof(chunk)));
    session.final_received_ = true;
    session_lock.unlock();
    session.cv_.notify_all();

    for (size_t i = 0; i < writers.size(); ++i) writers[i].join();

    for (size_t i = 0; i < writer_count; ++i) {
        CHECK(statuses[i] == aiden::AidenServiceStatus::SESSION_NOT_FOUND);
    }
}
