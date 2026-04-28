#ifndef AIDEN_SDK_H
#define AIDEN_SDK_H

#include <functional>
#include <memory>
#include <vector>
#include <cstdint>

namespace aiden {

// Audio configuration
struct AudioConfig {
    const char* device_name = "hw:0,0";
    int sample_rate = 16000;
    int channels = 1;
    int bit_width = 16;  // 8, 16, or 24
};

// Audio frame data
struct AudioFrame {
    void* data;
    uint32_t length;
    uint64_t timestamp;
};

// Camera configuration
struct CameraConfig {
    const char* device_name = nullptr;  // e.g., "/dev/video0"
    int width = 1920;
    int height = 1080;
    int camera_id = 0;
    const char* pixel_format = "nv12";  // nv12, nv16, uyvy, yuyv
};

// Video frame data
struct VideoFrame {
    void* data;
    uint32_t width;
    uint32_t height;
    uint32_t length;
    uint64_t timestamp;
    uint32_t sequence;
};

// Wakeup event callback
using WakeupCallback = std::function<void()>;

// Audio stream callback
using AudioStreamCallback = std::function<void(const AudioFrame& frame)>;

// Video stream callback
using VideoStreamCallback = std::function<void(const VideoFrame& frame)>;

// Forward declarations
class WakeupListenerImpl;
class AudioCaptureImpl;
class AudioPlayerImpl;
class CameraCaptureImpl;

// Wakeup event listener
class WakeupListener {
public:
    WakeupListener();
    ~WakeupListener();

    // Start listening for wakeup events on GPIO pin
    bool start(int gpio_pin, WakeupCallback callback);

    // Stop listening
    void stop();

    // Check if listener is running
    bool is_running() const;

private:
    std::unique_ptr<WakeupListenerImpl> impl_;
};

// Audio capture (recording)
class AudioCapture {
public:
    AudioCapture();
    ~AudioCapture();

    // Initialize audio capture with config
    bool init(const AudioConfig& config);

    // Start capturing audio with callback
    bool start(AudioStreamCallback callback);

    // Stop capturing
    void stop();

    // Get a single frame (blocking)
    bool get_frame(AudioFrame& frame);

    // Release frame after processing
    void release_frame();

    // Check if capture is running
    bool is_running() const;

private:
    std::unique_ptr<AudioCaptureImpl> impl_;
};

// Audio playback
class AudioPlayer {
public:
    AudioPlayer();
    ~AudioPlayer();

    // Initialize audio player with config
    bool init(const AudioConfig& config);

    // Play audio data (blocking until queued)
    bool play(const void* data, uint32_t length);

    // Play audio frame
    bool play(const AudioFrame& frame);

    // Stop playback and clear buffer
    void stop();

    // Pause playback
    void pause();

    // Resume playback
    void resume();

    // Set volume (-60 to 0 dB)
    bool set_volume(int volume_db);

    // Get current volume
    int get_volume() const;

    // Check if player is initialized
    bool is_initialized() const;

private:
    std::unique_ptr<AudioPlayerImpl> impl_;
};

// Camera capture
class CameraCapture {
public:
    CameraCapture();
    ~CameraCapture();

    // Initialize camera with config
    bool init(const CameraConfig& config);

    // Start capturing video with callback
    bool start(VideoStreamCallback callback);

    // Stop capturing
    void stop();

    // Get a single frame (blocking)
    bool get_frame(VideoFrame& frame);

    // Release frame after processing
    void release_frame();

    // Check if capture is running
    bool is_running() const;

private:
    std::unique_ptr<CameraCaptureImpl> impl_;
};

}  // namespace aiden

#endif  // AIDEN_SDK_H
