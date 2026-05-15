#include "audio_service_client.h"
#include "uds_client.h"
#include <limits>
#include <stdio.h>

namespace aiden {

// Extra time added to the socket timeout on top of the server-side long-poll
// timeout. Gives the server room to encode and send the response after the
// poll returns, and absorbs scheduling jitter on the embedded target.
static const uint32_t kSocketPaddingMs = 3000;

AudioServiceClient::AudioServiceClient(const char* socket_path)
    : socket_path_(socket_path ? socket_path : "") {}

// -----------------------------------------------------------------------
// Recording
// -----------------------------------------------------------------------

AidenServiceStatus AudioServiceClient::start_recording(const AudioFormat& fmt,
                                                        RecordStartResult* out) {
    std::string extra = audio_format_to_json(fmt);
    std::string req   = audio_request_json("start_recording", extra);

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;

    AidenServiceStatus status = audio_response_status(resp.header_json.c_str());
    if (status == AidenServiceStatus::OK && out) {
        out->session_id = audio_json_u64(resp.header_json.c_str(), "session_id");
    }
    return status;
}

AidenServiceStatus AudioServiceClient::read_record_chunk(uint64_t session_id,
                                                          uint32_t timeout_ms,
                                                          AudioChunkResult* out) {
    std::string extra = "\"session_id\":\"" + std::to_string(session_id) + "\"" +
                        ",\"timeout_ms\":"  + std::to_string(timeout_ms);
    std::string req   = audio_request_json("read_record_chunk", extra);

    // Socket timeout must exceed the server-side long-poll timeout.
    uint32_t socket_timeout_ms = timeout_ms;
    if (socket_timeout_ms > std::numeric_limits<uint32_t>::max() - kSocketPaddingMs) {
        socket_timeout_ms = std::numeric_limits<uint32_t>::max();
    } else {
        socket_timeout_ms += kSocketPaddingMs;
    }

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp,
                                                     socket_timeout_ms);
    if (transport != AidenServiceStatus::OK) return transport;

    AidenServiceStatus status = audio_response_status(resp.header_json.c_str());
    if (status == AidenServiceStatus::OK && out) {
        out->pcm           = std::move(resp.payload);
        out->end_of_stream = audio_json_bool(resp.header_json.c_str(), "end_of_stream");
    }
    return status;
}

AidenServiceStatus AudioServiceClient::stop_recording(uint64_t session_id) {
    std::string extra = "\"session_id\":\"" + std::to_string(session_id) + "\"";
    std::string req   = audio_request_json("stop_recording", extra);

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;
    return audio_response_status(resp.header_json.c_str());
}

// -----------------------------------------------------------------------
// Playback
// -----------------------------------------------------------------------

AidenServiceStatus AudioServiceClient::start_playback(const AudioFormat& fmt,
                                                       PlaybackStartResult* out) {
    std::string extra = audio_format_to_json(fmt);
    std::string req   = audio_request_json("start_playback", extra);

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;

    AidenServiceStatus status = audio_response_status(resp.header_json.c_str());
    if (status == AidenServiceStatus::OK && out) {
        out->session_id = audio_json_u64(resp.header_json.c_str(), "session_id");
    }
    return status;
}

AidenServiceStatus AudioServiceClient::write_play_chunk(uint64_t session_id,
                                                         const uint8_t* data,
                                                         size_t len,
                                                         bool is_final) {
    if (!data && len > 0) {
        fprintf(stderr, "[audio_service_client] write_play_chunk invalid args: data=null len=%zu\n",
                len);
        return AidenServiceStatus::INTERNAL_ERROR;
    }

    std::string extra = "\"session_id\":\"" + std::to_string(session_id) + "\"" +
                        ",\"is_final\":"    + (is_final ? "true" : "false");
    std::string req   = audio_request_json("write_play_chunk", extra);

    std::vector<uint8_t> payload;
    if (data && len > 0) payload.assign(data, data + len);

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, payload, &resp);
    if (transport != AidenServiceStatus::OK) return transport;
    return audio_response_status(resp.header_json.c_str());
}

AidenServiceStatus AudioServiceClient::stop_playback(uint64_t session_id) {
    std::string extra = "\"session_id\":\"" + std::to_string(session_id) + "\"";
    std::string req   = audio_request_json("stop_playback", extra);

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;
    return audio_response_status(resp.header_json.c_str());
}

AidenServiceStatus AudioServiceClient::set_playback_volume(uint32_t volume) {
    std::string extra = "\"volume\":" + std::to_string(volume);
    std::string req   = audio_request_json("set_playback_volume", extra);

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;
    return audio_response_status(resp.header_json.c_str());
}

AidenServiceStatus AudioServiceClient::get_playback_volume(uint32_t* out) {
    std::string req = audio_request_json("get_playback_volume");

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;

    AidenServiceStatus status = audio_response_status(resp.header_json.c_str());
    if (status == AidenServiceStatus::OK && out) {
        *out = audio_json_u32(resp.header_json.c_str(), "volume");
    }
    return status;
}

// -----------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------

AidenServiceStatus AudioServiceClient::health(AudioHealthResult* out) {
    std::string req = audio_request_json("health");

    UdsMessage resp;
    AidenServiceStatus transport = uds_request_once(socket_path_, req, {}, &resp);
    if (transport != AidenServiceStatus::OK) return transport;

    AidenServiceStatus status = audio_response_status(resp.header_json.c_str());
    if (status == AidenServiceStatus::OK && out) {
        out->recording_active  = audio_json_bool(resp.header_json.c_str(), "recording_active");
        out->playback_active   = audio_json_bool(resp.header_json.c_str(), "playback_active");
        out->record_sessions   = audio_json_u32(resp.header_json.c_str(),  "record_sessions");
        out->playback_sessions = audio_json_u32(resp.header_json.c_str(),  "playback_sessions");
    }
    return status;
}

}  // namespace aiden
