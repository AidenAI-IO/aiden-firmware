#include "doctest.h"
#include "vad.h"
#include <vector>
#include <cstdint>

namespace {

// 16kHz sample rate, 30ms frame = 480 samples
constexpr int kSampleRate = 16000;
constexpr int kFrameSamples = 480;

std::vector<int16_t> silence_frame() {
    return std::vector<int16_t>(kFrameSamples, 0);
}

std::vector<int16_t> speech_frame(int16_t amplitude) {
    return std::vector<int16_t>(kFrameSamples, amplitude);
}

}

TEST_CASE("VAD emits nothing for pure silence") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto silence = silence_frame();
    for (int i = 0; i < 50; i++) {
        CHECK(vad.process(silence.data(), silence.size()) == nullptr);
    }
}

TEST_CASE("VAD flushes an utterance after speech then trailing silence") {
    // silence_ms = 300 -> 10 silence frames needed to close the utterance
    // min_speech_ms = 150 -> 5 speech frames needed to qualify
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto speech = speech_frame(2000);
    auto silence = silence_frame();

    for (int i = 0; i < 6; i++) {
        CHECK(vad.process(speech.data(), speech.size()) == nullptr);
    }

    const std::vector<int16_t>* utterance = nullptr;
    for (int i = 0; i < 12 && !utterance; i++) {
        utterance = vad.process(silence.data(), silence.size());
    }

    REQUIRE(utterance != nullptr);
    CHECK(utterance->size() > 0);
    // Buffered speech + trailing silence frames should both be present.
    CHECK(utterance->size() >= (size_t)(6 * kFrameSamples));
}

TEST_CASE("VAD drops speech shorter than min_speech_ms") {
    // min_speech_ms = 150 -> need 5 speech frames; feed only 2.
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto speech = speech_frame(2000);
    auto silence = silence_frame();

    for (int i = 0; i < 2; i++) {
        CHECK(vad.process(speech.data(), speech.size()) == nullptr);
    }

    for (int i = 0; i < 20; i++) {
        CHECK(vad.process(silence.data(), silence.size()) == nullptr);
    }
}

TEST_CASE("VAD reset clears buffered speech") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto speech = speech_frame(2000);
    auto silence = silence_frame();

    for (int i = 0; i < 6; i++) vad.process(speech.data(), speech.size());
    vad.reset();

    // After reset, a short burst of silence should not produce an utterance
    for (int i = 0; i < 20; i++) {
        CHECK(vad.process(silence.data(), silence.size()) == nullptr);
    }
}

TEST_CASE("VAD flush() returns buffered audio in always_buffer mode") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150, /*always_buffer=*/true);
    auto speech = speech_frame(2000);

    for (int i = 0; i < 4; i++) vad.process(speech.data(), speech.size());

    const std::vector<int16_t>* flushed = vad.flush();
    REQUIRE(flushed != nullptr);
    CHECK(flushed->size() == (size_t)(4 * kFrameSamples));
}

TEST_CASE("VAD flush() returns null when nothing buffered") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    CHECK(vad.flush() == nullptr);
}

TEST_CASE("VAD treats exact threshold energy as non-speech") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto threshold = speech_frame(300);
    auto silence = silence_frame();

    for (int i = 0; i < 10; i++) {
        CHECK(vad.process(threshold.data(), threshold.size()) == nullptr);
    }
    for (int i = 0; i < 10; i++) {
        CHECK(vad.process(silence.data(), silence.size()) == nullptr);
    }
}

TEST_CASE("VAD emits utterance at exact minimum speech frame boundary") {
    // min_speech_ms = 150 -> 5 frames required exactly.
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto speech = speech_frame(2000);
    auto silence = silence_frame();

    for (int i = 0; i < 5; i++) {
        CHECK(vad.process(speech.data(), speech.size()) == nullptr);
    }

    const std::vector<int16_t>* utterance = nullptr;
    for (int i = 0; i < 10 && !utterance; i++) {
        utterance = vad.process(silence.data(), silence.size());
    }

    REQUIRE(utterance != nullptr);
    CHECK(utterance->size() == (size_t)(15 * kFrameSamples));
}

TEST_CASE("VAD emits utterance at exact silence boundary") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150);
    auto speech = speech_frame(2000);
    auto silence = silence_frame();

    for (int i = 0; i < 6; i++) vad.process(speech.data(), speech.size());

    const std::vector<int16_t>* utterance = nullptr;
    for (int i = 0; i < 9; i++) {
        utterance = vad.process(silence.data(), silence.size());
        CHECK(utterance == nullptr);
    }

    utterance = vad.process(silence.data(), silence.size());
    REQUIRE(utterance != nullptr);
    CHECK(utterance->size() == (size_t)(16 * kFrameSamples));
}

TEST_CASE("VAD flush returns silence-only audio in always_buffer mode") {
    aiden::AudioVAD vad(kSampleRate, 300, 300, 150, /*always_buffer=*/true);
    auto silence = silence_frame();

    for (int i = 0; i < 3; i++) vad.process(silence.data(), silence.size());

    const std::vector<int16_t>* flushed = vad.flush();
    REQUIRE(flushed != nullptr);
    CHECK(flushed->size() == (size_t)(3 * kFrameSamples));
}
