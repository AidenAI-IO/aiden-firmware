#pragma once

#include "aiden_sdk.h"
#include "audio_service_protocol.h"
#include <atomic>
#include <condition_variable>
#include <cstdint>
#include <mutex>
#include <queue>
#include <thread>
#include <vector>

namespace aiden {

// Manages a single speaker playback session.
// Callers push PCM chunks via push_chunk(); the session drains them to
// AudioPlayer on a dedicated thread.
class AudioPlaybackSession {
public:
    static const size_t kMaxQueueChunks = 128;

    explicit AudioPlaybackSession(uint64_t session_id, const AudioFormat& fmt);
    ~AudioPlaybackSession();

    uint64_t id() const { return session_id_; }

    // Start the playback thread. Returns false if AudioPlayer init fails.
    bool start();

    // Enqueue a PCM chunk for playback.
    // is_final: when true, the playback thread drains remaining chunks then exits.
    AidenServiceStatus push_chunk(const uint8_t* data, size_t len, bool is_final);

    // Abort playback immediately.
    void stop();

    bool is_stopped() const { return stopped_.load(); }

    bool set_volume(int volume);
    int get_volume() const;

    // Block until all queued audio has been played (or stop() is called).
    void wait_until_done();

private:
    void playback_loop();

    uint64_t session_id_;
    AudioFormat fmt_;
    AudioPlayer player_;
    std::thread playback_thread_;
    std::mutex mutex_;
    std::condition_variable cv_;
    std::queue<std::vector<uint8_t>> queue_;
    std::atomic<bool> stopped_;
    bool final_received_;
};

}  // namespace aiden
