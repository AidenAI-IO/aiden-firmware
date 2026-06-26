#define private public
#include "audio_session_manager.h"
#undef private

#include "doctest.h"

TEST_CASE("AudioSessionManager keeps a stopped record session readable until EOF") {
    aiden::AudioSessionManager manager;

    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    const uint64_t session_id = 42;
    auto session = std::make_shared<aiden::AudioRecordSession>(session_id, fmt);
    {
        std::lock_guard<std::mutex> lock(session->mutex_);
        session->queue_.push(std::vector<uint8_t>{1, 2, 3, 4});
    }
    session->cv_.notify_all();

    {
        std::lock_guard<std::mutex> lock(manager.mutex_);
        manager.record_sessions_[session_id] = session;
        manager.record_last_active_[session_id] = aiden::AudioSessionManager::Clock::now();
    }

    CHECK(manager.stop_recording(session_id) == aiden::AidenServiceStatus::OK);

    aiden::AudioChunkResult chunk;
    CHECK(manager.read_record_chunk(session_id, 1, &chunk) == aiden::AidenServiceStatus::OK);
    CHECK(chunk.end_of_stream == false);
    CHECK(chunk.pcm == std::vector<uint8_t>{1, 2, 3, 4});

    aiden::AudioChunkResult end;
    CHECK(manager.read_record_chunk(session_id, 1, &end) == aiden::AidenServiceStatus::OK);
    CHECK(end.end_of_stream == true);
    CHECK(end.pcm.empty());

    aiden::AudioChunkResult missing;
    CHECK(manager.read_record_chunk(session_id, 1, &missing) ==
          aiden::AidenServiceStatus::SESSION_NOT_FOUND);
}

TEST_CASE("AudioSessionManager rejects new playback while another session is draining") {
    aiden::AudioSessionManager manager;

    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    manager.draining_playback_count_->store(1, std::memory_order_relaxed);

    aiden::PlaybackStartResult out;
    CHECK(manager.start_playback(fmt, &out) == aiden::AidenServiceStatus::SERVICE_RECOVERING);
}

TEST_CASE("AudioSessionManager stops draining playback sessions") {
    aiden::AudioSessionManager manager;

    aiden::AudioFormat fmt;
    fmt.sample_rate = 16000;
    fmt.channels = 1;
    fmt.bit_width = 16;

    const uint64_t session_id = 99;
    auto session = std::make_shared<aiden::AudioPlaybackSession>(session_id, fmt);

    {
        std::lock_guard<std::mutex> lock(manager.mutex_);
        manager.draining_playback_sessions_[session_id] = session;
    }

    CHECK(manager.stop_playback(session_id) == aiden::AidenServiceStatus::OK);
    CHECK(session->is_stopped());
    CHECK(manager.stop_playback(session_id) == aiden::AidenServiceStatus::SESSION_NOT_FOUND);
}
