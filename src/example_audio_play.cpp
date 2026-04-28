#include "aiden_sdk.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

// Generate a simple sine wave tone
void generate_tone(short* buffer, int samples, int sample_rate, float frequency) {
    for (int i = 0; i < samples; i++) {
        float t = (float)i / sample_rate;
        buffer[i] = (short)(sin(2.0 * M_PI * frequency * t) * 16000);
    }
}

int main(int argc, char* argv[]) {
    aiden::AudioConfig config;
    config.device_name = (argc > 1) ? argv[1] : nullptr;
    config.sample_rate = 16000;
    config.channels = 1;
    config.bit_width = 16;

    aiden::AudioPlayer player;

    printf("Initializing audio player...\n");
    if (!player.init(config)) {
        fprintf(stderr, "Failed to initialize audio player\n");
        return 1;
    }

    printf("Playing 440Hz tone for 2 seconds...\n");

    const int samples_per_buffer = 1024;
    const int total_samples = config.sample_rate * 2;  // 2 seconds
    short buffer[samples_per_buffer];

    for (int offset = 0; offset < total_samples; offset += samples_per_buffer) {
        int samples = (offset + samples_per_buffer > total_samples)
                      ? (total_samples - offset)
                      : samples_per_buffer;

        generate_tone(buffer, samples, config.sample_rate, 440.0f);

        if (!player.play(buffer, samples * sizeof(short))) {
            fprintf(stderr, "Failed to play audio\n");
            break;
        }
    }

    printf("Playback complete.\n");
    player.stop();

    return 0;
}
