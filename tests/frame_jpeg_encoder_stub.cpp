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

bool encode_yuv_to_jpeg_hw_with_crop(const std::vector<uint8_t>& yuv_data,
                                     uint32_t width, uint32_t height,
                                     const std::string& pixel_format, int,
                                     uint32_t crop_x, uint32_t crop_y,
                                     uint32_t crop_width, uint32_t crop_height,
                                     std::vector<uint8_t>* output) {
    if (!output || (pixel_format != "uyvy" && pixel_format != "yuyv") ||
        yuv_data.size() < static_cast<size_t>(width) * height * 2U ||
        crop_width == 0 || crop_height == 0 || crop_x + crop_width > width ||
        crop_y + crop_height > height || (width & 1U) != 0 ||
        (crop_x & 1U) != 0 || (crop_width & 1U) != 0 ||
        (crop_height & 1U) != 0) {
        return false;
    }
    *output = std::vector<uint8_t>{0xff, 0xd8, 0xff, 0xd9};
    return true;
}

void clear_yuv_jpeg_hw_unavailable() {}

}  // namespace aiden
