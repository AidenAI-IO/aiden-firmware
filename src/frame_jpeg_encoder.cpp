#include "frame_jpeg_encoder.h"
#include "frame_processing.h"
#include "image_process.h"
#include <opencv2/core.hpp>
#include <opencv2/highgui.hpp>
#include <opencv2/imgproc.hpp>

namespace aiden {

bool encode_frame_to_jpeg_hw(const uint8_t* rgb_data, uint32_t width, uint32_t height,
                              int quality, std::vector<uint8_t>* output,
                              uint32_t* out_width, uint32_t* out_height) {
    if (!rgb_data || !output || width == 0 || height == 0) {
        return false;
    }

    cv::Mat rgb(height, width, CV_8UC3, const_cast<uint8_t*>(rgb_data));

    cv::Mat cropped;
    if (aiden_image::crop_black_bars(rgb, cropped) != aiden_image::kSuccess) {
        cropped = rgb;
    }

    if (out_width) *out_width = static_cast<uint32_t>(cropped.cols);
    if (out_height) *out_height = static_cast<uint32_t>(cropped.rows);

    cv::Mat bgr;
    cv::cvtColor(cropped, bgr, cv::COLOR_RGB2BGR);

    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", bgr, *output, params);
}

namespace {

// Crop black bars directly on a BGR mat (avoids extra RGB↔BGR conversion).
// A row/column is considered "black border" only if BOTH its mean grayscale
// is <= threshold AND its standard deviation is low (uniform darkness).
// This prevents cropping dark UI elements (e.g. phone status bars) that
// contain small bright pixels.
void crop_black_bars_bgr(const cv::Mat& bgr, cv::Mat& dst, unsigned char threshold = 15) {
    if (bgr.empty() || bgr.type() != CV_8UC3) {
        dst = bgr;
        return;
    }
    cv::Mat gray;
    cv::cvtColor(bgr, gray, cv::COLOR_BGR2GRAY);

    const double stddev_limit = 6.0;

    auto is_border_row = [&](int row) -> bool {
        cv::Scalar m, s;
        cv::meanStdDev(gray.row(row), m, s);
        return m[0] <= static_cast<double>(threshold) && s[0] <= stddev_limit;
    };

    auto is_border_col = [&](int col) -> bool {
        cv::Scalar m, s;
        cv::meanStdDev(gray.col(col), m, s);
        return m[0] <= static_cast<double>(threshold) && s[0] <= stddev_limit;
    };

    int top = 0;
    for (; top < gray.rows; ++top) {
        if (!is_border_row(top)) break;
    }
    int bottom = gray.rows - 1;
    for (; bottom >= top; --bottom) {
        if (!is_border_row(bottom)) break;
    }
    int left = 0;
    for (; left < gray.cols; ++left) {
        if (!is_border_col(left)) break;
    }
    int right = gray.cols - 1;
    for (; right >= left; --right) {
        if (!is_border_col(right)) break;
    }

    if (right < left || bottom < top) {
        dst = bgr;
        return;
    }
    cv::Rect roi(left, top, right - left + 1, bottom - top + 1);
    bgr(roi).copyTo(dst);
}

}  // namespace

bool encode_yuv_to_jpeg_hw(const std::vector<uint8_t>& yuv_data, uint32_t width, uint32_t height,
                            const std::string& pixel_format, int quality,
                            std::vector<uint8_t>* output,
                            uint32_t* out_width, uint32_t* out_height) {
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
        return encode_frame_to_jpeg_hw(rgb.data(), width, height, quality, output, out_width, out_height);
    }

    // Crop black bars before encoding
    cv::Mat cropped;
    crop_black_bars_bgr(bgr, cropped);

    if (out_width) *out_width = static_cast<uint32_t>(cropped.cols);
    if (out_height) *out_height = static_cast<uint32_t>(cropped.rows);

    // Encode to JPEG using opencv-mobile (automatically uses hardware encoder on RV1106)
    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", cropped, *output, params);
}

}  // namespace aiden
