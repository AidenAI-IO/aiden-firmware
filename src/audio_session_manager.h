#pragma once

#include "audio_playback_session.h"
#include "audio_record_session.h"
#include "audio_service_protocol.h"
#include <cstdint>
#include <memory>
#include <mutex>
#include <unordered_map>

namespace aiden {

// Owns all active record and playback sessions.
// Thread-safe; all public methods may be called from any thread.
class AudioSessionManager {
public:
    // Sessions idle longer than this are reaped automatically.
    static const uint32_t kSessionTimeoutMs = 30000;

    AudioSessionManager();
    ~AudioSessionManager();

    // --- Recording ---

    // Create and start a new record session.
    AidenServiceStatus start_recording(const AudioFormat& fmt,
                                       RecordStartResult* out);

    // Pop the next PCM chunk from a record session (long-poll).
    AidenServiceStatus read_record_chunk(uint64_t session_id,
                                         uint32_t timeout_ms,
                                         AudioChunkResult* out);

    // Stop and remove a record session.
    AidenServiceStatus stop_recording(uint64_t session_id);

    // --- Playback ---

    // Create and start a new playback session.
    AidenServiceStatus start_playback(const AudioFormat& fmt,
                                      PlaybackStartResult* out);

    // Push a PCM chunk into a playback session.
    AidenServiceStatus write_play_chunk(uint64_t session_id,
                                        const uint8_t* data,
                                        size_t len,
                                        bool is_final);

    // Stop and remove a playback session.
    AidenServiceStatus stop_playback(uint64_t session_id);

    // --- Health ---

    void fill_health(AudioHealthResult* out) const;

private:
    uint64_t next_session_id();

    mutable std::mutex mutex_;
    uint64_t next_id_;
    std::unordered_map<uint64_t, std::unique_ptr<AudioRecordSession>>   record_sessions_;
    std::unordered_map<uint64_t, std::unique_ptr<AudioPlaybackSession>> playback_sessions_;
};

}  // namespace aiden
