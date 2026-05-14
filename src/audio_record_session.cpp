#include "audio_record_session.h"
#include <chrono>
#include <cmath>
#include <stdio.h>
#include <stdint.h>
#include <unistd.h>

namespace aiden {

// Down-mix stereo int16 to mono by averaging L+R.
static std::vector<int16_t> stereo_to_mono(const int16_t* src, size_t stereo_samples) {
    size_t mono_samples = stereo_samples / 2;
    std::vector<int16_t> out(mono_samples);
    for (size_t i = 0; i < mono_samples; i++) {
        int32_t mixed = (int32_t)src[i * 2] + (int32_t)src[i * 2 + 1];
        out[i] = (int16_t)(mixed / 2);
    }
    return out;
}

// Resample int16 mono from src_rate to dst_rate with linear interpolation.
static std::vector<int16_t> resample_linear(const int16_t* src, size_t count,
                                            int src_rate, int dst_rate) {
    if (!src || count == 0 || src_rate <= 0 || dst_rate <= 0) return {};
    if (src_rate == dst_rate) return std::vector<int16_t>(src, src + count);
    double ratio = static_cast<double>(src_rate) / static_cast<double>(dst_rate);
    size_t out_count = static_cast<size_t>(count / ratio);
    if (out_count == 0) out_count = 1;
    std::vector<int16_t> out(out_count);
    for (size_t i = 0; i < out_count; i++) {
        double pos = static_cast<double>(i) * ratio;
        size_t idx = static_cast<size_t>(pos);
        if (idx >= count) idx = count - 1;
        size_t idx_next = (idx + 1 < count) ? (idx + 1) : idx;
        double frac = pos - static_cast<double>(idx);
        double v = static_cast<double>(src[idx]) * (1.0 - frac) +
                   static_cast<double>(src[idx_next]) * frac;
        out[i] = static_cast<int16_t>(v);
    }
    return out;
}

static int snap_sample_rate(int observed_rate) {
    static const int kRates[] = {8000, 16000, 32000, 44100, 48000};
    int best = kRates[0];
    int best_diff = std::abs(observed_rate - best);
    for (size_t i = 1; i < sizeof(kRates) / sizeof(kRates[0]); ++i) {
        int diff = std::abs(observed_rate - kRates[i]);
        if (diff < best_diff) {
            best = kRates[i];
            best_diff = diff;
        }
    }
    return best;
}

AudioRecordSession::AudioRecordSession(uint64_t session_id, const AudioFormat& fmt)
    : session_id_(session_id), fmt_(fmt), stopped_(false) {}

AudioRecordSession::~AudioRecordSession() {
    stop();
    if (capture_thread_.joinable()) capture_thread_.join();
}

bool AudioRecordSession::start() {
    // Open hardware with requested format; runtime loop will auto-correct
    // channels/sample-rate based on real frame shape and timestamps.
    AudioConfig cfg;
    cfg.sample_rate = static_cast<int>(fmt_.sample_rate);
    cfg.channels    = static_cast<int>(fmt_.channels);
    cfg.bit_width   = static_cast<int>(fmt_.bit_width);

    if (!capture_.init(cfg)) {
        fprintf(stderr, "[audio_service] record session %llu: AudioCapture init failed\n",
                static_cast<unsigned long long>(session_id_));
        return false;
    }

    // RV1106 capture path typically delivers stereo even in mono mode.
    hw_sample_rate_ = cfg.sample_rate;
    hw_channels_    = 2;
    prev_timestamp_us_ = 0;

    capture_thread_ = std::thread(&AudioRecordSession::capture_loop, this);
    return true;
}

void AudioRecordSession::stop() {
    if (stopped_.exchange(true)) return;
    // Do not call capture_.stop() here: the capture thread owns the device and
    // will exit its loop on the next get_frame() timeout once stopped_ is set.
    cv_.notify_all();
}

AidenServiceStatus AudioRecordSession::pop_chunk(uint32_t timeout_ms, AudioChunkResult* out) {
    std::unique_lock<std::mutex> lock(mutex_);
    auto deadline = std::chrono::steady_clock::now() +
                    std::chrono::milliseconds(timeout_ms);

    while (queue_.empty()) {
        if (stopped_.load()) {
            out->pcm.clear();
            out->end_of_stream = true;
            return AidenServiceStatus::OK;
        }
        if (cv_.wait_until(lock, deadline) == std::cv_status::timeout) {
            return AidenServiceStatus::TIMEOUT;
        }
    }

    out->pcm = std::move(queue_.front());
    queue_.pop();
    out->end_of_stream = false;
    return AidenServiceStatus::OK;
}

void AudioRecordSession::maybe_update_hw_sample_rate(uint64_t timestamp_us,
                                                     size_t frame_samples_per_channel) {
    if (prev_timestamp_us_ == 0 || timestamp_us <= prev_timestamp_us_) {
        prev_timestamp_us_ = timestamp_us;
        return;
    }
    uint64_t delta_us = timestamp_us - prev_timestamp_us_;
    prev_timestamp_us_ = timestamp_us;
    if (delta_us == 0 || frame_samples_per_channel == 0) return;

    int observed = static_cast<int>((frame_samples_per_channel * 1000000ULL) / delta_us);
    int snapped = snap_sample_rate(observed);
    int diff = std::abs(snapped - hw_sample_rate_);
    if (diff > hw_sample_rate_ / 10) {
        fprintf(stderr,
                "[audio_service] adjust hw_sample_rate %d -> %d (observed=%d, frame_samples=%zu, dt=%lluus)\n",
                hw_sample_rate_, snapped, observed, frame_samples_per_channel,
                static_cast<unsigned long long>(delta_us));
        hw_sample_rate_ = snapped;
    }
}

void AudioRecordSession::capture_loop() {
    int consecutive_failures = 0;

    while (!stopped_.load()) {
        AudioFrame frame;
        if (!capture_.get_frame(frame)) {
            consecutive_failures++;
            if (consecutive_failures >= 5) {
                fprintf(stderr,
                        "[audio_service] record session %llu: get_frame failed %d times, restarting capture\n",
                        static_cast<unsigned long long>(session_id_),
                        consecutive_failures);
                capture_.stop();
                AudioConfig cfg;
                cfg.sample_rate = static_cast<int>(fmt_.sample_rate);
                cfg.channels    = static_cast<int>(fmt_.channels);
                cfg.bit_width   = static_cast<int>(fmt_.bit_width);
                if (!capture_.init(cfg)) {
                    fprintf(stderr,
                            "[audio_service] record session %llu: capture re-init failed\n",
                            static_cast<unsigned long long>(session_id_));
                    usleep(20000);
                } else {
                    consecutive_failures = 0;
                    prev_timestamp_us_ = 0;
                }
            } else {
                usleep(5000);
            }
            continue;
        }
        consecutive_failures = 0;

        if (frame.data && frame.length > 0) {
            const int16_t* src = reinterpret_cast<const int16_t*>(frame.data);
            size_t total_samples = frame.length / sizeof(int16_t);
            if (total_samples == 0) {
                capture_.release_frame();
                continue;
            }
            if ((total_samples % 2) != 0) {
                // Odd sample count cannot be stereo-interleaved.
                hw_channels_ = 1;
            }

            size_t frame_samples_per_channel =
                (hw_channels_ == 2 && total_samples >= 2) ? (total_samples / 2) : total_samples;
            maybe_update_hw_sample_rate(frame.timestamp, frame_samples_per_channel);

            // Step 1: down-mix stereo to mono if needed.
            std::vector<int16_t> mono;
            if (hw_channels_ == 2) {
                mono = stereo_to_mono(src, total_samples);
            } else {
                mono.assign(src, src + total_samples);
            }

            // Step 2: resample to target rate if needed.
            std::vector<int16_t> resampled = resample_linear(
                mono.data(), mono.size(),
                hw_sample_rate_, static_cast<int>(fmt_.sample_rate));

            // Step 3: push as raw bytes.
            const uint8_t* out_bytes = reinterpret_cast<const uint8_t*>(resampled.data());
            std::vector<uint8_t> chunk(out_bytes, out_bytes + resampled.size() * sizeof(int16_t));

            std::unique_lock<std::mutex> lock(mutex_);
            if (queue_.size() < kMaxQueueChunks) {
                queue_.push(std::move(chunk));
                cv_.notify_one();
            }
        }
        capture_.release_frame();
    }

    capture_.stop();
}

}  // namespace aiden
