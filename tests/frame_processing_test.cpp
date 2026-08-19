#include "doctest.h"
#include "frame_processing.h"
#include <cstdint>
#include <cstring>
#include <vector>

using aiden::FrameMetadata;
using aiden::convert_frame_to_rgb;
using aiden::crop_frame_black_bars;
using aiden::crop_frame_center;
using aiden::crop_frame_center_aspect_auto;
using aiden::crop_frame_horizontal_center;
using aiden::crop_frame_horizontal_black_bars;
using aiden::encode_frame_to_bmp;
using aiden::encode_frame_to_png;
using aiden::encode_rgb_to_png;
using aiden::encode_rgb_to_bmp;
using aiden::scale_rgb_nearest;

namespace {

uint32_t le32_at(const std::vector<uint8_t>& bytes, size_t off) {
    return static_cast<uint32_t>(bytes[off]) |
           (static_cast<uint32_t>(bytes[off + 1]) << 8) |
           (static_cast<uint32_t>(bytes[off + 2]) << 16) |
           (static_cast<uint32_t>(bytes[off + 3]) << 24);
}

uint32_t be32_at(const std::vector<uint8_t>& bytes, size_t off) {
    return (static_cast<uint32_t>(bytes[off]) << 24) |
           (static_cast<uint32_t>(bytes[off + 1]) << 16) |
           (static_cast<uint32_t>(bytes[off + 2]) << 8) |
           static_cast<uint32_t>(bytes[off + 3]);
}

FrameMetadata frame_meta(uint32_t width, uint32_t height, const char* format) {
    FrameMetadata metadata;
    metadata.width = width;
    metadata.height = height;
    metadata.pixel_format = format;
    metadata.stride = width * 2;
    metadata.bytes = metadata.stride * height;
    return metadata;
}

std::vector<uint8_t> uyvy_from_luma(uint32_t width, uint32_t height,
                                    const std::vector<uint8_t>& luma) {
    std::vector<uint8_t> uyvy;
    uyvy.reserve(static_cast<size_t>(width) * height * 2U);
    for (uint32_t y = 0; y < height; ++y) {
        for (uint32_t x = 0; x < width; x += 2) {
            uyvy.push_back(128);
            uyvy.push_back(luma[static_cast<size_t>(y) * width + x]);
            uyvy.push_back(128);
            uyvy.push_back(luma[static_cast<size_t>(y) * width + x + 1]);
        }
    }
    return uyvy;
}

}

TEST_CASE("convert_frame_to_rgb converts UYVY packed pixels") {
    FrameMetadata metadata = frame_meta(2, 1, "uyvy");
    std::vector<uint8_t> uyvy = {128, 235, 128, 16};
    std::vector<uint8_t> rgb;

    REQUIRE(convert_frame_to_rgb(metadata, uyvy, &rgb));
    REQUIRE(rgb.size() == 6);
    CHECK(rgb[0] == 255);
    CHECK(rgb[1] == 255);
    CHECK(rgb[2] == 255);
    CHECK(rgb[3] == 0);
    CHECK(rgb[4] == 0);
    CHECK(rgb[5] == 0);
}

TEST_CASE("crop raw UYVY frame removes only horizontal black pixel pairs") {
    FrameMetadata metadata = frame_meta(8, 1, "uyvy");
    std::vector<uint8_t> uyvy = {
        128, 16, 128, 16,
        128, 235, 128, 235,
        128, 235, 128, 235,
        128, 16, 128, 16,
    };
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_horizontal_black_bars(metadata, uyvy, 0, &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 4);
    CHECK(cropped_metadata.height == 1);
    CHECK(cropped_metadata.source_width == 8);
    CHECK(cropped_metadata.crop_x == 2);
    CHECK(cropped_metadata.crop_width == 4);
    CHECK(cropped_metadata.stride == 8);
    CHECK(cropped_metadata.bytes == 8);
    CHECK(cropped == std::vector<uint8_t>{128, 235, 128, 235, 128, 235, 128, 235});
}

TEST_CASE("crop raw UYVY frame keeps an all-black frame intact") {
    FrameMetadata metadata = frame_meta(8, 1, "uyvy");
    std::vector<uint8_t> uyvy(16, 128);
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_horizontal_black_bars(metadata, uyvy, 0,
                                              &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 8);
    CHECK(cropped_metadata.crop_x == 0);
    CHECK(cropped_metadata.crop_width == 8);
    CHECK(cropped == uyvy);
}

TEST_CASE("crop raw UYVY frame honors centered minimal width") {
    FrameMetadata metadata = frame_meta(8, 1, "uyvy");
    std::vector<uint8_t> uyvy = {
        128, 16, 128, 16,
        128, 235, 128, 235,
        128, 16, 128, 16,
        128, 16, 128, 16,
    };
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_horizontal_black_bars(metadata, uyvy, 4, &cropped_metadata, &cropped));
    CHECK(cropped_metadata.crop_x == 2);
    CHECK(cropped_metadata.crop_width == 4);
    CHECK(cropped.size() == 8);
}

TEST_CASE("crop raw UYVY frame uses centered width without scanning") {
    FrameMetadata metadata = frame_meta(10, 2, "uyvy");
    std::vector<uint8_t> uyvy(40);
    for (size_t i = 0; i < uyvy.size(); ++i) {
        uyvy[i] = static_cast<uint8_t>(i);
    }
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_horizontal_center(metadata, uyvy, 5,
                                          &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 6);
    CHECK(cropped_metadata.height == 2);
    CHECK(cropped_metadata.source_width == 10);
    CHECK(cropped_metadata.source_height == 2);
    CHECK(cropped_metadata.crop_x == 2);
    CHECK(cropped_metadata.crop_width == 6);
    CHECK(cropped_metadata.crop_height == 2);
    CHECK(cropped_metadata.stride == 12);
    CHECK(cropped_metadata.bytes == 24);
    CHECK(cropped == std::vector<uint8_t>{
        4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
        24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35,
    });
}

TEST_CASE("crop raw UYVY frame uses centered height without scanning") {
    FrameMetadata metadata = frame_meta(4, 6, "uyvy");
    std::vector<uint8_t> uyvy(48);
    for (size_t i = 0; i < uyvy.size(); ++i) {
        uyvy[i] = static_cast<uint8_t>(i);
    }
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_center(metadata, uyvy, 4, 2,
                              &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 4);
    CHECK(cropped_metadata.height == 2);
    CHECK(cropped_metadata.source_width == 4);
    CHECK(cropped_metadata.source_height == 6);
    CHECK(cropped_metadata.crop_x == 0);
    CHECK(cropped_metadata.crop_y == 2);
    CHECK(cropped_metadata.crop_width == 4);
    CHECK(cropped_metadata.crop_height == 2);
    CHECK(cropped_metadata.bytes == 16);
    CHECK(cropped == std::vector<uint8_t>{
        16, 17, 18, 19, 20, 21, 22, 23,
        24, 25, 26, 27, 28, 29, 30, 31,
    });
}

TEST_CASE("crop raw UYVY frame detects top and bottom black bars") {
    FrameMetadata metadata = frame_meta(4, 6, "uyvy");
    std::vector<uint8_t> uyvy;
    for (int y = 0; y < 6; ++y) {
        const uint8_t luma = y >= 2 && y <= 3 ? 235 : 16;
        for (int x = 0; x < 2; ++x) {
            uyvy.push_back(128);
            uyvy.push_back(luma);
            uyvy.push_back(128);
            uyvy.push_back(luma);
        }
    }
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_black_bars(metadata, uyvy, &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 4);
    CHECK(cropped_metadata.height == 2);
    CHECK(cropped_metadata.crop_x == 0);
    CHECK(cropped_metadata.crop_y == 2);
    CHECK(cropped_metadata.crop_width == 4);
    CHECK(cropped_metadata.crop_height == 2);
    CHECK(cropped.size() == 16);
}

TEST_CASE("known screen ratio derives landscape orientation from current frame") {
    FrameMetadata metadata = frame_meta(12, 8, "uyvy");
    std::vector<uint8_t> luma(96, 16);
    for (uint32_t y = 1; y < 7; ++y) {
        for (uint32_t x = 0; x < 12; ++x) {
            luma[static_cast<size_t>(y) * 12 + x] =
                static_cast<uint8_t>((x + y) % 2 == 0 ? 35 : 20);
        }
    }
    const std::vector<uint8_t> uyvy = uyvy_from_luma(12, 8, luma);
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_center_aspect_auto(metadata, uyvy, 2, 4,
                                          &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 12);
    CHECK(cropped_metadata.height == 6);
    CHECK(cropped_metadata.crop_x == 0);
    CHECK(cropped_metadata.crop_y == 1);
}

TEST_CASE("known screen ratio retains one row to make landscape crop even") {
    FrameMetadata metadata = frame_meta(16, 12, "uyvy");
    std::vector<uint8_t> luma(192, 16);
    for (uint32_t y = 1; y < 11; ++y) {
        for (uint32_t x = 0; x < 16; ++x) {
            luma[static_cast<size_t>(y) * 16 + x] =
                static_cast<uint8_t>((x + y) % 2 == 0 ? 235 : 180);
        }
    }
    const std::vector<uint8_t> uyvy = uyvy_from_luma(16, 12, luma);
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_center_aspect_auto(metadata, uyvy, 4, 7,
                                          &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 16);
    CHECK(cropped_metadata.height == 10);
    CHECK(cropped_metadata.crop_x == 0);
    CHECK(cropped_metadata.crop_y == 1);
}

TEST_CASE("known screen ratio derives portrait orientation from current frame") {
    FrameMetadata metadata = frame_meta(12, 8, "uyvy");
    std::vector<uint8_t> luma(96, 16);
    for (uint32_t y = 0; y < 8; ++y) {
        for (uint32_t x = 4; x < 8; ++x) {
            luma[static_cast<size_t>(y) * 12 + x] =
                static_cast<uint8_t>((x + y) % 2 == 0 ? 235 : 180);
        }
    }
    const std::vector<uint8_t> uyvy = uyvy_from_luma(12, 8, luma);
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_center_aspect_auto(metadata, uyvy, 4, 2,
                                          &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 4);
    CHECK(cropped_metadata.height == 8);
    CHECK(cropped_metadata.crop_x == 4);
    CHECK(cropped_metadata.crop_y == 0);
}

TEST_CASE("known screen ratio retains one column to make portrait crop even") {
    FrameMetadata metadata = frame_meta(16, 12, "uyvy");
    std::vector<uint8_t> luma(192, 16);
    for (uint32_t y = 0; y < 12; ++y) {
        for (uint32_t x = 4; x < 12; ++x) {
            luma[static_cast<size_t>(y) * 16 + x] =
                static_cast<uint8_t>((x + y) % 2 == 0 ? 235 : 180);
        }
    }
    const std::vector<uint8_t> uyvy = uyvy_from_luma(16, 12, luma);
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_center_aspect_auto(metadata, uyvy, 4, 7,
                                          &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 8);
    CHECK(cropped_metadata.height == 12);
    CHECK(cropped_metadata.crop_x == 4);
    CHECK(cropped_metadata.crop_y == 0);
}

TEST_CASE("crop raw NV12 frame rebuilds plane metadata") {
    FrameMetadata metadata;
    metadata.width = 8;
    metadata.height = 2;
    metadata.pixel_format = "nv12";
    metadata.stride = 8;
    metadata.bytes = 24;
    std::vector<uint8_t> nv12 = {
        16, 16, 235, 235, 235, 235, 16, 16,
        16, 16, 235, 235, 235, 235, 16, 16,
        128, 128, 128, 128, 128, 128, 128, 128,
    };
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    REQUIRE(crop_frame_horizontal_black_bars(metadata, nv12, 0, &cropped_metadata, &cropped));
    CHECK(cropped_metadata.width == 4);
    CHECK(cropped_metadata.crop_x == 2);
    CHECK(cropped_metadata.stride == 4);
    CHECK(cropped_metadata.bytes == 12);
    REQUIRE(cropped_metadata.planes.size() == 2);
    CHECK(cropped_metadata.planes[0].offset == 0);
    CHECK(cropped_metadata.planes[0].stride == 4);
    CHECK(cropped_metadata.planes[0].bytes == 8);
    CHECK(cropped_metadata.planes[1].offset == 8);
    CHECK(cropped_metadata.planes[1].stride == 4);
    CHECK(cropped_metadata.planes[1].bytes == 4);
    CHECK(cropped == std::vector<uint8_t>{
        235, 235, 235, 235,
        235, 235, 235, 235,
        128, 128, 128, 128,
    });
}

TEST_CASE("crop raw NV12 frame rejects odd height before conversion") {
    FrameMetadata metadata;
    metadata.width = 2;
    metadata.height = 3;
    metadata.pixel_format = "nv12";
    metadata.stride = 2;
    metadata.bytes = 9;
    std::vector<uint8_t> nv12(9, 128);
    FrameMetadata cropped_metadata;
    std::vector<uint8_t> cropped;

    CHECK_FALSE(crop_frame_horizontal_black_bars(metadata, nv12, 0, &cropped_metadata, &cropped));
}

TEST_CASE("encode_rgb_to_bmp writes top-down 24-bit BMP") {
    std::vector<uint8_t> rgb = {255, 0, 0, 0, 255, 0};
    std::vector<uint8_t> bmp;

    REQUIRE(encode_rgb_to_bmp(rgb, 2, 1, &bmp));
    REQUIRE(bmp.size() == 62);
    CHECK(std::memcmp(bmp.data(), "BM", 2) == 0);
    CHECK(le32_at(bmp, 2) == 62);
    CHECK(le32_at(bmp, 10) == 54);
    CHECK(le32_at(bmp, 18) == 2);
    CHECK(le32_at(bmp, 22) == static_cast<uint32_t>(-1));
    CHECK(bmp[54] == 0);
    CHECK(bmp[55] == 0);
    CHECK(bmp[56] == 255);
    CHECK(bmp[57] == 0);
    CHECK(bmp[58] == 255);
    CHECK(bmp[59] == 0);
}

TEST_CASE("encode_frame_to_bmp converts raw frame to BMP pixels") {
    FrameMetadata metadata = frame_meta(2, 1, "uyvy");
    std::vector<uint8_t> uyvy = {128, 235, 128, 16};
    std::vector<uint8_t> bmp;

    REQUIRE(encode_frame_to_bmp(metadata, uyvy, &bmp));
    CHECK(std::memcmp(bmp.data(), "BM", 2) == 0);
    CHECK(le32_at(bmp, 18) == 2);
    CHECK(le32_at(bmp, 22) == static_cast<uint32_t>(-1));
    CHECK(bmp[54] == 255);
    CHECK(bmp[55] == 255);
    CHECK(bmp[56] == 255);
    CHECK(bmp[57] == 0);
    CHECK(bmp[58] == 0);
    CHECK(bmp[59] == 0);
}

TEST_CASE("scale_rgb_nearest scales RGB image by nearest neighbor") {
    std::vector<uint8_t> rgb = {
        255, 0, 0,     0, 255, 0,
        0, 0, 255,     255, 255, 255,
    };
    std::vector<uint8_t> scaled;

    REQUIRE(scale_rgb_nearest(rgb, 2, 2, 4, 4, &scaled));
    REQUIRE(scaled.size() == 4 * 4 * 3);
    CHECK(std::vector<uint8_t>(scaled.begin(), scaled.begin() + 3) == std::vector<uint8_t>{255, 0, 0});
    CHECK(std::vector<uint8_t>(scaled.begin() + 9, scaled.begin() + 12) == std::vector<uint8_t>{0, 255, 0});
    CHECK(std::vector<uint8_t>(scaled.begin() + 36, scaled.begin() + 39) == std::vector<uint8_t>{0, 0, 255});
    CHECK(std::vector<uint8_t>(scaled.begin() + 45, scaled.begin() + 48) == std::vector<uint8_t>{255, 255, 255});
}

TEST_CASE("encode_rgb_to_png writes a valid RGB PNG envelope") {
    std::vector<uint8_t> rgb = {255, 0, 0, 0, 255, 0};
    std::vector<uint8_t> png;

    REQUIRE(encode_rgb_to_png(rgb, 2, 1, &png));
    REQUIRE(png.size() > 33);
    const uint8_t signature[] = {137, 80, 78, 71, 13, 10, 26, 10};
    CHECK(std::memcmp(png.data(), signature, sizeof(signature)) == 0);
    CHECK(be32_at(png, 8) == 13);
    CHECK(std::memcmp(png.data() + 12, "IHDR", 4) == 0);
    CHECK(be32_at(png, 16) == 2);
    CHECK(be32_at(png, 20) == 1);
    CHECK(png[24] == 8);
    CHECK(png[25] == 2);
    CHECK(std::memcmp(png.data() + png.size() - 12, "\0\0\0\0IEND", 8) == 0);
}

TEST_CASE("encode_frame_to_png converts raw frame to PNG") {
    FrameMetadata metadata = frame_meta(2, 1, "uyvy");
    std::vector<uint8_t> uyvy = {128, 235, 128, 16};
    std::vector<uint8_t> png;

    REQUIRE(encode_frame_to_png(metadata, uyvy, &png));
    REQUIRE(png.size() > 33);
    CHECK(std::memcmp(png.data(), "\x89PNG\r\n\x1a\n", 8) == 0);
    CHECK(be32_at(png, 16) == 2);
    CHECK(be32_at(png, 20) == 1);
}
