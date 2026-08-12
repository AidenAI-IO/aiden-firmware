#include "frame_jpeg_encoder.h"
#include "frame_crop_bounds.h"
#include "frame_processing.h"
#include "image_process.h"
#include <opencv2/core.hpp>
#include <opencv2/highgui.hpp>
#include <opencv2/imgproc.hpp>

namespace aiden {

namespace {
void crop_black_bars_bgr(const cv::Mat& bgr, cv::Mat& dst,
                         uint32_t* out_crop_x = nullptr, uint32_t* out_crop_y = nullptr,
                         unsigned char threshold = 15, uint32_t minimal_width = 0,
                         bool crop_black = true);
}

bool encode_frame_to_jpeg_hw(const uint8_t* rgb_data, uint32_t width, uint32_t height,
                              int quality, std::vector<uint8_t>* output,
                              uint32_t* out_width, uint32_t* out_height,
                              uint32_t* out_crop_x, uint32_t* out_crop_y,
                              uint32_t minimal_width, bool crop_black) {
    if (!rgb_data || !output || width == 0 || height == 0) {
        return false;
    }

    cv::Mat rgb(height, width, CV_8UC3, const_cast<uint8_t*>(rgb_data));
    cv::Mat bgr;
    cv::cvtColor(rgb, bgr, cv::COLOR_RGB2BGR);

    cv::Mat cropped;
    uint32_t crop_x = 0;
    uint32_t crop_y = 0;
    crop_black_bars_bgr(bgr, cropped, &crop_x, &crop_y, 15, minimal_width, crop_black);

    if (out_width) *out_width = static_cast<uint32_t>(cropped.cols);
    if (out_height) *out_height = static_cast<uint32_t>(cropped.rows);
    if (out_crop_x) *out_crop_x = crop_x;
    if (out_crop_y) *out_crop_y = crop_y;

    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", cropped, *output, params);
}

namespace {

// Crop left and right black bars directly on a BGR mat (avoids extra RGB↔BGR conversion).
// A column is considered "black border" only if BOTH its mean grayscale
// is <= threshold AND its standard deviation is low (uniform darkness).
// This prevents cropping dark UI elements (e.g. phone status bars) that
// contain small bright pixels.
void crop_black_bars_bgr(const cv::Mat& bgr, cv::Mat& dst,
                         uint32_t* out_crop_x, uint32_t* out_crop_y,
                         unsigned char threshold, uint32_t minimal_width, bool crop_black) {
    if (bgr.empty() || bgr.type() != CV_8UC3) {
        dst = bgr;
        if (out_crop_x) *out_crop_x = 0;
        if (out_crop_y) *out_crop_y = 0;
        return;
    }
    if (!crop_black) {
        bgr.copyTo(dst);
        if (out_crop_x) *out_crop_x = 0;
        if (out_crop_y) *out_crop_y = 0;
        return;
    }
    cv::Mat gray;
    cv::cvtColor(bgr, gray, cv::COLOR_BGR2GRAY);

    const double stddev_limit = 6.0;

    auto is_border_col = [&](int col) -> bool {
        cv::Scalar m, s;
        cv::meanStdDev(gray.col(col), m, s);
        return m[0] <= static_cast<double>(threshold) && s[0] <= stddev_limit;
    };

    int left = 0;
    for (; left < gray.cols; ++left) {
        if (!is_border_col(left)) break;
    }
    int right = gray.cols - 1;
    for (; right >= left; --right) {
        if (!is_border_col(right)) break;
    }

    if (right < left) {
        dst = bgr;
        if (out_crop_x) *out_crop_x = 0;
        if (out_crop_y) *out_crop_y = 0;
        return;
    }

    include_centered_minimal_width(bgr.cols, minimal_width, &left, &right);

    cv::Rect roi(left, 0, right - left + 1, bgr.rows);
    bgr(roi).copyTo(dst);
    if (out_crop_x) *out_crop_x = static_cast<uint32_t>(left);
    if (out_crop_y) *out_crop_y = 0;
}

}  // namespace

bool encode_yuv_to_jpeg_hw(const std::vector<uint8_t>& yuv_data, uint32_t width, uint32_t height,
                            const std::string& pixel_format, int quality,
                            std::vector<uint8_t>* output,
                            uint32_t* out_width, uint32_t* out_height,
                            uint32_t* out_crop_x, uint32_t* out_crop_y,
                            uint32_t minimal_width, bool crop_black) {
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
        return encode_frame_to_jpeg_hw(rgb.data(), width, height, quality, output,
                                       out_width, out_height, out_crop_x, out_crop_y,
                                       minimal_width, crop_black);
    }

    // Crop left and right black bars before encoding.
    cv::Mat cropped;
    uint32_t crop_x = 0;
    uint32_t crop_y = 0;
    crop_black_bars_bgr(bgr, cropped, &crop_x, &crop_y, 15, minimal_width, crop_black);

    if (out_width) *out_width = static_cast<uint32_t>(cropped.cols);
    if (out_height) *out_height = static_cast<uint32_t>(cropped.rows);
    if (out_crop_x) *out_crop_x = crop_x;
    if (out_crop_y) *out_crop_y = crop_y;

    // Encode to JPEG using opencv-mobile (automatically uses hardware encoder on RV1106)
    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", cropped, *output, params);
}

}  // namespace aiden
