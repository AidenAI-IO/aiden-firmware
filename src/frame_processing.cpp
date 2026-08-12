#include "frame_processing.h"
#include "frame_crop_bounds.h"
#include <algorithm>
#include <cmath>
#include <limits>
#include <string.h>

namespace aiden {

static uint8_t clamp_u8(int value) {
    if (value < 0) return 0;
    if (value > 255) return 255;
    return static_cast<uint8_t>(value);
}

static void yuv_to_rgb(uint8_t y, uint8_t u, uint8_t v, uint8_t* rgb) {
    const int c = static_cast<int>(y) - 16;
    const int d = static_cast<int>(u) - 128;
    const int e = static_cast<int>(v) - 128;
    const int c298 = (c < 0 ? 0 : c) * 298;
    rgb[0] = clamp_u8((c298 + 409 * e + 128) >> 8);
    rgb[1] = clamp_u8((c298 - 100 * d - 208 * e + 128) >> 8);
    rgb[2] = clamp_u8((c298 + 516 * d + 128) >> 8);
}

bool convert_frame_to_rgb(const FrameMetadata& metadata,
                          const std::vector<uint8_t>& frame,
                          std::vector<uint8_t>* rgb) {
    if (!rgb || metadata.width == 0 || metadata.height == 0 || metadata.pixel_format.empty()) {
        return false;
    }

    const uint32_t width = metadata.width;
    const uint32_t height = metadata.height;
    const size_t pixels = static_cast<size_t>(width) * height;
    rgb->assign(pixels * 3, 0);

    if (metadata.pixel_format == "uyvy" || metadata.pixel_format == "yuyv") {
        if ((width & 1U) != 0 || frame.size() < pixels * 2) {
            return false;
        }
        size_t src = 0;
        size_t dst = 0;
        while (src + 3 < frame.size() && dst + 5 < rgb->size()) {
            uint8_t y0;
            uint8_t u;
            uint8_t y1;
            uint8_t v;
            if (metadata.pixel_format == "uyvy") {
                u = frame[src++];
                y0 = frame[src++];
                v = frame[src++];
                y1 = frame[src++];
            } else {
                y0 = frame[src++];
                u = frame[src++];
                y1 = frame[src++];
                v = frame[src++];
            }
            yuv_to_rgb(y0, u, v, rgb->data() + dst);
            yuv_to_rgb(y1, u, v, rgb->data() + dst + 3);
            dst += 6;
        }
        return true;
    }

    if (metadata.pixel_format == "nv12" || metadata.pixel_format == "nv16") {
        const bool is_nv12 = metadata.pixel_format == "nv12";
        const size_t y_plane_size = pixels;
        const size_t uv_plane_size = is_nv12 ? pixels / 2 : pixels;
        if (frame.size() < y_plane_size + uv_plane_size) {
            return false;
        }
        const uint8_t* y_plane = frame.data();
        const uint8_t* uv_plane = frame.data() + y_plane_size;
        for (uint32_t y = 0; y < height; ++y) {
            const uint32_t uv_row = is_nv12 ? (y / 2) : y;
            for (uint32_t x = 0; x < width; ++x) {
                const size_t y_index = static_cast<size_t>(y) * width + x;
                const size_t uv_index = static_cast<size_t>(uv_row) * width + (x & ~1U);
                yuv_to_rgb(y_plane[y_index], uv_plane[uv_index], uv_plane[uv_index + 1],
                           rgb->data() + y_index * 3);
            }
        }
        return true;
    }

    return false;
}

static bool is_black_rgb_column(const std::vector<uint8_t>& rgb,
                                uint32_t width,
                                uint32_t height,
                                uint32_t column,
                                double threshold,
                                double stddev_limit) {
    double sum = 0.0;
    double squared_sum = 0.0;
    for (uint32_t y = 0; y < height; ++y) {
        const size_t offset = (static_cast<size_t>(y) * width + column) * 3;
        const double brightness = 0.299 * rgb[offset] +
                                  0.587 * rgb[offset + 1] +
                                  0.114 * rgb[offset + 2];
        sum += brightness;
        squared_sum += brightness * brightness;
    }
    const double mean = sum / height;
    const double variance = std::max(0.0, squared_sum / height - mean * mean);
    return mean <= threshold && std::sqrt(variance) <= stddev_limit;
}

bool crop_frame_horizontal_black_bars(const FrameMetadata& metadata,
                                      const std::vector<uint8_t>& frame,
                                      uint32_t minimal_width,
                                      FrameMetadata* cropped_metadata,
                                      std::vector<uint8_t>* cropped_frame) {
    if (!cropped_metadata || !cropped_frame || metadata.width == 0 || metadata.height == 0 ||
        (metadata.width & 1U) != 0) {
        return false;
    }
    if (metadata.pixel_format == "nv12" && (metadata.height & 1U) != 0) {
        return false;
    }

    std::vector<uint8_t> rgb;
    if (!convert_frame_to_rgb(metadata, frame, &rgb)) {
        return false;
    }

    int left = 0;
    int right = static_cast<int>(metadata.width) - 1;
    while (left <= right && is_black_rgb_column(rgb, metadata.width, metadata.height,
                                                 static_cast<uint32_t>(left), 15.0, 6.0)) {
        ++left;
    }
    while (right >= left && is_black_rgb_column(rgb, metadata.width, metadata.height,
                                                 static_cast<uint32_t>(right), 15.0, 6.0)) {
        --right;
    }
    if (right < left) {
        left = 0;
        right = static_cast<int>(metadata.width) - 1;
    }

    include_centered_minimal_width(static_cast<int>(metadata.width), minimal_width, &left, &right);

    // Every supported raw format shares chroma between adjacent horizontal
    // pixels, so preserve complete pixel pairs in the cropped payload.
    left &= ~1;
    right = std::min(static_cast<int>(metadata.width) - 1, right | 1);
    const uint32_t cropped_width = static_cast<uint32_t>(right - left + 1);
    const uint32_t height = metadata.height;

    cropped_frame->clear();
    FrameMetadata result = metadata;
    result.source_width = metadata.width;
    result.source_height = metadata.height;
    result.crop_x = static_cast<uint32_t>(left);
    result.crop_y = 0;
    result.crop_width = cropped_width;
    result.crop_height = height;
    result.width = cropped_width;
    result.height = height;
    result.planes.clear();

    if (metadata.pixel_format == "uyvy" || metadata.pixel_format == "yuyv") {
        const size_t source_row_bytes = static_cast<size_t>(metadata.width) * 2;
        const size_t cropped_row_bytes = static_cast<size_t>(cropped_width) * 2;
        if (frame.size() < source_row_bytes * height) {
            return false;
        }
        cropped_frame->reserve(cropped_row_bytes * height);
        for (uint32_t y = 0; y < height; ++y) {
            const size_t begin = static_cast<size_t>(y) * source_row_bytes + static_cast<size_t>(left) * 2;
            cropped_frame->insert(cropped_frame->end(), frame.begin() + begin,
                                  frame.begin() + begin + cropped_row_bytes);
        }
        result.stride = cropped_width * 2;
    } else if (metadata.pixel_format == "nv12" || metadata.pixel_format == "nv16") {
        const size_t source_y_bytes = static_cast<size_t>(metadata.width) * height;
        const uint32_t uv_rows = metadata.pixel_format == "nv12" ? height / 2 : height;
        const size_t source_uv_bytes = static_cast<size_t>(metadata.width) * uv_rows;
        if (frame.size() < source_y_bytes + source_uv_bytes) {
            return false;
        }
        cropped_frame->reserve(static_cast<size_t>(cropped_width) * (height + uv_rows));
        for (uint32_t y = 0; y < height; ++y) {
            const size_t begin = static_cast<size_t>(y) * metadata.width + left;
            cropped_frame->insert(cropped_frame->end(), frame.begin() + begin,
                                  frame.begin() + begin + cropped_width);
        }
        for (uint32_t y = 0; y < uv_rows; ++y) {
            const size_t begin = source_y_bytes + static_cast<size_t>(y) * metadata.width + left;
            cropped_frame->insert(cropped_frame->end(), frame.begin() + begin,
                                  frame.begin() + begin + cropped_width);
        }
        FramePlaneMetadata y_plane;
        y_plane.offset = 0;
        y_plane.stride = cropped_width;
        y_plane.bytes = cropped_width * height;
        result.planes.push_back(y_plane);
        FramePlaneMetadata uv_plane;
        uv_plane.offset = y_plane.bytes;
        uv_plane.stride = cropped_width;
        uv_plane.bytes = cropped_width * uv_rows;
        result.planes.push_back(uv_plane);
        result.stride = metadata.pixel_format == "nv16" ? cropped_width * 2 : cropped_width;
    } else {
        return false;
    }

    result.bytes = cropped_frame->size();
    *cropped_metadata = result;
    return true;
}

static void write_u16_le(std::vector<uint8_t>* out, size_t offset, uint16_t value) {
    (*out)[offset] = static_cast<uint8_t>(value & 0xff);
    (*out)[offset + 1] = static_cast<uint8_t>((value >> 8) & 0xff);
}

static void write_u32_le(std::vector<uint8_t>* out, size_t offset, uint32_t value) {
    (*out)[offset] = static_cast<uint8_t>(value & 0xff);
    (*out)[offset + 1] = static_cast<uint8_t>((value >> 8) & 0xff);
    (*out)[offset + 2] = static_cast<uint8_t>((value >> 16) & 0xff);
    (*out)[offset + 3] = static_cast<uint8_t>((value >> 24) & 0xff);
}

static void append_u32_be(std::vector<uint8_t>* out, uint32_t value) {
    out->push_back(static_cast<uint8_t>((value >> 24) & 0xff));
    out->push_back(static_cast<uint8_t>((value >> 16) & 0xff));
    out->push_back(static_cast<uint8_t>((value >> 8) & 0xff));
    out->push_back(static_cast<uint8_t>(value & 0xff));
}

static uint32_t crc32_bytes(const uint8_t* data, size_t size) {
    uint32_t crc = 0xffffffffU;
    for (size_t i = 0; i < size; ++i) {
        crc ^= data[i];
        for (int bit = 0; bit < 8; ++bit) {
            uint32_t mask = static_cast<uint32_t>(-(static_cast<int32_t>(crc & 1U)));
            crc = (crc >> 1) ^ (0xedb88320U & mask);
        }
    }
    return crc ^ 0xffffffffU;
}

static uint32_t adler32_bytes(const uint8_t* data, size_t size) {
    uint32_t a = 1;
    uint32_t b = 0;
    for (size_t i = 0; i < size; ++i) {
        a = (a + data[i]) % 65521U;
        b = (b + a) % 65521U;
    }
    return (b << 16) | a;
}

static void append_png_chunk(std::vector<uint8_t>* png,
                             const char type[4],
                             const std::vector<uint8_t>& data) {
    append_u32_be(png, static_cast<uint32_t>(data.size()));
    size_t type_offset = png->size();
    png->insert(png->end(), type, type + 4);
    png->insert(png->end(), data.begin(), data.end());
    uint32_t crc = crc32_bytes(png->data() + type_offset, 4 + data.size());
    append_u32_be(png, crc);
}

static bool zlib_store_uncompressed(const std::vector<uint8_t>& input,
                                    std::vector<uint8_t>* out) {
    if (!out) {
        return false;
    }
    out->clear();
    out->push_back(0x78);
    out->push_back(0x01);

    size_t offset = 0;
    do {
        size_t remaining = input.size() - offset;
        uint16_t block_len = static_cast<uint16_t>(remaining > 65535U ? 65535U : remaining);
        bool final_block = offset + block_len == input.size();
        out->push_back(final_block ? 0x01 : 0x00);
        out->push_back(static_cast<uint8_t>(block_len & 0xff));
        out->push_back(static_cast<uint8_t>((block_len >> 8) & 0xff));
        uint16_t nlen = static_cast<uint16_t>(~block_len);
        out->push_back(static_cast<uint8_t>(nlen & 0xff));
        out->push_back(static_cast<uint8_t>((nlen >> 8) & 0xff));
        out->insert(out->end(), input.begin() + offset, input.begin() + offset + block_len);
        offset += block_len;
    } while (offset < input.size());

    append_u32_be(out, adler32_bytes(input.empty() ? nullptr : input.data(), input.size()));
    return true;
}

static bool prepare_bmp(uint32_t width,
                        uint32_t height,
                        std::vector<uint8_t>* bmp,
                        uint32_t* row_stride_out) {
    if (!bmp || width == 0 || height == 0) {
        return false;
    }
    const uint32_t row_stride = (width * 3U + 3U) & ~3U;
    const uint64_t image_size = static_cast<uint64_t>(row_stride) * height;
    const uint64_t file_size = 54U + image_size;
    if (height > static_cast<uint32_t>(std::numeric_limits<int32_t>::max()) ||
        width > static_cast<uint32_t>(std::numeric_limits<int32_t>::max()) ||
        file_size > std::numeric_limits<uint32_t>::max()) {
        return false;
    }

    bmp->assign(static_cast<size_t>(file_size), 0);
    (*bmp)[0] = 'B';
    (*bmp)[1] = 'M';
    write_u32_le(bmp, 2, static_cast<uint32_t>(file_size));
    write_u32_le(bmp, 10, 54);
    write_u32_le(bmp, 14, 40);
    write_u32_le(bmp, 18, width);
    write_u32_le(bmp, 22, static_cast<uint32_t>(-static_cast<int32_t>(height)));
    write_u16_le(bmp, 26, 1);
    write_u16_le(bmp, 28, 24);
    write_u32_le(bmp, 34, static_cast<uint32_t>(image_size));
    if (row_stride_out) {
        *row_stride_out = row_stride;
    }
    return true;
}

static void write_bmp_pixel(std::vector<uint8_t>* bmp, size_t offset, const uint8_t* rgb) {
    (*bmp)[offset] = rgb[2];
    (*bmp)[offset + 1] = rgb[1];
    (*bmp)[offset + 2] = rgb[0];
}

bool encode_rgb_to_bmp(const std::vector<uint8_t>& rgb,
                       uint32_t width,
                       uint32_t height,
                       std::vector<uint8_t>* bmp) {
    const size_t pixels = static_cast<size_t>(width) * height;
    if (rgb.size() < pixels * 3) {
        return false;
    }

    uint32_t row_stride = 0;
    if (!prepare_bmp(width, height, bmp, &row_stride)) {
        return false;
    }

    for (uint32_t y = 0; y < height; ++y) {
        const size_t src_row = static_cast<size_t>(y) * width * 3;
        const size_t dst_row = 54 + static_cast<size_t>(y) * row_stride;
        for (uint32_t x = 0; x < width; ++x) {
            const size_t src = src_row + static_cast<size_t>(x) * 3;
            const size_t dst = dst_row + static_cast<size_t>(x) * 3;
            write_bmp_pixel(bmp, dst, rgb.data() + src);
        }
    }
    return true;
}

bool encode_frame_to_bmp(const FrameMetadata& metadata,
                         const std::vector<uint8_t>& frame,
                         std::vector<uint8_t>* bmp) {
    const uint32_t width = metadata.width;
    const uint32_t height = metadata.height;
    const size_t pixels = static_cast<size_t>(width) * height;
    uint32_t row_stride = 0;

    if ((metadata.pixel_format == "uyvy" || metadata.pixel_format == "yuyv") &&
        width > 0 && height > 0 && (width & 1U) == 0 && frame.size() >= pixels * 2 &&
        prepare_bmp(width, height, bmp, &row_stride)) {
        size_t src = 0;
        for (uint32_t y = 0; y < height; ++y) {
            const size_t dst_row = 54 + static_cast<size_t>(y) * row_stride;
            for (uint32_t x = 0; x < width; x += 2) {
                uint8_t y0;
                uint8_t u;
                uint8_t y1;
                uint8_t v;
                if (metadata.pixel_format == "uyvy") {
                    u = frame[src++];
                    y0 = frame[src++];
                    v = frame[src++];
                    y1 = frame[src++];
                } else {
                    y0 = frame[src++];
                    u = frame[src++];
                    y1 = frame[src++];
                    v = frame[src++];
                }
                uint8_t rgb[3];
                yuv_to_rgb(y0, u, v, rgb);
                write_bmp_pixel(bmp, dst_row + static_cast<size_t>(x) * 3, rgb);
                yuv_to_rgb(y1, u, v, rgb);
                write_bmp_pixel(bmp, dst_row + static_cast<size_t>(x + 1) * 3, rgb);
            }
        }
        return true;
    }

    if ((metadata.pixel_format == "nv12" || metadata.pixel_format == "nv16") &&
        width > 0 && height > 0 && prepare_bmp(width, height, bmp, &row_stride)) {
        const bool is_nv12 = metadata.pixel_format == "nv12";
        const size_t y_plane_size = pixels;
        const size_t uv_plane_size = is_nv12 ? pixels / 2 : pixels;
        if (frame.size() >= y_plane_size + uv_plane_size) {
            const uint8_t* y_plane = frame.data();
            const uint8_t* uv_plane = frame.data() + y_plane_size;
            for (uint32_t y = 0; y < height; ++y) {
                const uint32_t uv_row = is_nv12 ? (y / 2) : y;
                const size_t dst_row = 54 + static_cast<size_t>(y) * row_stride;
                for (uint32_t x = 0; x < width; ++x) {
                    const size_t y_index = static_cast<size_t>(y) * width + x;
                    const size_t uv_index = static_cast<size_t>(uv_row) * width + (x & ~1U);
                    uint8_t rgb[3];
                    yuv_to_rgb(y_plane[y_index], uv_plane[uv_index], uv_plane[uv_index + 1], rgb);
                    write_bmp_pixel(bmp, dst_row + static_cast<size_t>(x) * 3, rgb);
                }
            }
            return true;
        }
    }

    std::vector<uint8_t> rgb;
    if (!convert_frame_to_rgb(metadata, frame, &rgb)) {
        return false;
    }
    return encode_rgb_to_bmp(rgb, metadata.width, metadata.height, bmp);
}

bool encode_rgb_to_png(const std::vector<uint8_t>& rgb,
                       uint32_t width,
                       uint32_t height,
                       std::vector<uint8_t>* png) {
    if (!png || width == 0 || height == 0) {
        return false;
    }
    const size_t pixels = static_cast<size_t>(width) * height;
    if (rgb.size() < pixels * 3) {
        return false;
    }

    std::vector<uint8_t> filtered;
    filtered.reserve(static_cast<size_t>(height) * (1 + static_cast<size_t>(width) * 3));
    for (uint32_t y = 0; y < height; ++y) {
        filtered.push_back(0);  // PNG filter type 0 (None)
        const size_t row = static_cast<size_t>(y) * width * 3;
        filtered.insert(filtered.end(), rgb.begin() + row, rgb.begin() + row + static_cast<size_t>(width) * 3);
    }

    std::vector<uint8_t> compressed;
    if (!zlib_store_uncompressed(filtered, &compressed)) {
        return false;
    }

    png->clear();
    const uint8_t signature[] = {137, 80, 78, 71, 13, 10, 26, 10};
    png->insert(png->end(), signature, signature + sizeof(signature));

    std::vector<uint8_t> ihdr;
    append_u32_be(&ihdr, width);
    append_u32_be(&ihdr, height);
    ihdr.push_back(8);  // bit depth
    ihdr.push_back(2);  // truecolor RGB
    ihdr.push_back(0);  // compression method
    ihdr.push_back(0);  // filter method
    ihdr.push_back(0);  // no interlace
    append_png_chunk(png, "IHDR", ihdr);
    append_png_chunk(png, "IDAT", compressed);
    std::vector<uint8_t> empty;
    append_png_chunk(png, "IEND", empty);
    return true;
}

bool encode_frame_to_png(const FrameMetadata& metadata,
                         const std::vector<uint8_t>& frame,
                         std::vector<uint8_t>* png) {
    std::vector<uint8_t> rgb;
    if (!convert_frame_to_rgb(metadata, frame, &rgb)) {
        return false;
    }
    return encode_rgb_to_png(rgb, metadata.width, metadata.height, png);
}

bool scale_rgb_nearest(const std::vector<uint8_t>& rgb,
                       uint32_t src_width,
                       uint32_t src_height,
                       uint32_t dst_width,
                       uint32_t dst_height,
                       std::vector<uint8_t>* scaled) {
    if (!scaled || src_width == 0 || src_height == 0 || dst_width == 0 || dst_height == 0) {
        return false;
    }
    if (rgb.size() < static_cast<size_t>(src_width) * src_height * 3) {
        return false;
    }
    scaled->assign(static_cast<size_t>(dst_width) * dst_height * 3, 0);
    for (uint32_t y = 0; y < dst_height; ++y) {
        uint32_t src_y = static_cast<uint32_t>((static_cast<uint64_t>(y) * src_height) / dst_height);
        for (uint32_t x = 0; x < dst_width; ++x) {
            uint32_t src_x = static_cast<uint32_t>((static_cast<uint64_t>(x) * src_width) / dst_width);
            size_t src = (static_cast<size_t>(src_y) * src_width + src_x) * 3;
            size_t dst = (static_cast<size_t>(y) * dst_width + x) * 3;
            (*scaled)[dst] = rgb[src];
            (*scaled)[dst + 1] = rgb[src + 1];
            (*scaled)[dst + 2] = rgb[src + 2];
        }
    }
    return true;
}

}  // namespace aiden
