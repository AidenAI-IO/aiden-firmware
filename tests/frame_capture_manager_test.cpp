#include "doctest.h"
#include "frame_capture_manager.h"
#include "frame_service_client.h"
#include "frame_service_server.h"
#include <chrono>
#include <mutex>
#include <string>
#include <thread>
#include <unistd.h>
#include <vector>

using aiden::CapturedFrame;
using aiden::FrameCaptureManager;
using aiden::FrameCaptureManagerOptions;
using aiden::FrameCaptureSource;
using aiden::FrameMetadata;
using aiden::FrameResult;
using aiden::FrameServiceClient;
using aiden::FrameServiceServer;
using aiden::FrameServiceStatus;
using aiden::HealthResult;

namespace {

struct TempSocketPath {
    std::string path;
    TempSocketPath() {
        char tmpl[] = "/tmp/aiden_frame_capture_XXXXXX";
        int fd = ::mkstemp(tmpl);
        REQUIRE(fd >= 0);
        ::close(fd);
        ::unlink(tmpl);
        path = tmpl;
    }
    ~TempSocketPath() { ::unlink(path.c_str()); }
};

FrameMetadata metadata(uint64_t ts) {
    FrameMetadata meta;
    meta.capture_ts_ns = ts;
    meta.width = 1;
    meta.height = 1;
    meta.pixel_format = "uyvy";
    meta.stride = 2;
    return meta;
}

class FakeCaptureSource : public FrameCaptureSource {
public:
    std::vector<CapturedFrame> frames;
    int open_failures_remaining = 0;
    int fail_after = -1;
    bool repeat_last = false;
    int open_count = 0;
    int close_count = 0;
    int capture_delay_ms = 0;

    bool open() override {
        std::lock_guard<std::mutex> lock(mutex_);
        ++open_count;
        if (open_failures_remaining > 0) {
            --open_failures_remaining;
            return false;
        }
        return true;
    }

    bool capture(CapturedFrame* frame) override {
        if (capture_delay_ms > 0) {
            std::this_thread::sleep_for(std::chrono::milliseconds(capture_delay_ms));
        }
        std::lock_guard<std::mutex> lock(mutex_);
        if (fail_after >= 0 && captures_ == fail_after) {
            fail_after = -1;
            return false;
        }
        if (frames.empty()) {
            if (repeat_last && has_last_) {
                *frame = last_;
                ++captures_;
                return true;
            }
            return false;
        }
        *frame = frames.front();
        last_ = *frame;
        has_last_ = true;
        frames.erase(frames.begin());
        ++captures_;
        return true;
    }

    void close() override {
        std::lock_guard<std::mutex> lock(mutex_);
        ++close_count;
    }

    int capture_count() {
        std::lock_guard<std::mutex> lock(mutex_);
        return captures_;
    }

private:
    int captures_ = 0;
    bool has_last_ = false;
    CapturedFrame last_;
    std::mutex mutex_;
};

bool wait_for_latest(FrameServiceClient* client, uint64_t since_seq, FrameResult* out) {
    for (int i = 0; i < 50; ++i) {
        if (client->latest_frame(since_seq, 0, out) == FrameServiceStatus::OK) {
            return true;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    return false;
}

bool wait_for_latest_ts(FrameServiceClient* client, uint64_t capture_ts_ns, FrameResult* out) {
    for (int i = 0; i < 50; ++i) {
        if (client->latest_frame(0, 0, out) == FrameServiceStatus::OK &&
            out->metadata.capture_ts_ns == capture_ts_ns) {
            return true;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    return false;
}

bool wait_for_health_failure(FrameServiceClient* client, HealthResult* out) {
    for (int i = 0; i < 50; ++i) {
        if (client->health(out) == FrameServiceStatus::OK && out->consecutive_failures > 0) {
            return true;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    return false;
}

}

TEST_CASE("FrameCaptureManager captures frames into frame service server") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);

    FakeCaptureSource source;
    source.frames.push_back(CapturedFrame{metadata(10), std::vector<uint8_t>{1, 2}});
    source.frames.push_back(CapturedFrame{metadata(20), std::vector<uint8_t>{3, 4}});
    FrameCaptureManagerOptions options;
    options.recovery_initial_backoff_ms = 1;
    options.recovery_max_backoff_ms = 1;
    FrameCaptureManager manager(&source, &server, options);
    REQUIRE(manager.start());

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    REQUIRE(wait_for_latest_ts(&client, 20, &frame));
    CHECK(frame.metadata.capture_ts_ns == 20);
    CHECK(frame.data == std::vector<uint8_t>{3, 4});

    manager.stop();
    server.stop();
}

TEST_CASE("FrameCaptureManager reopens source after capture failure") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);

    FakeCaptureSource source;
    source.frames.push_back(CapturedFrame{metadata(10), std::vector<uint8_t>{1, 2}});
    source.frames.push_back(CapturedFrame{metadata(30), std::vector<uint8_t>{5, 6}});
    source.fail_after = 1;
    FrameCaptureManagerOptions options;
    options.recovery_initial_backoff_ms = 1;
    options.recovery_max_backoff_ms = 1;
    FrameCaptureManager manager(&source, &server, options);
    REQUIRE(manager.start());

    FrameServiceClient client(socket_path.path.c_str());
    FrameResult frame;
    REQUIRE(wait_for_latest_ts(&client, 30, &frame));
    CHECK(frame.metadata.capture_ts_ns == 30);
    CHECK(source.open_count >= 2);
    CHECK(source.close_count >= 1);

    manager.stop();
    server.stop();
}

TEST_CASE("FrameCaptureManager stop interrupts recovery backoff promptly") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);

    FakeCaptureSource source;
    source.fail_after = 0;
    FrameCaptureManagerOptions options;
    options.recovery_initial_backoff_ms = 1000;
    options.recovery_max_backoff_ms = 1000;
    FrameCaptureManager manager(&source, &server, options);
    REQUIRE(manager.start());
    std::this_thread::sleep_for(std::chrono::milliseconds(50));

    auto start = std::chrono::steady_clock::now();
    manager.stop();
    auto elapsed = std::chrono::duration_cast<std::chrono::milliseconds>(
        std::chrono::steady_clock::now() - start).count();

    CHECK(elapsed < 250);
    server.stop();
}

TEST_CASE("FrameCaptureManager publishes failure and copy latency health metrics") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 4);
    REQUIRE(server.start() == FrameServiceStatus::OK);

    FakeCaptureSource source;
    source.open_failures_remaining = 1;
    source.repeat_last = true;
    source.frames.push_back(CapturedFrame{metadata(40), std::vector<uint8_t>{7, 8}});
    FrameCaptureManagerOptions options;
    options.recovery_initial_backoff_ms = 100;
    options.recovery_max_backoff_ms = 100;
    FrameCaptureManager manager(&source, &server, options);
    REQUIRE(manager.start());

    FrameServiceClient client(socket_path.path.c_str());
    HealthResult health;
    REQUIRE(wait_for_health_failure(&client, &health));
    CHECK(health.consecutive_failures == 1);
    CHECK(health.last_error == "open failed");
    CHECK(health.last_recovery_ts > 0);

    FrameResult frame;
    REQUIRE(wait_for_latest_ts(&client, 40, &frame));
    manager.stop();
    REQUIRE(client.health(&health) == FrameServiceStatus::OK);
    CHECK(health.consecutive_failures == 0);
    CHECK(health.last_error == "open failed");
    CHECK(health.last_recovery_ts > 0);
    CHECK(health.avg_capture_copy_latency_ms > 0.0);

    server.stop();
}

TEST_CASE("FrameCaptureManager drains captures while limiting publish cadence") {
    TempSocketPath socket_path;
    FrameServiceServer server(socket_path.path.c_str(), 8);
    REQUIRE(server.start() == FrameServiceStatus::OK);

    FakeCaptureSource source;
    source.repeat_last = true;
    source.capture_delay_ms = 10;
    source.frames.push_back(CapturedFrame{metadata(50), std::vector<uint8_t>{1, 2}});
    FrameCaptureManagerOptions options;
    options.recovery_initial_backoff_ms = 1;
    options.recovery_max_backoff_ms = 1;
    options.capture_interval_ms = 100;
    FrameCaptureManager manager(&source, &server, options);
    REQUIRE(manager.start());

    std::this_thread::sleep_for(std::chrono::milliseconds(260));
    manager.stop();

    FrameServiceClient client(socket_path.path.c_str());
    HealthResult health;
    REQUIRE(client.health(&health) == FrameServiceStatus::OK);
    CHECK(source.capture_count() >= 10);
    CHECK(health.latest_seq >= 2);
    CHECK(health.latest_seq <= 4);
    CHECK(health.ring_buffer_used == health.latest_seq);
    server.stop();
}
