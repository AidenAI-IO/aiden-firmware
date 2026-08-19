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
                         bool crop_black = true, bool crop_by_aspect = false);
}

bool encode_frame_to_jpeg_hw(const uint8_t* rgb_data, uint32_t width, uint32_t height,
                              int quality, std::vector<uint8_t>* output,
                              uint32_t* out_width, uint32_t* out_height,
                              uint32_t* out_crop_x, uint32_t* out_crop_y,
                              uint32_t minimal_width, bool crop_black,
                              bool crop_by_aspect) {
    if (!rgb_data || !output || width == 0 || height == 0) {
        return false;
    }

    cv::Mat rgb(height, width, CV_8UC3, const_cast<uint8_t*>(rgb_data));
    cv::Mat bgr;
    cv::cvtColor(rgb, bgr, cv::COLOR_RGB2BGR);

    cv::Mat cropped;
    uint32_t crop_x = 0;
    uint32_t crop_y = 0;
    crop_black_bars_bgr(bgr, cropped, &crop_x, &crop_y, 15, minimal_width,
                        crop_black, crop_by_aspect);

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
                         unsigned char threshold, uint32_t minimal_width,
                         bool crop_black, bool crop_by_aspect) {
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
    if (crop_by_aspect && minimal_width > 0) {
        const int cropped_width = static_cast<int>(
            std::min<uint32_t>(minimal_width, static_cast<uint32_t>(bgr.cols)));
        const int left = (bgr.cols - cropped_width) / 2;
        dst = bgr(cv::Rect(left, 0, cropped_width, bgr.rows));
        if (out_crop_x) *out_crop_x = static_cast<uint32_t>(left);
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

    // Black bars are assumed to be contiguous from each edge; locate each
    // transition with logarithmic probes after finding an active seed.
    const int last_column = gray.cols - 1;
    int left = 0;
    int right = last_column;
    const bool first_is_border = is_border_col(0);
    const bool last_is_border = is_border_col(last_column);
    int active_seed = -1;
    if (!first_is_border) {
        active_seed = 0;
    } else if (!last_is_border) {
        active_seed = last_column;
    } else {
        const int probe_positions[] = {
            gray.cols / 2,
            gray.cols / 4,
            (gray.cols * 3) / 4,
            gray.cols / 8,
            (gray.cols * 3) / 8,
            (gray.cols * 5) / 8,
            (gray.cols * 7) / 8,
        };
        for (size_t i = 0; i < sizeof(probe_positions) / sizeof(probe_positions[0]); ++i) {
            const int probe = std::min(last_column, std::max(0, probe_positions[i]));
            if (!is_border_col(probe)) {
                active_seed = probe;
                break;
            }
        }
    }

    if (active_seed < 0) {
        left = 0;
        right = last_column;
    } else if (first_is_border) {
        int border = 0;
        int active = active_seed;
        while (active - border > 1) {
            const int middle = border + (active - border) / 2;
            if (is_border_col(middle)) {
                border = middle;
            } else {
                active = middle;
            }
        }
        left = active;
    }
    if (active_seed >= 0 && last_is_border) {
        int active = active_seed;
        int border = last_column;
        while (border - active > 1) {
            const int middle = active + (border - active) / 2;
            if (is_border_col(middle)) {
                border = middle;
            } else {
                active = middle;
            }
        }
        right = active;
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
                            uint32_t minimal_width, bool crop_black,
                            bool crop_by_aspect) {
    if (yuv_data.empty() || !output || width == 0 || height == 0) {
        return false;
    }

    const std::vector<uint8_t>* conversion_data = &yuv_data;
    std::vector<uint8_t> centered_yuv;
    uint32_t conversion_width = width;
    uint32_t source_crop_x = 0;
    bool raw_pre_cropped = false;
    if (crop_black && crop_by_aspect && minimal_width > 0 &&
        (pixel_format == "uyvy" || pixel_format == "yuyv" ||
         pixel_format == "nv12" || pixel_format == "nv16")) {
        FrameMetadata source_metadata;
        source_metadata.width = width;
        source_metadata.height = height;
        source_metadata.pixel_format = pixel_format;
        source_metadata.stride = pixel_format == "uyvy" || pixel_format == "yuyv"
                                     ? width * 2U
                                     : width;
        source_metadata.bytes = yuv_data.size();
        FrameMetadata cropped_metadata;
        if (!crop_frame_horizontal_center(source_metadata, yuv_data, minimal_width,
                                          &cropped_metadata, &centered_yuv)) {
            return false;
        }
        conversion_data = &centered_yuv;
        conversion_width = cropped_metadata.width;
        source_crop_x = cropped_metadata.crop_x;
        raw_pre_cropped = true;
    }

    cv::Mat bgr;

    // Use opencv-mobile's hardware-accelerated YUV→BGR conversion
    if (pixel_format == "nv12") {
        // NV12: Y plane + interleaved UV plane (4:2:0)
        cv::Mat yuv_nv12(height * 3 / 2, conversion_width, CV_8UC1,
                         const_cast<uint8_t*>(conversion_data->data()));
        cv::cvtColor(yuv_nv12, bgr, cv::COLOR_YUV2BGR_NV12);
    } else if (pixel_format == "nv21") {
        // NV21: Y plane + interleaved VU plane (4:2:0)
        cv::Mat yuv_nv21(height * 3 / 2, conversion_width, CV_8UC1,
                         const_cast<uint8_t*>(conversion_data->data()));
        cv::cvtColor(yuv_nv21, bgr, cv::COLOR_YUV2BGR_NV21);
    } else if (pixel_format == "nv16") {
        FrameMetadata meta;
        meta.width = conversion_width;
        meta.height = height;
        meta.pixel_format = pixel_format;
        meta.bytes = conversion_data->size();

        std::vector<uint8_t> rgb;
        if (!convert_frame_to_rgb(meta, *conversion_data, &rgb)) {
            return false;
        }
        cv::Mat rgb_mat(height, conversion_width, CV_8UC3, rgb.data());
        cv::cvtColor(rgb_mat, bgr, cv::COLOR_RGB2BGR);
    } else if (pixel_format == "yuyv") {
        // YUYV: packed YUV 4:2:2
        cv::Mat yuv_yuyv(height, conversion_width, CV_8UC2,
                         const_cast<uint8_t*>(conversion_data->data()));
        cv::cvtColor(yuv_yuyv, bgr, cv::COLOR_YUV2BGR_YUYV);
    } else if (pixel_format == "uyvy") {
        // UYVY: packed YUV 4:2:2
        cv::Mat yuv_uyvy(height, conversion_width, CV_8UC2,
                         const_cast<uint8_t*>(conversion_data->data()));
        cv::cvtColor(yuv_uyvy, bgr, cv::COLOR_YUV2BGR_UYVY);
    } else {
        // Fallback to software conversion for unsupported formats
        FrameMetadata meta;
        meta.width = conversion_width;
        meta.height = height;
        meta.pixel_format = pixel_format;
        meta.bytes = conversion_data->size();

        std::vector<uint8_t> rgb;
        if (!convert_frame_to_rgb(meta, *conversion_data, &rgb)) {
            return false;
        }
        return encode_frame_to_jpeg_hw(rgb.data(), conversion_width, height, quality, output,
                                       out_width, out_height, out_crop_x, out_crop_y,
                                       minimal_width, crop_black, crop_by_aspect);
    }

    // Crop left and right black bars before encoding.
    cv::Mat cropped;
    uint32_t crop_x = 0;
    uint32_t crop_y = 0;
    crop_black_bars_bgr(bgr, cropped, &crop_x, &crop_y, 15, minimal_width,
                        crop_black && !raw_pre_cropped,
                        crop_by_aspect && !raw_pre_cropped);

    if (out_width) *out_width = static_cast<uint32_t>(cropped.cols);
    if (out_height) *out_height = static_cast<uint32_t>(cropped.rows);
    if (out_crop_x) *out_crop_x = source_crop_x + crop_x;
    if (out_crop_y) *out_crop_y = crop_y;

    // Encode to JPEG using opencv-mobile (automatically uses hardware encoder on RV1106)
    std::vector<int> params;
    params.push_back(cv::IMWRITE_JPEG_QUALITY);
    params.push_back(quality);

    return cv::imencode(".jpg", cropped, *output, params);
}

}  // namespace aiden
