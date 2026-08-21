#include "frame_capture_manager.h"
#include <chrono>
#include <thread>

namespace aiden {

FrameCaptureManager::FrameCaptureManager(FrameCaptureSource* source,
                                         FrameServiceServer* server,
                                         const FrameCaptureManagerOptions& options)
    : source_(source),
      server_(server),
      options_(options),
      running_(false),
      restart_requested_(false),
      requested_generation_(0),
      completed_generation_(0),
      completed_status_(FrameServiceStatus::SERVICE_RECOVERING) {}

FrameCaptureManager::~FrameCaptureManager() {
    stop();
}

bool FrameCaptureManager::start() {
    if (!source_ || !server_ || running_) {
        return false;
    }
    running_ = true;
    server_->set_state("STARTING");
    thread_ = std::thread(&FrameCaptureManager::run, this);
    return true;
}

void FrameCaptureManager::stop() {
    if (!running_ && !thread_.joinable()) {
        return;
    }
    running_ = false;
    work_cv_.notify_all();
    capture_cv_.notify_all();
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
    work_cv_.notify_all();
}

FrameServiceStatus FrameCaptureManager::capture(uint32_t timeout_ms, CapturedFrame* frame) {
    if (!frame) {
        return FrameServiceStatus::INTERNAL_ERROR;
    }

    // Only one caller registers and consumes a result at a time. The worker
    // coalesces timed-out generations so repeated short polls cannot build an
    // unbounded backlog of captures.
    std::unique_lock<std::mutex> request_lock(request_mutex_);
    uint64_t generation = 0;
    {
        std::lock_guard<std::mutex> lock(mutex_);
        if (!running_) {
            return FrameServiceStatus::SERVICE_RECOVERING;
        }
        generation = ++requested_generation_;
    }
    work_cv_.notify_all();

    const int configured_timeout = options_.request_timeout_ms > 0
        ? options_.request_timeout_ms
        : 4000;
    const uint32_t wait_ms = timeout_ms > 0
        ? timeout_ms
        : static_cast<uint32_t>(configured_timeout);

    std::unique_lock<std::mutex> lock(mutex_);
    if (!capture_cv_.wait_for(lock, std::chrono::milliseconds(wait_ms), [&]() {
            return !running_ || completed_generation_ >= generation;
        })) {
        return FrameServiceStatus::TIMEOUT;
    }
    if (completed_generation_ < generation) {
        return FrameServiceStatus::SERVICE_RECOVERING;
    }
    if (completed_status_ == FrameServiceStatus::OK) {
        *frame = std::move(completed_frame_);
    }
    return completed_status_;
}

void FrameCaptureManager::recover(int* backoff_ms, const char* error, bool count_failure) {
    server_->record_recovery(error ? error : "", count_failure);
    source_->close();
    std::unique_lock<std::mutex> lock(mutex_);
    work_cv_.wait_for(lock, std::chrono::milliseconds(*backoff_ms), [&]() {
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
        if (!options_.keep_streamon && !source_->pause()) {
            recover(&backoff_ms, "initial pause failed", true);
            continue;
        }
        backoff_ms = options_.recovery_initial_backoff_ms > 0 ? options_.recovery_initial_backoff_ms : 1;

        while (running_) {
            uint64_t generation = 0;
            {
                std::unique_lock<std::mutex> lock(mutex_);
                work_cv_.wait(lock, [&]() {
                    return !running_ || restart_requested_ ||
                           completed_generation_ < requested_generation_;
                });
                if (!running_) {
                    break;
                }
                if (!restart_requested_) {
                    generation = requested_generation_;
                }
            }

            if (restart_requested_.exchange(false)) {
                recover(&backoff_ms, "restart requested", false);
                break;
            }

            CapturedFrame frame;
            const bool resumed = options_.keep_streamon || source_->resume();
            bool warmed_up = resumed;
            for (int i = 0; warmed_up && i < options_.warmup_frames; ++i) {
                warmed_up = source_->discard();
            }
            const bool captured = warmed_up && source_->capture(&frame);
            const bool paused = options_.keep_streamon || source_->pause();
            const bool ok = resumed && warmed_up && captured && paused;

            {
                std::lock_guard<std::mutex> lock(mutex_);
                completed_generation_ = generation;
                completed_status_ = ok ? FrameServiceStatus::OK
                                       : FrameServiceStatus::SERVICE_RECOVERING;
                if (ok) {
                    completed_frame_ = std::move(frame);
                } else {
                    completed_frame_ = CapturedFrame();
                }
            }
            capture_cv_.notify_all();

            if (!ok) {
                const char* error = !resumed ? "resume failed"
                                  : !warmed_up ? "warmup failed"
                                  : !captured ? "capture failed"
                                              : "pause failed";
                recover(&backoff_ms, error, true);
                break;
            }
            server_->set_state("RUNNING");
        }
    }
}

}  // namespace aiden
