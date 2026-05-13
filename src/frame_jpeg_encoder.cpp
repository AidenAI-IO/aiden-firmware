#include "frame_jpeg_encoder.h"
#include "frame_processing.h"
#include <opencv2/core.hpp>
#include <opencv2/highgui.hpp>
#include <opencv2/imgproc.hpp>

namespace aiden {

bool encode_frame_to_jpeg_hw(const uint8_t* rgb_data, uint32_t width, uint32_t height,
                              int quality, std::vector<uint8_t>* output) {
    if (!rgb_data || !output || width == 0 || height == 0) {
        return false;
    }

    // Create cv::Mat from RGB data (opencv-mobile uses BGR internally)
    cv::Mat rgb(height, width, CV_8UC3, const_cast<uint8_t*>(rgb_data));
    cv::Mat bgr;
    cv::cvtColor(rgb, bgr, cv::COLOR_RGB2BGR);

    // Encode to JPEG using opencv-mobile (automatically uses hardware encoder on RV1106)
    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", bgr, *output, params);
}

bool encode_yuv_to_jpeg_hw(const std::vector<uint8_t>& yuv_data, uint32_t width, uint32_t height,
                            const std::string& pixel_format, int quality,
                            std::vector<uint8_t>* output) {
    if (yuv_data.empty() || !output) {
        return false;
    }

    // Convert YUV to RGB first
    FrameMetadata meta;
    meta.width = width;
    meta.height = height;
    meta.pixel_format = pixel_format;
    meta.bytes = yuv_data.size();

    std::vector<uint8_t> rgb;
    if (!convert_frame_to_rgb(meta, yuv_data, &rgb)) {
        return false;
    }

    return encode_frame_to_jpeg_hw(rgb.data(), width, height, quality, output);
}

}  // namespace aiden
