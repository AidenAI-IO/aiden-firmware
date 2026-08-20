#include "frame_jpeg_encoder.h"

namespace aiden {

bool encode_frame_to_jpeg_hw(const uint8_t*, uint32_t, uint32_t, int,
                             std::vector<uint8_t>*, uint32_t*, uint32_t*,
                             uint32_t*, uint32_t*, uint32_t, bool, bool) {
    return false;
}

bool encode_yuv_to_jpeg_hw(const std::vector<uint8_t>&, uint32_t, uint32_t,
                           const std::string&, int, std::vector<uint8_t>*,
                           uint32_t*, uint32_t*, uint32_t*, uint32_t*, uint32_t, bool, bool) {
    return false;
}

}  // namespace aiden
