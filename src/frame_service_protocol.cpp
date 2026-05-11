#include "frame_service_protocol.h"
#include <string.h>

namespace aiden {

const char* frame_service_status_to_string(FrameServiceStatus status) {
    switch (status) {
        case FrameServiceStatus::OK:
            return "OK";
        case FrameServiceStatus::NO_NEW_FRAME:
            return "NO_NEW_FRAME";
        case FrameServiceStatus::FRAME_NOT_FOUND:
            return "FRAME_NOT_FOUND";
        case FrameServiceStatus::SERVICE_RECOVERING:
            return "SERVICE_RECOVERING";
        case FrameServiceStatus::TIMEOUT:
            return "TIMEOUT";
        case FrameServiceStatus::TRANSPORT_ERROR:
            return "TRANSPORT_ERROR";
        case FrameServiceStatus::INTERNAL_ERROR:
            return "INTERNAL_ERROR";
    }
    return "INTERNAL_ERROR";
}

bool frame_service_status_from_string(const char* text, FrameServiceStatus* out) {
    if (!text || !out) {
        return false;
    }

    struct Mapping {
        const char* text;
        FrameServiceStatus status;
    };

    static const Mapping mappings[] = {
        {"OK", FrameServiceStatus::OK},
        {"NO_NEW_FRAME", FrameServiceStatus::NO_NEW_FRAME},
        {"FRAME_NOT_FOUND", FrameServiceStatus::FRAME_NOT_FOUND},
        {"SERVICE_RECOVERING", FrameServiceStatus::SERVICE_RECOVERING},
        {"TIMEOUT", FrameServiceStatus::TIMEOUT},
        {"TRANSPORT_ERROR", FrameServiceStatus::TRANSPORT_ERROR},
        {"INTERNAL_ERROR", FrameServiceStatus::INTERNAL_ERROR},
    };

    for (size_t i = 0; i < sizeof(mappings) / sizeof(mappings[0]); ++i) {
        if (strcmp(text, mappings[i].text) == 0) {
            *out = mappings[i].status;
            return true;
        }
    }
    return false;
}

static void write_le32(std::vector<uint8_t>& out, uint32_t value) {
    out.push_back(static_cast<uint8_t>(value & 0xff));
    out.push_back(static_cast<uint8_t>((value >> 8) & 0xff));
    out.push_back(static_cast<uint8_t>((value >> 16) & 0xff));
    out.push_back(static_cast<uint8_t>((value >> 24) & 0xff));
}

static void write_le64(std::vector<uint8_t>& out, uint64_t value) {
    for (int i = 0; i < 8; ++i) {
        out.push_back(static_cast<uint8_t>((value >> (i * 8)) & 0xff));
    }
}

static uint32_t read_le32(const uint8_t* data) {
    return static_cast<uint32_t>(data[0]) |
           (static_cast<uint32_t>(data[1]) << 8) |
           (static_cast<uint32_t>(data[2]) << 16) |
           (static_cast<uint32_t>(data[3]) << 24);
}

static uint64_t read_le64(const uint8_t* data) {
    uint64_t value = 0;
    for (int i = 0; i < 8; ++i) {
        value |= static_cast<uint64_t>(data[i]) << (i * 8);
    }
    return value;
}

std::vector<uint8_t> encode_frame_wire_prefix(uint32_t header_len, uint64_t payload_len) {
    std::vector<uint8_t> out;
    out.reserve(12);
    write_le32(out, header_len);
    write_le64(out, payload_len);
    return out;
}

bool decode_frame_wire_prefix(const uint8_t* data, size_t len, FrameWirePrefix* out) {
    if (!data || !out || len < 12) {
        return false;
    }
    out->header_len = read_le32(data);
    out->payload_len = read_le64(data + 4);
    return true;
}

}  // namespace aiden
