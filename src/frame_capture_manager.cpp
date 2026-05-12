#include "frame_capture_manager.h"
#include <chrono>
#include <thread>

namespace aiden {

FrameCaptureManager::FrameCaptureManager(FrameCaptureSource* source,
                                         FrameServiceServer* server,
                                         const FrameCaptureManagerOptions& options)
    : source_(source), server_(server), options_(options), running_(false), restart_requested_(false) {}

FrameCaptureManager::~FrameCaptureManager() {
    stop();
}

bool FrameCaptureManager::start() {
    if (!source_ || !server_ || running_) {
        return false;
    }
    running_ = true;
    thread_ = std::thread(&FrameCaptureManager::run, this);
    return true;
}

void FrameCaptureManager::stop() {
    if (!running_ && !thread_.joinable()) {
        return;
    }
    running_ = false;
    stop_cv_.notify_all();
    if (thread_.joinable()) {
        thread_.join();
    }
    if (source_) {
        source_->close();
    }
}

bool FrameCaptureManager::is_running() const {
    return running_;
}

void FrameCaptureManager::request_restart() {
    restart_requested_ = true;
    stop_cv_.notify_all();
}

void FrameCaptureManager::recover(int* backoff_ms, const char* error, bool count_failure) {
    server_->record_recovery(error ? error : "", count_failure);
    source_->close();
    std::unique_lock<std::mutex> lock(mutex_);
    stop_cv_.wait_for(lock, std::chrono::milliseconds(*backoff_ms), [&]() {
        return !running_;
    });
    if (*backoff_ms < options_.recovery_max_backoff_ms) {
        *backoff_ms *= 2;
        if (*backoff_ms > options_.recovery_max_backoff_ms) {
            *backoff_ms = options_.recovery_max_backoff_ms;
        }
    }
}

void FrameCaptureManager::run() {
    int backoff_ms = options_.recovery_initial_backoff_ms;
    if (backoff_ms <= 0) backoff_ms = 1;
    if (options_.recovery_max_backoff_ms <= 0) options_.recovery_max_backoff_ms = backoff_ms;

    while (running_) {
        if (!source_->open()) {
            recover(&backoff_ms, "open failed", true);
            continue;
        }
        server_->set_state("RUNNING");
        backoff_ms = options_.recovery_initial_backoff_ms > 0 ? options_.recovery_initial_backoff_ms : 1;
        bool have_next_capture_time = false;
        std::chrono::steady_clock::time_point next_capture_time;

        while (running_) {
            if (options_.capture_interval_ms > 0 && have_next_capture_time) {
                std::unique_lock<std::mutex> lock(mutex_);
                stop_cv_.wait_until(lock, next_capture_time, [&]() {
                    return !running_ || restart_requested_.load();
                });
            }
            if (!running_) {
                break;
            }
            if (restart_requested_.exchange(false)) {
                recover(&backoff_ms, "restart requested", false);
                break;
            }
            CapturedFrame frame;
            if (!source_->capture(&frame)) {
                recover(&backoff_ms, "capture failed", true);
                break;
            }
            server_->append_frame(frame.metadata, frame.data.data(), frame.data.size(), nullptr);
            if (options_.capture_interval_ms > 0) {
                next_capture_time = std::chrono::steady_clock::now() +
                    std::chrono::milliseconds(options_.capture_interval_ms);
                have_next_capture_time = true;
            }
        }
    }
}

}  // namespace aiden
