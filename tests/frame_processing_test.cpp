#include "doctest.h"
#include "frame_processing.h"
#include <cstdint>
#include <cstring>
#include <vector>

using aiden::FrameMetadata;
using aiden::convert_frame_to_rgb;
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
