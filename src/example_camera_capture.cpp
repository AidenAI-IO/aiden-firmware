#include "aiden_sdk.h"
#include <stdio.h>
#include <signal.h>
#include <unistd.h>

static bool quit = false;
static int frame_count = 0;

void signal_handler(int sig) {
    printf("Received signal %d, exiting...\n", sig);
    quit = true;
}

void on_video_frame(const aiden::VideoFrame& frame) {
    frame_count++;
    printf("Frame #%d: %ux%u, %u bytes, seq=%u, pts=%llu\n",
           frame_count, frame.width, frame.height,
           frame.length, frame.sequence, frame.timestamp);
}

int main(int argc, char* argv[]) {
    signal(SIGINT, signal_handler);

    aiden::CameraConfig config;
    config.device_name = (argc > 1) ? argv[1] : nullptr;
    config.width = 1920;
    config.height = 1080;
    config.camera_id = 0;
    config.pixel_format = "nv12";

    aiden::CameraCapture camera;

    printf("Initializing camera...\n");
    if (!camera.init(config)) {
        fprintf(stderr, "Failed to initialize camera\n");
        return 1;
    }

    printf("Starting camera capture...\n");
    if (!camera.start(on_video_frame)) {
        fprintf(stderr, "Failed to start camera capture\n");
        return 1;
    }

    printf("Capturing video. Press Ctrl+C to exit.\n");

    while (!quit) {
        sleep(1);
    }

    camera.stop();
    printf("Captured %d frames. Stopped.\n", frame_count);

    return 0;
}
