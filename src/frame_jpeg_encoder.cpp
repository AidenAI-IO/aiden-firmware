#include "frame_jpeg_encoder.h"
#include "aiden_log.h"
#include "frame_crop_bounds.h"
#include "frame_processing.h"
#include "image_process.h"
#include "rk_mpi_mb.h"
#include "rk_mpi_sys.h"
#include "rk_mpi_venc.h"
#include <algorithm>
#include <cstring>
#include <mutex>
#include <opencv2/core.hpp>
#include <opencv2/highgui.hpp>
#include <opencv2/imgproc.hpp>

namespace aiden {

namespace {
void crop_black_bars_bgr(const cv::Mat& bgr, cv::Mat& dst,
                         uint32_t* out_crop_x = nullptr, uint32_t* out_crop_y = nullptr,
                         unsigned char threshold = 15, uint32_t minimal_width = 0,
                         bool crop_black = true, bool crop_by_aspect = false);

class PersistentPacked422JpegEncoder {
public:
    PersistentPacked422JpegEncoder()
        : sys_initialized_(false),
          channel_created_(false),
          receiving_(false),
          channel_(0),
          input_pool_(MB_INVALID_POOLID),
          input_block_(RK_NULL),
          width_(0),
          height_(0),
          pixel_format_(RK_FMT_BUTT),
          input_bytes_(0),
          quality_(0),
          crop_x_(0),
          crop_y_(0),
          crop_width_(0),
          crop_height_(0),
          crop_configured_(false) {}

    ~PersistentPacked422JpegEncoder() {
        std::lock_guard<std::mutex> lock(mutex_);
        reset_resources();
        if (sys_initialized_) {
            RK_MPI_SYS_Exit();
        }
    }

    bool encode(const std::vector<uint8_t>& yuv_data,
                uint32_t width, uint32_t height,
                PIXEL_FORMAT_E pixel_format, int quality,
                uint32_t crop_x, uint32_t crop_y,
                uint32_t crop_width, uint32_t crop_height,
                std::vector<uint8_t>* output) {
        std::lock_guard<std::mutex> lock(mutex_);
        if (!output || width == 0 || height == 0 || (width & 1U) != 0 ||
            crop_width == 0 || crop_height == 0 || crop_x + crop_width > width ||
            crop_y + crop_height > height || (crop_x & 1U) != 0 ||
            (crop_width & 1U) != 0 || (crop_height & 1U) != 0) {
            return false;
        }
        const size_t required_bytes = static_cast<size_t>(width) * height * 2U;
        if (yuv_data.size() < required_bytes) {
            return false;
        }
        quality = std::max(1, std::min(99, quality));

        if (!ensure_initialized(width, height, pixel_format, required_bytes, quality)) {
            return false;
        }
        if (!set_quality(quality) ||
            !set_crop(width, height, crop_x, crop_y, crop_width, crop_height) ||
            !ensure_receiving()) {
            reset_resources();
            return false;
        }

        void* input = RK_MPI_MB_Handle2VirAddr(input_block_);
        if (!input) {
            reset_resources();
            return false;
        }
        memcpy(input, yuv_data.data(), required_bytes);
        if (RK_MPI_SYS_MmzFlushCache(input_block_, RK_FALSE) != RK_SUCCESS) {
            reset_resources();
            return false;
        }

        VIDEO_FRAME_INFO_S frame;
        memset(&frame, 0, sizeof(frame));
        frame.stVFrame.pMbBlk = input_block_;
        frame.stVFrame.u32Width = width;
        frame.stVFrame.u32Height = height;
        frame.stVFrame.u32VirWidth = width;
        frame.stVFrame.u32VirHeight = height;
        frame.stVFrame.enPixelFormat = pixel_format;
        frame.stVFrame.enCompressMode = COMPRESS_MODE_NONE;

        RK_S32 ret = RK_MPI_VENC_SendFrame(channel_, &frame, 2000);
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_send_failed", "ret=%#x", ret);
            reset_resources();
            return false;
        }

        VENC_PACK_S pack;
        memset(&pack, 0, sizeof(pack));
        VENC_STREAM_S stream;
        memset(&stream, 0, sizeof(stream));
        stream.pstPack = &pack;
        stream.u32PackCount = 1;
        ret = RK_MPI_VENC_GetStream(channel_, &stream, 2000);
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_get_stream_failed", "ret=%#x", ret);
            reset_resources();
            return false;
        }

        bool ok = stream.u32PackCount == 1 && pack.pMbBlk != RK_NULL && pack.u32Len > 0;
        if (ok) {
            RK_MPI_SYS_MmzFlushCache(pack.pMbBlk, RK_TRUE);
            const uint8_t* encoded = static_cast<const uint8_t*>(
                RK_MPI_MB_Handle2VirAddr(pack.pMbBlk));
            if (encoded) {
                // Rockit's JPEG adapter returns an MB handle already positioned
                // at the pack start; its reference samples ignore u32Offset.
                output->assign(encoded, encoded + pack.u32Len);
            } else {
                ok = false;
            }
        }
        const RK_S32 release_ret = RK_MPI_VENC_ReleaseStream(channel_, &stream);
        if (release_ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_release_stream_failed", "ret=%#x", release_ret);
            ok = false;
        }
        if (!ok) {
            output->clear();
            reset_resources();
        }
        return ok;
    }

private:
    bool ensure_sys_initialized() {
        if (sys_initialized_) {
            return true;
        }
        const RK_S32 ret = RK_MPI_SYS_Init();
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "mpi_sys_init_failed", "ret=%#x", ret);
            return false;
        }
        sys_initialized_ = true;
        return true;
    }

    bool ensure_initialized(uint32_t width, uint32_t height,
                            PIXEL_FORMAT_E pixel_format,
                            size_t input_bytes, int quality) {
        if (channel_created_ && width_ == width && height_ == height &&
            pixel_format_ == pixel_format && input_bytes_ == input_bytes) {
            return true;
        }
        reset_resources();
        if (!ensure_sys_initialized()) {
            return false;
        }

        MB_POOL_CONFIG_S pool_config;
        memset(&pool_config, 0, sizeof(pool_config));
        pool_config.u64MBSize = input_bytes;
        pool_config.u32MBCnt = 1;
        pool_config.enAllocType = MB_ALLOC_TYPE_DMA;
        pool_config.enRemapMode = MB_REMAP_MODE_CACHED;
        pool_config.bPreAlloc = RK_TRUE;
        input_pool_ = RK_MPI_MB_CreatePool(&pool_config);
        if (input_pool_ == MB_INVALID_POOLID) {
            AIDEN_LOG_WARN("jpeg", "venc_input_pool_create_failed",
                           "bytes=%zu", input_bytes);
            return false;
        }
        input_block_ = RK_MPI_MB_GetMB(input_pool_, input_bytes, RK_TRUE);
        if (input_block_ == RK_NULL) {
            AIDEN_LOG_WARN("jpeg", "venc_input_buffer_get_failed",
                           "bytes=%zu", input_bytes);
            reset_resources();
            return false;
        }

        VENC_CHN_ATTR_S attr;
        memset(&attr, 0, sizeof(attr));
        attr.stVencAttr.enType = RK_VIDEO_ID_JPEG;
        attr.stVencAttr.enPixelFormat = pixel_format;
        attr.stVencAttr.u32MaxPicWidth = width;
        attr.stVencAttr.u32MaxPicHeight = height;
        attr.stVencAttr.u32PicWidth = width;
        attr.stVencAttr.u32PicHeight = height;
        attr.stVencAttr.u32VirWidth = width;
        attr.stVencAttr.u32VirHeight = height;
        attr.stVencAttr.u32StreamBufCnt = 2;
        attr.stVencAttr.u32BufSize = static_cast<RK_U32>(input_bytes);
        attr.stVencAttr.enMirror = MIRROR_NONE;
        attr.stVencAttr.stAttrJpege.bSupportDCF = RK_FALSE;
        attr.stVencAttr.stAttrJpege.stMPFCfg.u8LargeThumbNailNum = 0;
        attr.stVencAttr.stAttrJpege.enReceiveMode = VENC_PIC_RECEIVE_SINGLE;

        RK_S32 ret = RK_MPI_VENC_CreateChn(channel_, &attr);
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_channel_create_failed", "ret=%#x", ret);
            reset_resources();
            return false;
        }
        channel_created_ = true;
        width_ = width;
        height_ = height;
        pixel_format_ = pixel_format;
        input_bytes_ = input_bytes;
        quality_ = 0;
        crop_configured_ = false;

        if (!set_quality(quality)) {
            reset_resources();
            return false;
        }
        return true;
    }

    bool ensure_receiving() {
        if (receiving_) {
            return true;
        }
        VENC_RECV_PIC_PARAM_S receive;
        memset(&receive, 0, sizeof(receive));
        receive.s32RecvPicNum = -1;
        const RK_S32 ret = RK_MPI_VENC_StartRecvFrame(channel_, &receive);
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_start_failed", "ret=%#x", ret);
            return false;
        }
        receiving_ = true;
        AIDEN_LOG_INFO("jpeg", "venc_fast_path_ready",
                       "width=%u height=%u pixel_format=%d", width_, height_,
                       static_cast<int>(pixel_format_));
        return true;
    }

    bool set_quality(int quality) {
        if (quality_ == quality) {
            return true;
        }
        VENC_JPEG_PARAM_S jpeg;
        memset(&jpeg, 0, sizeof(jpeg));
        jpeg.u32Qfactor = static_cast<RK_U32>(quality);
        const RK_S32 ret = RK_MPI_VENC_SetJpegParam(channel_, &jpeg);
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_quality_failed", "ret=%#x quality=%d", ret, quality);
            return false;
        }
        quality_ = quality;
        return true;
    }

    bool set_crop(uint32_t width, uint32_t height,
                  uint32_t crop_x, uint32_t crop_y,
                  uint32_t crop_width, uint32_t crop_height) {
        if (crop_configured_ && crop_x_ == crop_x && crop_y_ == crop_y &&
            crop_width_ == crop_width && crop_height_ == crop_height) {
            return true;
        }
        if (receiving_) {
            const RK_S32 stop_ret = RK_MPI_VENC_StopRecvFrame(channel_);
            if (stop_ret != RK_SUCCESS) {
                AIDEN_LOG_WARN("jpeg", "venc_stop_for_crop_failed", "ret=%#x", stop_ret);
                return false;
            }
            receiving_ = false;
        }
        VENC_CHN_PARAM_S parameter;
        memset(&parameter, 0, sizeof(parameter));
        const RK_S32 get_ret = RK_MPI_VENC_GetChnParam(channel_, &parameter);
        if (get_ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_get_param_failed", "ret=%#x", get_ret);
            return false;
        }
        // RV1106 keeps the previous crop offset when a running JPEG channel is
        // changed from VENC_CROP_ONLY to VENC_CROP_NONE. Express the full-frame
        // case as an explicit zero-offset crop so toggling crop_black cannot
        // shift the image or read past the end of the source frame.
        parameter.stCropCfg.enCropType = VENC_CROP_ONLY;
        parameter.stCropCfg.stCropRect.s32X = static_cast<RK_S32>(crop_x);
        parameter.stCropCfg.stCropRect.s32Y = static_cast<RK_S32>(crop_y);
        parameter.stCropCfg.stCropRect.u32Width = crop_width;
        parameter.stCropCfg.stCropRect.u32Height = crop_height;
        const RK_S32 ret = RK_MPI_VENC_SetChnParam(channel_, &parameter);
        if (ret != RK_SUCCESS) {
            AIDEN_LOG_WARN("jpeg", "venc_crop_failed",
                           "ret=%#x x=%u y=%u width=%u height=%u",
                           ret, crop_x, crop_y, crop_width, crop_height);
            return false;
        }
        crop_x_ = crop_x;
        crop_y_ = crop_y;
        crop_width_ = crop_width;
        crop_height_ = crop_height;
        crop_configured_ = true;
        return true;
    }

    void reset_resources() {
        if (channel_created_) {
            if (receiving_) {
                RK_MPI_VENC_StopRecvFrame(channel_);
            }
            RK_MPI_VENC_DestroyChn(channel_);
            channel_created_ = false;
            receiving_ = false;
        }
        if (input_block_ != RK_NULL) {
            RK_MPI_MB_ReleaseMB(input_block_);
            input_block_ = RK_NULL;
        }
        if (input_pool_ != MB_INVALID_POOLID) {
            RK_MPI_MB_DestroyPool(input_pool_);
            input_pool_ = MB_INVALID_POOLID;
        }
        width_ = 0;
        height_ = 0;
        input_bytes_ = 0;
        quality_ = 0;
        crop_configured_ = false;
    }

    std::mutex mutex_;
    bool sys_initialized_;
    bool channel_created_;
    bool receiving_;
    VENC_CHN channel_;
    MB_POOL input_pool_;
    MB_BLK input_block_;
    uint32_t width_;
    uint32_t height_;
    PIXEL_FORMAT_E pixel_format_;
    size_t input_bytes_;
    int quality_;
    uint32_t crop_x_;
    uint32_t crop_y_;
    uint32_t crop_width_;
    uint32_t crop_height_;
    bool crop_configured_;
};
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
        dst = bgr;
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

bool encode_yuv_to_jpeg_hw_with_crop(const std::vector<uint8_t>& yuv_data,
                                     uint32_t width, uint32_t height,
                                     const std::string& pixel_format, int quality,
                                     uint32_t crop_x, uint32_t crop_y,
                                     uint32_t crop_width, uint32_t crop_height,
                                     std::vector<uint8_t>* output) {
    PIXEL_FORMAT_E rk_pixel_format = RK_FMT_BUTT;
    if (pixel_format == "uyvy") {
        rk_pixel_format = RK_FMT_YUV422_UYVY;
    } else if (pixel_format == "yuyv") {
        rk_pixel_format = RK_FMT_YUV422_YUYV;
    } else {
        return false;
    }

    // Keep the channel and DMA input buffer alive for the process lifetime.
    // This also avoids static destruction ordering with the camera's separate
    // RK_MPI_SYS lifetime management during service shutdown.
    static PersistentPacked422JpegEncoder* encoder =
        new PersistentPacked422JpegEncoder();
    return encoder->encode(yuv_data, width, height, rk_pixel_format, quality,
                           crop_x, crop_y, crop_width, crop_height, output);
}

}  // namespace aiden
