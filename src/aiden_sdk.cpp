#include "aiden_sdk.h"

#include <atomic>
#include <cstring>
#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <stdio.h>
#include <string>
#include <unistd.h>

extern "C" {
#include "rk_debug.h"
#include "rk_mpi_ai.h"
#include "rk_mpi_amix.h"
#include "rk_mpi_ao.h"
#include "rk_mpi_mb.h"
#include "rk_mpi_sys.h"
#include "rk_mpi_vi.h"
}

namespace aiden {

static std::atomic<int> sys_init_count{0};

static void ensure_sys_init() {
    if (sys_init_count.fetch_add(1) == 0) {
        RK_MPI_SYS_Init();
    }
}

static void maybe_sys_deinit() {
    if (sys_init_count.fetch_sub(1) == 1) {
        RK_MPI_SYS_Exit();
    }
}

static AUDIO_BIT_WIDTH_E to_bit_width(int bits) {
    switch (bits) {
    case 8:  return AUDIO_BIT_WIDTH_8;
    case 24: return AUDIO_BIT_WIDTH_24;
    default: return AUDIO_BIT_WIDTH_16;
    }
}

// --- WakeupListener ---

class WakeupListenerImpl {
public:
    std::atomic<bool> running{false};
    pthread_t thread{};
    int gpio_pin = 0;
    WakeupCallback callback;

    static void* thread_func(void* arg) {
        auto* self = static_cast<WakeupListenerImpl*>(arg);
        self->run();
        return nullptr;
    }

    void run() {
        char gpio_path[64];
        snprintf(gpio_path, sizeof(gpio_path), "/sys/class/gpio/gpio%d/value", gpio_pin);

        int fd = open(gpio_path, O_RDONLY);
        if (fd < 0) {
            fprintf(stderr, "Failed to open GPIO %d\n", gpio_pin);
            running = false;
            return;
        }

        struct pollfd pfd;
        pfd.fd = fd;
        pfd.events = POLLPRI | POLLERR;

        char buf[64];
        while (running) {
            lseek(fd, 0, SEEK_SET);
            read(fd, buf, sizeof(buf));

            int ret = poll(&pfd, 1, 500);
            if (ret > 0 && (pfd.revents & POLLPRI)) {
                if (callback) {
                    callback();
                }
                usleep(150000);
            }
        }

        close(fd);
    }
};

WakeupListener::WakeupListener() : impl_(new WakeupListenerImpl()) {}
WakeupListener::~WakeupListener() { stop(); }

bool WakeupListener::start(int gpio_pin, WakeupCallback callback) {
    if (impl_->running) return false;

    // Export GPIO
    int fd = open("/sys/class/gpio/export", O_WRONLY);
    if (fd != -1) {
        char pin_str[8];
        snprintf(pin_str, sizeof(pin_str), "%d", gpio_pin);
        write(fd, pin_str, strlen(pin_str));
        close(fd);
    }

    // Set direction to input
    char path[64];
    snprintf(path, sizeof(path), "/sys/class/gpio/gpio%d/direction", gpio_pin);
    fd = open(path, O_WRONLY);
    if (fd != -1) {
        write(fd, "in", 2);
        close(fd);
    }

    // Set edge to falling
    snprintf(path, sizeof(path), "/sys/class/gpio/gpio%d/edge", gpio_pin);
    fd = open(path, O_WRONLY);
    if (fd != -1) {
        write(fd, "falling", 7);
        close(fd);
    }

    impl_->gpio_pin = gpio_pin;
    impl_->callback = callback;
    impl_->running = true;

    pthread_create(&impl_->thread, nullptr, WakeupListenerImpl::thread_func, impl_.get());
    return true;
}

void WakeupListener::stop() {
    if (impl_->running) {
        impl_->running = false;
        pthread_join(impl_->thread, nullptr);
    }
}

bool WakeupListener::is_running() const {
    return impl_->running;
}

// --- AudioCapture ---

class AudioCaptureImpl {
public:
    std::atomic<bool> running{false};
    bool initialized = false;
    pthread_t thread{};
    AudioConfig config;
    AudioStreamCallback callback;

    AUDIO_DEV dev_id = 0;
    AI_CHN chn_id = 0;
    AIO_ATTR_S attr{};
    AUDIO_FRAME_S frame{};

    static void* thread_func(void* arg) {
        auto* self = static_cast<AudioCaptureImpl*>(arg);
        self->run();
        return nullptr;
    }

    void run() {
        while (running) {
            RK_S32 ret = RK_MPI_AI_GetFrame(dev_id, chn_id, &frame, nullptr, 500);
            if (ret == RK_SUCCESS) {
                void* data = RK_MPI_MB_Handle2VirAddr(frame.pMbBlk);
                if (callback && data) {
                    AudioFrame af;
                    af.data = data;
                    af.length = frame.u32Len;
                    af.timestamp = frame.u64TimeStamp;
                    callback(af);
                }
                RK_MPI_AI_ReleaseFrame(dev_id, chn_id, &frame, nullptr);
            }
        }
    }
};

AudioCapture::AudioCapture() : impl_(new AudioCaptureImpl()) {}
AudioCapture::~AudioCapture() { stop(); }

bool AudioCapture::init(const AudioConfig& config) {
    ensure_sys_init();

    impl_->config = config;
    impl_->dev_id = 0;
    impl_->chn_id = 0;

    memset(&impl_->attr, 0, sizeof(AIO_ATTR_S));

    if (config.device_name) {
        strncpy((char*)impl_->attr.u8CardName, config.device_name, sizeof(impl_->attr.u8CardName) - 1);
    }

    // Hardware always opens with 2 channels (rv1106-acodec minimum)
    impl_->attr.soundCard.channels = 2;
    impl_->attr.soundCard.sampleRate = config.sample_rate;
    impl_->attr.soundCard.bitWidth = to_bit_width(config.bit_width);
    impl_->attr.enBitwidth = to_bit_width(config.bit_width);
    impl_->attr.enSamplerate = (AUDIO_SAMPLE_RATE_E)config.sample_rate;
    impl_->attr.enSoundmode = (config.channels == 1) ? AUDIO_SOUND_MODE_MONO : AUDIO_SOUND_MODE_STEREO;
    impl_->attr.u32PtNumPerFrm = 1024;
    impl_->attr.u32FrmNum = 4;
    impl_->attr.u32EXFlag = 0;
    impl_->attr.u32ChnCnt = 2;

    RK_S32 ret = RK_MPI_AI_SetPubAttr(impl_->dev_id, &impl_->attr);
    if (ret != RK_SUCCESS) return false;

    // RV1106 mixer: enable loopback and set ADC volume
    RK_MPI_AMIX_SetControl(impl_->dev_id, "I2STDM Digital Loopback Mode", (char*)"Mode2");
    RK_MPI_AMIX_SetControl(impl_->dev_id, "ADC ALC Left Volume", (char*)"22");
    RK_MPI_AMIX_SetControl(impl_->dev_id, "ADC ALC Right Volume", (char*)"22");

    ret = RK_MPI_AI_Enable(impl_->dev_id);
    if (ret != RK_SUCCESS) return false;

    // Set channel param — s32UsrFrmDepth must be > 0 for GetFrame to work
    AI_CHN_PARAM_S chnParam;
    memset(&chnParam, 0, sizeof(AI_CHN_PARAM_S));
    chnParam.s32UsrFrmDepth = 4;
    RK_MPI_AI_SetChnParam(impl_->dev_id, impl_->chn_id, &chnParam);

    ret = RK_MPI_AI_EnableChn(impl_->dev_id, impl_->chn_id);
    if (ret != RK_SUCCESS) return false;

    RK_MPI_AI_SetVolume(impl_->dev_id, 100);
    RK_MPI_AI_SetTrackMode(impl_->dev_id, AUDIO_TRACK_NORMAL);

    impl_->initialized = true;
    return true;
}

bool AudioCapture::start(AudioStreamCallback callback) {
    if (impl_->running) return false;

    impl_->callback = callback;
    impl_->running = true;
    pthread_create(&impl_->thread, nullptr, AudioCaptureImpl::thread_func, impl_.get());
    return true;
}

void AudioCapture::stop() {
    if (!impl_->initialized) return;

    if (impl_->running) {
        impl_->running = false;
        pthread_join(impl_->thread, nullptr);
    }

    RK_MPI_AI_DisableChn(impl_->dev_id, impl_->chn_id);
    RK_MPI_AI_Disable(impl_->dev_id);
    RK_MPI_AMIX_SetControl(impl_->dev_id, "I2STDM Digital Loopback Mode", (char*)"Disabled");

    impl_->initialized = false;
    maybe_sys_deinit();
}

bool AudioCapture::get_frame(AudioFrame& frame) {
    RK_S32 ret = RK_MPI_AI_GetFrame(impl_->dev_id, impl_->chn_id, &impl_->frame, nullptr, 500);
    if (ret == RK_SUCCESS) {
        void* data = RK_MPI_MB_Handle2VirAddr(impl_->frame.pMbBlk);
        frame.data = data;
        frame.length = impl_->frame.u32Len;
        frame.timestamp = impl_->frame.u64TimeStamp;
        return true;
    }
    return false;
}

void AudioCapture::release_frame() {
    RK_MPI_AI_ReleaseFrame(impl_->dev_id, impl_->chn_id, &impl_->frame, nullptr);
}

bool AudioCapture::is_running() const {
    return impl_->running;
}

// --- AudioPlayer ---

class AudioPlayerImpl {
public:
    AudioConfig config;
    bool initialized = false;

    AUDIO_DEV dev_id = 0;
    AO_CHN chn_id = 0;
    AIO_ATTR_S attr{};
};

AudioPlayer::AudioPlayer() : impl_(new AudioPlayerImpl()) {}
AudioPlayer::~AudioPlayer() { stop(); }

bool AudioPlayer::init(const AudioConfig& config) {
    ensure_sys_init();

    impl_->config = config;
    impl_->dev_id = 0;
    impl_->chn_id = 0;

    memset(&impl_->attr, 0, sizeof(AIO_ATTR_S));

    if (config.device_name) {
        strncpy((char*)impl_->attr.u8CardName, config.device_name, sizeof(impl_->attr.u8CardName) - 1);
    }

    // Hardware always opens with 2 channels (rv1106-acodec minimum)
    impl_->attr.soundCard.channels = 2;
    impl_->attr.soundCard.sampleRate = config.sample_rate;
    impl_->attr.soundCard.bitWidth = to_bit_width(config.bit_width);
    impl_->attr.enBitwidth = to_bit_width(config.bit_width);
    impl_->attr.enSamplerate = (AUDIO_SAMPLE_RATE_E)config.sample_rate;
    impl_->attr.enSoundmode = (config.channels == 1) ? AUDIO_SOUND_MODE_MONO : AUDIO_SOUND_MODE_STEREO;
    impl_->attr.u32PtNumPerFrm = 1024;
    impl_->attr.u32FrmNum = 4;
    impl_->attr.u32EXFlag = 0;
    impl_->attr.u32ChnCnt = 2;

    RK_S32 ret = RK_MPI_AO_SetPubAttr(impl_->dev_id, &impl_->attr);
    if (ret != RK_SUCCESS) return false;

    ret = RK_MPI_AO_Enable(impl_->dev_id);
    if (ret != RK_SUCCESS) return false;

    AO_CHN_PARAM_S chnParam;
    memset(&chnParam, 0, sizeof(AO_CHN_PARAM_S));
    chnParam.enLoopbackMode = AUDIO_LOOPBACK_NONE;
    RK_MPI_AO_SetChnParams(impl_->dev_id, impl_->chn_id, &chnParam);

    if (config.channels == 1)
        RK_MPI_AO_SetTrackMode(impl_->dev_id, AUDIO_TRACK_OUT_STEREO);
    else
        RK_MPI_AO_SetTrackMode(impl_->dev_id, AUDIO_TRACK_NORMAL);

    ret = RK_MPI_AO_EnableChn(impl_->dev_id, impl_->chn_id);
    if (ret != RK_SUCCESS) return false;

    ret = RK_MPI_AO_EnableReSmp(impl_->dev_id, impl_->chn_id,
                                (AUDIO_SAMPLE_RATE_E)config.sample_rate);
    if (ret != RK_SUCCESS) return false;

    RK_MPI_AO_SetVolume(impl_->dev_id, 100);

    impl_->initialized = true;
    return true;
}

bool AudioPlayer::play(const void* data, uint32_t length) {
    if (!impl_->initialized) return false;

    AUDIO_FRAME_S frame{};
    frame.u32Len = length;
    frame.enBitWidth = to_bit_width(impl_->config.bit_width);
    frame.enSoundMode = (impl_->config.channels == 1) ? AUDIO_SOUND_MODE_MONO : AUDIO_SOUND_MODE_STEREO;
    frame.bBypassMbBlk = RK_FALSE;

    MB_EXT_CONFIG_S extConfig;
    memset(&extConfig, 0, sizeof(MB_EXT_CONFIG_S));
    extConfig.pOpaque = const_cast<void*>(data);
    extConfig.pu8VirAddr = (RK_U8*)data;
    extConfig.u64Size = length;

    RK_S32 ret = RK_MPI_SYS_CreateMB(&frame.pMbBlk, &extConfig);
    if (ret != RK_SUCCESS) return false;

    ret = RK_MPI_AO_SendFrame(impl_->dev_id, impl_->chn_id, &frame, -1);

    RK_MPI_MB_ReleaseMB(frame.pMbBlk);
    return ret == RK_SUCCESS;
}

bool AudioPlayer::play(const AudioFrame& frame) {
    return play(frame.data, frame.length);
}

void AudioPlayer::stop() {
    if (!impl_->initialized) return;

    RK_MPI_AO_ClearChnBuf(impl_->dev_id, impl_->chn_id);
    RK_MPI_AO_DisableReSmp(impl_->dev_id, impl_->chn_id);
    RK_MPI_AO_DisableChn(impl_->dev_id, impl_->chn_id);
    RK_MPI_AO_Disable(impl_->dev_id);

    impl_->initialized = false;
    maybe_sys_deinit();
}

void AudioPlayer::pause() {
    if (impl_->initialized)
        RK_MPI_AO_PauseChn(impl_->dev_id, impl_->chn_id);
}

void AudioPlayer::resume() {
    if (impl_->initialized)
        RK_MPI_AO_ResumeChn(impl_->dev_id, impl_->chn_id);
}

bool AudioPlayer::set_volume(int volume_db) {
    if (!impl_->initialized) return false;
    return RK_MPI_AO_SetVolume(impl_->dev_id, volume_db) == RK_SUCCESS;
}

int AudioPlayer::get_volume() const {
    if (!impl_->initialized) return 0;
    RK_S32 vol = 0;
    RK_MPI_AO_GetVolume(impl_->dev_id, &vol);
    return vol;
}

bool AudioPlayer::is_initialized() const {
    return impl_->initialized;
}

// --- CameraCapture ---

static PIXEL_FORMAT_E to_pixel_format(const char* fmt) {
    if (!fmt || strcmp(fmt, "nv12") == 0) return RK_FMT_YUV420SP;
    if (strcmp(fmt, "nv16") == 0) return RK_FMT_YUV422SP;
    if (strcmp(fmt, "uyvy") == 0) return RK_FMT_YUV422_UYVY;
    if (strcmp(fmt, "yuyv") == 0) return RK_FMT_YUV422_YUYV;
    return RK_FMT_YUV420SP;
}

class CameraCaptureImpl {
public:
    std::atomic<bool> running{false};
    bool initialized = false;
    pthread_t thread{};
    CameraConfig config;
    VideoStreamCallback callback;

    VI_DEV dev_id = 0;
    VI_PIPE pipe_id = 0;
    VI_CHN chn_id = 1;
    VI_DEV_ATTR_S dev_attr{};
    VI_DEV_BIND_PIPE_S bind_pipe{};
    VI_CHN_ATTR_S chn_attr{};
    VIDEO_FRAME_INFO_S frame{};

    static void* thread_func(void* arg) {
        auto* self = static_cast<CameraCaptureImpl*>(arg);
        self->run();
        return nullptr;
    }

    void run() {
        while (running) {
            RK_S32 ret = RK_MPI_VI_GetChnFrame(pipe_id, chn_id, &frame, 500);
            if (ret == RK_SUCCESS) {
                void* data = RK_MPI_MB_Handle2VirAddr(frame.stVFrame.pMbBlk);
                if (callback && data) {
                    VideoFrame vf;
                    vf.data = data;
                    vf.width = frame.stVFrame.u32Width;
                    vf.height = frame.stVFrame.u32Height;
                    vf.length = frame.stVFrame.u64PrivateData;
                    vf.timestamp = frame.stVFrame.u64PTS;
                    vf.sequence = frame.stVFrame.u32TimeRef;
                    callback(vf);
                }
                RK_MPI_VI_ReleaseChnFrame(pipe_id, chn_id, &frame);
            }
            usleep(1000);
        }
    }
};

CameraCapture::CameraCapture() : impl_(new CameraCaptureImpl()) {}
CameraCapture::~CameraCapture() { stop(); }

bool CameraCapture::init(const CameraConfig& config) {
    ensure_sys_init();

    impl_->config = config;
    impl_->dev_id = config.camera_id;
    impl_->pipe_id = config.camera_id;
    impl_->chn_id = 1;

    // Check if device is already configured
    RK_S32 ret = RK_MPI_VI_GetDevAttr(impl_->dev_id, &impl_->dev_attr);
    if (ret == RK_ERR_VI_NOT_CONFIG) {
        // Configure device
        impl_->dev_attr.enIntfMode = VI_MODE_MIPI;
        impl_->dev_attr.enWorkMode = VI_WORK_MODE_1Multiplex;
        impl_->dev_attr.enInputDataType = VI_DATA_TYPE_RGB;

        ret = RK_MPI_VI_SetDevAttr(impl_->dev_id, &impl_->dev_attr);
        if (ret != RK_SUCCESS) return false;
    }

    // Enable device if not already enabled
    ret = RK_MPI_VI_GetDevIsEnable(impl_->dev_id);
    if (ret != RK_SUCCESS) {
        ret = RK_MPI_VI_EnableDev(impl_->dev_id);
        if (ret != RK_SUCCESS) return false;

        // Bind device to pipe
        impl_->bind_pipe.u32Num = 1;
        impl_->bind_pipe.PipeId[0] = impl_->pipe_id;
        ret = RK_MPI_VI_SetDevBindPipe(impl_->dev_id, &impl_->bind_pipe);
        if (ret != RK_SUCCESS) {
            RK_MPI_VI_DisableDev(impl_->dev_id);
            return false;
        }
    }

    // Configure channel
    impl_->chn_attr.stSize.u32Width = config.width;
    impl_->chn_attr.stSize.u32Height = config.height;
    impl_->chn_attr.enPixelFormat = to_pixel_format(config.pixel_format);
    impl_->chn_attr.enCompressMode = COMPRESS_MODE_NONE;
    impl_->chn_attr.stFrameRate.s32SrcFrameRate = -1;
    impl_->chn_attr.stFrameRate.s32DstFrameRate = -1;
    impl_->chn_attr.u32Depth = 1;
    impl_->chn_attr.stIspOpt.u32BufCount = 2;
    impl_->chn_attr.stIspOpt.enMemoryType = VI_V4L2_MEMORY_TYPE_DMABUF;

    if (config.device_name) {
        strncpy(impl_->chn_attr.stIspOpt.aEntityName, config.device_name,
                sizeof(impl_->chn_attr.stIspOpt.aEntityName) - 1);
    }

    ret = RK_MPI_VI_SetChnAttr(impl_->pipe_id, impl_->chn_id, &impl_->chn_attr);
    if (ret != RK_SUCCESS) {
        RK_MPI_VI_DisableDev(impl_->dev_id);
        return false;
    }

    // Enable channel
    ret = RK_MPI_VI_EnableChn(impl_->pipe_id, impl_->chn_id);
    if (ret != RK_SUCCESS) {
        RK_MPI_VI_DisableDev(impl_->dev_id);
        return false;
    }

    impl_->initialized = true;
    return true;
}

bool CameraCapture::start(VideoStreamCallback callback) {
    if (impl_->running) return false;

    impl_->callback = callback;
    impl_->running = true;
    pthread_create(&impl_->thread, nullptr, CameraCaptureImpl::thread_func, impl_.get());
    return true;
}

void CameraCapture::stop() {
    if (!impl_->initialized) return;

    if (impl_->running) {
        impl_->running = false;
        pthread_join(impl_->thread, nullptr);
    }

    RK_MPI_VI_DisableChn(impl_->pipe_id, impl_->chn_id);
    RK_MPI_VI_DisableDev(impl_->dev_id);

    impl_->initialized = false;
    maybe_sys_deinit();
}

bool CameraCapture::get_frame(VideoFrame& frame) {
    RK_S32 ret = RK_MPI_VI_GetChnFrame(impl_->pipe_id, impl_->chn_id, &impl_->frame, -1);
    if (ret == RK_SUCCESS) {
        void* data = RK_MPI_MB_Handle2VirAddr(impl_->frame.stVFrame.pMbBlk);
        frame.data = data;
        frame.width = impl_->frame.stVFrame.u32Width;
        frame.height = impl_->frame.stVFrame.u32Height;
        frame.length = impl_->frame.stVFrame.u64PrivateData;
        frame.timestamp = impl_->frame.stVFrame.u64PTS;
        frame.sequence = impl_->frame.stVFrame.u32TimeRef;
        return true;
    }
    return false;
}

void CameraCapture::release_frame() {
    RK_MPI_VI_ReleaseChnFrame(impl_->pipe_id, impl_->chn_id, &impl_->frame);
}

bool CameraCapture::is_running() const {
    return impl_->running;
}

}  // namespace aiden
