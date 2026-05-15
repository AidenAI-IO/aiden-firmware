#pragma once

#include <cstdint>
#include <vector>
#include <string>

namespace aiden {

/**
 * Encode frame data to JPEG using opencv-mobile hardware encoder.
 * @param rgb_data RGB888 data (width * height * 3 bytes)
 * @param width Image width
 * @param height Image height
 * @param quality JPEG quality (1-100)
 * @param output Output JPEG data
 * @return true on success
 */
bool encode_frame_to_jpeg_hw(const uint8_t* rgb_data, uint32_t width, uint32_t height,
                              int quality, std::vector<uint8_t>* output);

/**
 * Convert YUV frame to RGB and encode to JPEG using hardware encoder.
 * @param yuv_data YUV data
 * @param width Image width
 * @param height Image height
 * @param pixel_format "nv12", "nv16", "yuyv", or "uyvy"
 * @param quality JPEG quality (1-100)
 * @param output Output JPEG data
 * @return true on success
 */
bool encode_yuv_to_jpeg_hw(const std::vector<uint8_t>& yuv_data, uint32_t width, uint32_t height,
                            const std::string& pixel_format, int quality,
                            std::vector<uint8_t>* output);

}  // namespace aiden
