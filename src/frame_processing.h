#pragma once

#include "frame_service_protocol.h"
#include <cstdint>
#include <vector>

namespace aiden {

bool convert_frame_to_rgb(const FrameMetadata& metadata,
                          const std::vector<uint8_t>& frame,
                          std::vector<uint8_t>* rgb);

bool encode_rgb_to_bmp(const std::vector<uint8_t>& rgb,
                       uint32_t width,
                       uint32_t height,
                       std::vector<uint8_t>* bmp);

bool encode_frame_to_bmp(const FrameMetadata& metadata,
                         const std::vector<uint8_t>& frame,
                         std::vector<uint8_t>* bmp);

bool encode_rgb_to_png(const std::vector<uint8_t>& rgb,
                       uint32_t width,
                       uint32_t height,
                       std::vector<uint8_t>* png);

bool encode_frame_to_png(const FrameMetadata& metadata,
                         const std::vector<uint8_t>& frame,
                         std::vector<uint8_t>* png);

bool scale_rgb_nearest(const std::vector<uint8_t>& rgb,
                       uint32_t src_width,
                       uint32_t src_height,
                       uint32_t dst_width,
                       uint32_t dst_height,
                       std::vector<uint8_t>* scaled);

}  // namespace aiden
