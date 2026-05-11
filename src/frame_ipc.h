#pragma once

#include "frame_service_protocol.h"
#include <string>
#include <vector>

namespace aiden {

struct FrameIpcMessage {
    std::string header_json;
    std::vector<uint8_t> payload;
};

FrameServiceStatus write_frame_message(int fd,
                                       const std::string& header_json,
                                       const std::vector<uint8_t>& payload);
FrameServiceStatus read_frame_message(int fd, FrameIpcMessage* out);

}  // namespace aiden
