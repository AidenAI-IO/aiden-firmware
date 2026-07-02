#include "audio_playback_session.h"
#include <chrono>
#include <stdio.h>
#include <thread>

namespace aiden {

AudioPlaybackSession::AudioPlaybackSession(uint64_t session_id, const AudioFormat& fmt)
    : session_id_(session_id), fmt_(fmt), stopped_(false), final_received_(false) {}

AudioPlaybackSession::~AudioPlaybackSession() {
    stop();
}

bool AudioPlaybackSession::start() {
    AudioConfig cfg;
    cfg.sample_rate = static_cast<int>(fmt_.sample_rate);
    cfg.channels    = static_cast<int>(fmt_.channels);
    cfg.bit_width   = static_cast<int>(fmt_.bit_width);

    if (!player_.init(cfg)) {
        fprintf(stderr, "[audio_service] playback session %llu: AudioPlayer init failed\n",
                static_cast<unsigned long long>(session_id_));
        return false;
    }

    playback_thread_ = std::thread(&AudioPlaybackSession::playback_loop, this);
    return true;
}

void AudioPlaybackSession::stop() {
    if (stopped_.exchange(true)) return;
    // Pause the AO channel to unblock any in-progress SendFrame(-1).
    // This is safe to call from another thread — it only toggles channel state
    // without tearing down hardware (unlike player_.stop()).
    player_.pause();
    cv_.notify_all();
    if (playback_thread_.joinable()) playback_thread_.join();
}

bool AudioPlaybackSession::set_volume(int volume) {
    if (stopped_.load()) return false;
    return player_.set_volume(volume);
}

int AudioPlaybackSession::get_volume() const {
    return player_.get_volume();
}

AidenServiceStatus AudioPlaybackSession::push_chunk(const uint8_t* data, size_t len,
                                                    bool is_final) {
    if (stopped_.load()) return AidenServiceStatus::SESSION_NOT_FOUND;

    std::unique_lock<std::mutex> lock(mutex_);
    if (queue_.size() >= kMaxQueueChunks) {
        // Apply back-pressure: wait for the queue to drain a bit.
        cv_.wait_for(lock, std::chrono::milliseconds(200),
                     [this] { return queue_.size() < kMaxQueueChunks / 2 || stopped_.load(); });
        if (stopped_.load()) return AidenServiceStatus::SESSION_NOT_FOUND;
    }

    if (data && len > 0) {
        queue_.push(std::vector<uint8_t>(data, data + len));
    }
    if (is_final) final_received_ = true;
    cv_.notify_one();
    return AidenServiceStatus::OK;
}

void AudioPlaybackSession::wait_until_done() {
    if (playback_thread_.joinable()) playback_thread_.join();
}

void AudioPlaybackSession::playback_loop() {
    size_t last_played_chunk_bytes = 0;
    while (!stopped_.load()) {
        std::vector<uint8_t> chunk;
        bool final_and_drained = false;
        {
            std::unique_lock<std::mutex> lock(mutex_);
            cv_.wait(lock, [this] {
                return !queue_.empty() || final_received_ || stopped_.load();
            });

            if (stopped_.load()) {
                fprintf(stderr,
                        "[audio_service] playback session %llu interrupted before drain completed\n",
                        static_cast<unsigned long long>(session_id_));
                player_.stop();
                return;
            }

            if (!queue_.empty()) {
                chunk = std::move(queue_.front());
                queue_.pop();
                cv_.notify_one();  // wake push_chunk back-pressure waiter
            } else if (final_received_) {
                // Queue drained and final chunk received — no more PCM writes are
                // expected; allow AO buffer to drain before session teardown.
                final_and_drained = true;
            }
        }

        if (final_and_drained) {
            std::unique_lock<std::mutex> lock(mutex_);
            cv_.wait_for(lock,
                         playback_tail_drain_grace(fmt_, last_played_chunk_bytes),
                         [this] { return stopped_.load(); });
            if (stopped_.load()) {
                fprintf(stderr,
                        "[audio_service] playback session %llu interrupted during final drain\n",
                        static_cast<unsigned long long>(session_id_));
                player_.stop();
                return;
            }
            fprintf(stderr,
                    "[audio_service] playback session %llu drained and completed\n",
                    static_cast<unsigned long long>(session_id_));
            break;
        }

        if (!chunk.empty()) {
            if (!player_.play(chunk.data(), static_cast<uint32_t>(chunk.size()))) {
                fprintf(stderr,
                        "[audio_service] playback session %llu: AudioPlayer::play failed (chunk=%zu)\n",
                        static_cast<unsigned long long>(session_id_),
                        chunk.size());
            } else {
                last_played_chunk_bytes = chunk.size();
            }
        }
    }

    if (stopped_.load()) {
        fprintf(stderr,
                "[audio_service] playback session %llu interrupted\n",
                static_cast<unsigned long long>(session_id_));
    }

    // Tear down hardware from the same thread that called SendFrame — safe.
    player_.stop();
}

}  // namespace aiden
