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
    if (yuv_data.empty() || !output || width == 0 || height == 0) {
        return false;
    }

    cv::Mat bgr;

    // Use opencv-mobile's hardware-accelerated YUV→BGR conversion
    if (pixel_format == "nv12") {
        // NV12: Y plane + interleaved UV plane (4:2:0)
        cv::Mat yuv_nv12(height * 3 / 2, width, CV_8UC1, const_cast<uint8_t*>(yuv_data.data()));
        cv::cvtColor(yuv_nv12, bgr, cv::COLOR_YUV2BGR_NV12);
    } else if (pixel_format == "nv21") {
        // NV21: Y plane + interleaved VU plane (4:2:0)
        cv::Mat yuv_nv21(height * 3 / 2, width, CV_8UC1, const_cast<uint8_t*>(yuv_data.data()));
        cv::cvtColor(yuv_nv21, bgr, cv::COLOR_YUV2BGR_NV21);
    } else if (pixel_format == "nv16") {
        FrameMetadata meta;
        meta.width = width;
        meta.height = height;
        meta.pixel_format = pixel_format;
        meta.bytes = yuv_data.size();

        std::vector<uint8_t> rgb;
        if (!convert_frame_to_rgb(meta, yuv_data, &rgb)) {
            return false;
        }
        cv::Mat rgb_mat(height, width, CV_8UC3, rgb.data());
        cv::cvtColor(rgb_mat, bgr, cv::COLOR_RGB2BGR);
    } else if (pixel_format == "yuyv") {
        // YUYV: packed YUV 4:2:2
        cv::Mat yuv_yuyv(height, width, CV_8UC2, const_cast<uint8_t*>(yuv_data.data()));
        cv::cvtColor(yuv_yuyv, bgr, cv::COLOR_YUV2BGR_YUYV);
    } else if (pixel_format == "uyvy") {
        // UYVY: packed YUV 4:2:2
        cv::Mat yuv_uyvy(height, width, CV_8UC2, const_cast<uint8_t*>(yuv_data.data()));
        cv::cvtColor(yuv_uyvy, bgr, cv::COLOR_YUV2BGR_UYVY);
    } else {
        // Fallback to software conversion for unsupported formats
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

    // Encode to JPEG using opencv-mobile (automatically uses hardware encoder on RV1106)
    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", bgr, *output, params);
}

}  // namespace aiden
