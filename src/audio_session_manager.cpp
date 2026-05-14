#include "audio_session_manager.h"
#include <stdio.h>

namespace aiden {

AudioSessionManager::AudioSessionManager() : next_id_(1) {}

AudioSessionManager::~AudioSessionManager() {
    // Sessions own their threads; destructors join them.
    std::lock_guard<std::mutex> lock(mutex_);
    record_sessions_.clear();
    playback_sessions_.clear();
}

uint64_t AudioSessionManager::next_session_id() {
    return next_id_++;
}

// -----------------------------------------------------------------------
// Recording
// -----------------------------------------------------------------------

AidenServiceStatus AudioSessionManager::start_recording(const AudioFormat& fmt,
                                                         RecordStartResult* out) {
    std::lock_guard<std::mutex> lock(mutex_);
    uint64_t id = next_session_id();
    auto session = std::unique_ptr<AudioRecordSession>(new AudioRecordSession(id, fmt));
    if (!session->start()) {
        return AidenServiceStatus::INTERNAL_ERROR;
    }
    out->session_id = id;
    record_sessions_[id] = std::move(session);
    fprintf(stderr, "[audio_service] record session %llu started\n",
            static_cast<unsigned long long>(id));
    return AidenServiceStatus::OK;
}

AidenServiceStatus AudioSessionManager::read_record_chunk(uint64_t session_id,
                                                           uint32_t timeout_ms,
                                                           AudioChunkResult* out) {
    AudioRecordSession* session = nullptr;
    {
        std::lock_guard<std::mutex> lock(mutex_);
        auto it = record_sessions_.find(session_id);
        if (it == record_sessions_.end()) {
            return AidenServiceStatus::SESSION_NOT_FOUND;
        }
        session = it->second.get();
    }
    // pop_chunk blocks without holding the manager lock.
    AidenServiceStatus status = session->pop_chunk(timeout_ms, out);

    // Clean up stopped sessions lazily.
    if (out->end_of_stream) {
        std::lock_guard<std::mutex> lock(mutex_);
        record_sessions_.erase(session_id);
    }
    return status;
}

AidenServiceStatus AudioSessionManager::stop_recording(uint64_t session_id) {
    std::unique_ptr<AudioRecordSession> session;
    {
        std::lock_guard<std::mutex> lock(mutex_);
        auto it = record_sessions_.find(session_id);
        if (it == record_sessions_.end()) {
            return AidenServiceStatus::SESSION_NOT_FOUND;
        }
        session = std::move(it->second);
        record_sessions_.erase(it);
    }
    session->stop();
    fprintf(stderr, "[audio_service] record session %llu stopped\n",
            static_cast<unsigned long long>(session_id));
    return AidenServiceStatus::OK;
}

// -----------------------------------------------------------------------
// Playback
// -----------------------------------------------------------------------

AidenServiceStatus AudioSessionManager::start_playback(const AudioFormat& fmt,
                                                        PlaybackStartResult* out) {
    std::lock_guard<std::mutex> lock(mutex_);
    uint64_t id = next_session_id();
    auto session = std::unique_ptr<AudioPlaybackSession>(new AudioPlaybackSession(id, fmt));
    if (!session->start()) {
        return AidenServiceStatus::INTERNAL_ERROR;
    }
    out->session_id = id;
    playback_sessions_[id] = std::move(session);
    fprintf(stderr, "[audio_service] playback session %llu started\n",
            static_cast<unsigned long long>(id));
    return AidenServiceStatus::OK;
}

AidenServiceStatus AudioSessionManager::write_play_chunk(uint64_t session_id,
                                                          const uint8_t* data,
                                                          size_t len,
                                                          bool is_final) {
    AudioPlaybackSession* session = nullptr;
    {
        std::lock_guard<std::mutex> lock(mutex_);
        auto it = playback_sessions_.find(session_id);
        if (it == playback_sessions_.end()) {
            return AidenServiceStatus::SESSION_NOT_FOUND;
        }
        session = it->second.get();
    }
    AidenServiceStatus status = session->push_chunk(data, len, is_final);

    if (is_final && status == AidenServiceStatus::OK) {
        // Remove from map so no further writes are accepted, then detach the
        // playback thread so it drains remaining audio without blocking the
        // RPC handler. The session object is kept alive by a shared_ptr on the
        // detached thread itself via a lambda capture.
        std::unique_ptr<AudioPlaybackSession> owned;
        {
            std::lock_guard<std::mutex> lock(mutex_);
            auto it = playback_sessions_.find(session_id);
            if (it != playback_sessions_.end()) {
                owned = std::move(it->second);
                playback_sessions_.erase(it);
            }
        }
        if (owned) {
            // Transfer ownership to a detached thread so the destructor (which
            // joins the playback thread) does not run on the RPC handler thread.
            AudioPlaybackSession* raw = owned.release();
            std::thread([raw]() {
                raw->wait_until_done();
                delete raw;
            }).detach();
        }
    }
    return status;
}

AidenServiceStatus AudioSessionManager::stop_playback(uint64_t session_id) {
    std::unique_ptr<AudioPlaybackSession> session;
    {
        std::lock_guard<std::mutex> lock(mutex_);
        auto it = playback_sessions_.find(session_id);
        if (it == playback_sessions_.end()) {
            return AidenServiceStatus::SESSION_NOT_FOUND;
        }
        session = std::move(it->second);
        playback_sessions_.erase(it);
    }
    session->stop();
    fprintf(stderr, "[audio_service] playback session %llu stopped\n",
            static_cast<unsigned long long>(session_id));
    return AidenServiceStatus::OK;
}

// -----------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------

void AudioSessionManager::fill_health(AudioHealthResult* out) const {
    std::lock_guard<std::mutex> lock(mutex_);
    out->record_sessions   = static_cast<uint32_t>(record_sessions_.size());
    out->playback_sessions = static_cast<uint32_t>(playback_sessions_.size());
    out->recording_active  = !record_sessions_.empty();
    out->playback_active   = !playback_sessions_.empty();
}

}  // namespace aiden
