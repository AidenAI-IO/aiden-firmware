#pragma once

#include <cstdint>
#include <vector>
#include <string>

namespace aiden {

/**
 * Encode frame data to JPEG using opencv-mobile hardware encoder.
 * Automatically crops black bars from the left and right edges before encoding.
 * @param rgb_data RGB888 data (width * height * 3 bytes)
 * @param width Image width
 * @param height Image height
 * @param quality JPEG quality (1-100)
 * @param output Output JPEG data
 * @param out_width Actual encoded width after cropping (may be null)
 * @param out_height Actual encoded height after cropping (may be null)
 * @param minimal_width Minimum output width after cropping. Values larger than
 *        the source width are clamped to the source width.
 * @param crop_black Whether to crop uniformly dark columns at the edges.
 * @param crop_by_aspect Whether minimal_width is an exact centered crop width
 *        derived from known screen geometry. This skips black-bar scanning.
 * @return true on success
 */
bool encode_frame_to_jpeg_hw(const uint8_t* rgb_data, uint32_t width, uint32_t height,
                              int quality, std::vector<uint8_t>* output,
                              uint32_t* out_width = nullptr, uint32_t* out_height = nullptr,
                              uint32_t* out_crop_x = nullptr, uint32_t* out_crop_y = nullptr,
                              uint32_t minimal_width = 0, bool crop_black = true,
                              bool crop_by_aspect = false);

/**
 * Convert YUV frame to RGB and encode to JPEG using hardware encoder.
 * Automatically crops black bars from the left and right edges before encoding.
 * @param yuv_data YUV data
 * @param width Image width
 * @param height Image height
 * @param pixel_format "nv12", "nv16", "yuyv", or "uyvy"
 * @param quality JPEG quality (1-100)
 * @param output Output JPEG data
 * @param out_width Actual encoded width after cropping (may be null)
 * @param out_height Actual encoded height after cropping (may be null)
 * @param minimal_width Minimum output width after cropping. Values larger than
 *        the source width are clamped to the source width.
 * @param crop_black Whether to crop uniformly dark columns at the edges.
 * @param crop_by_aspect Whether minimal_width is an exact centered crop width
 *        derived from known screen geometry. Packed 4:2:2 data is cropped
 *        before color conversion when enabled.
 * @return true on success
 */
bool encode_yuv_to_jpeg_hw(const std::vector<uint8_t>& yuv_data, uint32_t width, uint32_t height,
                            const std::string& pixel_format, int quality,
                            std::vector<uint8_t>* output,
                            uint32_t* out_width = nullptr, uint32_t* out_height = nullptr,
                            uint32_t* out_crop_x = nullptr, uint32_t* out_crop_y = nullptr,
                            uint32_t minimal_width = 0, bool crop_black = true,
                            bool crop_by_aspect = false);

// Encode a full packed UYVY/YUYV frame while applying the crop rectangle in
// a persistent hardware JPEG channel. Returns false when RK VENC is unavailable
// so the caller can use the existing OpenCV path. The crop rectangle must be
// even-aligned for packed 4:2:2.
bool encode_yuv_to_jpeg_hw_with_crop(const std::vector<uint8_t>& yuv_data,
                                     uint32_t width, uint32_t height,
                                     const std::string& pixel_format, int quality,
                                     uint32_t crop_x, uint32_t crop_y,
                                     uint32_t crop_width, uint32_t crop_height,
                                     std::vector<uint8_t>* output);

}  // namespace aiden
