#pragma once

#include "frame_service_server.h"
#include <atomic>
#include <condition_variable>
#include <mutex>
#include <thread>
#include <vector>

namespace aiden {

struct CapturedFrame {
    FrameMetadata metadata;
    std::vector<uint8_t> data;
};

class FrameCaptureSource {
public:
    virtual ~FrameCaptureSource() {}
    virtual bool open() = 0;
    virtual bool capture(CapturedFrame* frame) = 0;
    virtual void close() = 0;
};

struct FrameCaptureManagerOptions {
    int recovery_initial_backoff_ms = 1000;
    int recovery_max_backoff_ms = 30000;
    int capture_interval_ms = 0;
};

class FrameCaptureManager {
public:
    FrameCaptureManager(FrameCaptureSource* source,
                        FrameServiceServer* server,
                        const FrameCaptureManagerOptions& options);
    ~FrameCaptureManager();

    bool start();
    void stop();
    void request_restart();
    bool is_running() const;

private:
    void run();
    void recover(int* backoff_ms, const char* error, bool count_failure);

    FrameCaptureSource* source_;
    FrameServiceServer* server_;
    FrameCaptureManagerOptions options_;
    std::atomic<bool> running_;
    std::atomic<bool> restart_requested_;
    std::thread thread_;
    std::mutex mutex_;
    std::condition_variable stop_cv_;
};

}  // namespace aiden
