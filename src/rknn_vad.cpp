#include "rknn_api_minimal.h"

#include <algorithm>
#include <cmath>
#include <cstring>
#include <iostream>
#include <sstream>
#include <string>
#include <vector>

namespace {

constexpr int kSampleRate = 16000;
constexpr int kFrameSamples = 512;
constexpr int kSTFTSize = 256;
constexpr int kSTFTHop = 128;
constexpr int kSTFTBins = 129;
constexpr int kSTFTFrames = 3;
constexpr int kFeatureFloats = kSTFTBins * kSTFTFrames;
constexpr int kStateFloats = 2 * 1 * 128;
constexpr int kMinInputFloats = kFrameSamples;
constexpr uint32_t kMissingTensorIndex = 0xffffffffu;

const char* tensor_type_name(rknn_tensor_type type) {
    switch (type) {
    case RKNN_TENSOR_FLOAT32: return "FP32";
    case RKNN_TENSOR_FLOAT16: return "FP16";
    case RKNN_TENSOR_INT8: return "INT8";
    case RKNN_TENSOR_UINT8: return "UINT8";
    case RKNN_TENSOR_INT16: return "INT16";
    case RKNN_TENSOR_UINT16: return "UINT16";
    case RKNN_TENSOR_INT32: return "INT32";
    case RKNN_TENSOR_UINT32: return "UINT32";
    case RKNN_TENSOR_INT64: return "INT64";
    case RKNN_TENSOR_BOOL: return "BOOL";
    case RKNN_TENSOR_INT4: return "INT4";
    case RKNN_TENSOR_BFLOAT16: return "BF16";
    default: return "UNKNOWN";
    }
}

const char* tensor_format_name(rknn_tensor_format format) {
    switch (format) {
    case RKNN_TENSOR_NCHW: return "NCHW";
    case RKNN_TENSOR_NHWC: return "NHWC";
    case RKNN_TENSOR_NC1HWC2: return "NC1HWC2";
    case RKNN_TENSOR_UNDEFINED: return "UNDEFINED";
    default: return "UNKNOWN";
    }
}

std::string dims_string(const rknn_tensor_attr& attr) {
    std::ostringstream out;
    out << "[";
    for (uint32_t i = 0; i < attr.n_dims && i < RKNN_MAX_DIMS; ++i) {
        if (i > 0) out << ",";
        out << attr.dims[i];
    }
    out << "]";
    return out.str();
}

bool read_exact(std::istream& in, char* dst, std::size_t bytes) {
    in.read(dst, static_cast<std::streamsize>(bytes));
    return static_cast<std::size_t>(in.gcount()) == bytes;
}

void protocol_error(const std::string& message) {
    std::cout << "ERR " << message << std::endl;
}

class SileroRKNNVAD {
public:
    ~SileroRKNNVAD() {
        if (ctx_ != 0) {
            rknn_destroy(ctx_);
        }
    }

    bool init(const std::string& model_path, std::string* err) {
        int ret = rknn_init(&ctx_, const_cast<char*>(model_path.c_str()), 0, 0, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "rknn_init failed: " + std::to_string(ret);
            return false;
        }

        rknn_input_output_num io_num;
        std::memset(&io_num, 0, sizeof(io_num));
        ret = rknn_query(ctx_, RKNN_QUERY_IN_OUT_NUM, &io_num, sizeof(io_num));
        if (ret != RKNN_SUCC) {
            *err = "rknn_query input/output count failed: " + std::to_string(ret);
            return false;
        }
        if (io_num.n_input < 2 || io_num.n_output < 2) {
            *err = "Silero VAD RKNN model must expose at least 2 inputs and 2 outputs";
            return false;
        }

        input_attrs_.resize(io_num.n_input);
        output_attrs_.resize(io_num.n_output);
        for (uint32_t i = 0; i < io_num.n_input; ++i) {
            std::memset(&input_attrs_[i], 0, sizeof(rknn_tensor_attr));
            input_attrs_[i].index = i;
            ret = rknn_query(ctx_, RKNN_QUERY_INPUT_ATTR, &input_attrs_[i], sizeof(rknn_tensor_attr));
            if (ret != RKNN_SUCC) {
                *err = "rknn_query input attr failed at index " + std::to_string(i) + ": " + std::to_string(ret);
                return false;
            }
            log_attr("input", input_attrs_[i]);
        }
        for (uint32_t i = 0; i < io_num.n_output; ++i) {
            std::memset(&output_attrs_[i], 0, sizeof(rknn_tensor_attr));
            output_attrs_[i].index = i;
            ret = rknn_query(ctx_, RKNN_QUERY_OUTPUT_ATTR, &output_attrs_[i], sizeof(rknn_tensor_attr));
            if (ret != RKNN_SUCC) {
                *err = "rknn_query output attr failed at index " + std::to_string(i) + ": " + std::to_string(ret);
                return false;
            }
            log_attr("output", output_attrs_[i]);
        }

        input_index_ = find_tensor_index(input_attrs_, "input", 0);
        state_input_index_ = find_tensor_index(input_attrs_, "state", 1);
        sr_index_ = find_tensor_index(input_attrs_, "sr", kMissingTensorIndex);
        output_index_ = find_tensor_index(output_attrs_, "output", 0);
        state_output_index_ = find_tensor_index(output_attrs_, "stateN", 1);

        if (input_index_ >= input_attrs_.size()) {
            *err = "Silero VAD audio input is missing";
            return false;
        }
        feature_input_ = input_attrs_[input_index_].n_elems == kFeatureFloats;
        if (!feature_input_ && input_attrs_[input_index_].n_elems < kMinInputFloats) {
            *err = "Silero VAD audio input is smaller than one frame";
            return false;
        }
        input_.assign(input_attrs_[input_index_].n_elems, 0.0f);
        if (feature_input_) {
            prepare_stft_basis();
        }

        reset();
        return true;
    }

    void reset() {
        std::fill(state_.begin(), state_.end(), 0.0f);
    }

    bool infer(const int16_t* pcm, float* probability, std::string* err) {
        std::fill(input_.begin(), input_.end(), 0.0f);
        if (feature_input_) {
            compute_stft_features(pcm);
        } else {
            for (int i = 0; i < kFrameSamples; ++i) {
                input_[i] = static_cast<float>(pcm[i]) / 32768.0f;
            }
        }

        int64_t sr = kSampleRate;
        std::vector<rknn_input> inputs;

        rknn_input audio_input;
        std::memset(&audio_input, 0, sizeof(audio_input));
        audio_input.index = input_index_;
        audio_input.buf = input_.data();
        audio_input.size = static_cast<uint32_t>(input_.size() * sizeof(float));
        audio_input.type = RKNN_TENSOR_FLOAT32;
        audio_input.fmt = input_format(input_index_);
        inputs.push_back(audio_input);

        rknn_input state_input;
        std::memset(&state_input, 0, sizeof(state_input));
        state_input.index = state_input_index_;
        state_input.buf = state_.data();
        state_input.size = static_cast<uint32_t>(state_.size() * sizeof(float));
        state_input.type = RKNN_TENSOR_FLOAT32;
        state_input.fmt = input_format(state_input_index_);
        inputs.push_back(state_input);

        if (sr_index_ != kMissingTensorIndex) {
            rknn_input sr_input;
            std::memset(&sr_input, 0, sizeof(sr_input));
            sr_input.index = sr_index_;
            sr_input.buf = &sr;
            sr_input.size = sizeof(sr);
            sr_input.type = RKNN_TENSOR_INT64;
            sr_input.fmt = input_format(sr_index_);
            inputs.push_back(sr_input);
        }

        int ret = rknn_inputs_set(ctx_, static_cast<uint32_t>(inputs.size()), inputs.data());
        if (ret != RKNN_SUCC) {
            *err = "rknn_inputs_set failed: " + std::to_string(ret);
            return false;
        }

        ret = rknn_run(ctx_, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "rknn_run failed: " + std::to_string(ret);
            return false;
        }

        rknn_output outputs[2];
        std::memset(outputs, 0, sizeof(outputs));
        outputs[0].index = output_index_;
        outputs[0].want_float = 1;
        outputs[1].index = state_output_index_;
        outputs[1].want_float = 1;

        ret = rknn_outputs_get(ctx_, 2, outputs, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "rknn_outputs_get failed: " + std::to_string(ret);
            return false;
        }

        bool ok = copy_outputs(outputs, probability, err);
        rknn_outputs_release(ctx_, 2, outputs);
        return ok;
    }

private:
    static uint32_t find_tensor_index(const std::vector<rknn_tensor_attr>& attrs, const char* name, uint32_t fallback) {
        for (std::vector<rknn_tensor_attr>::const_iterator it = attrs.begin(); it != attrs.end(); ++it) {
            if (std::string(it->name) == name) {
                return it->index;
            }
        }
        return fallback;
    }

    rknn_tensor_format input_format(uint32_t index) const {
        if (index < input_attrs_.size() && input_attrs_[index].fmt <= RKNN_TENSOR_UNDEFINED) {
            return input_attrs_[index].fmt;
        }
        return RKNN_TENSOR_UNDEFINED;
    }

    bool copy_outputs(rknn_output outputs[2], float* probability, std::string* err) {
        if (outputs[0].buf == nullptr || outputs[1].buf == nullptr) {
            *err = "RKNN returned a null output buffer";
            return false;
        }

        const float* prob = static_cast<const float*>(outputs[0].buf);
        *probability = prob[0];

        if (state_output_index_ < output_attrs_.size() &&
            output_attrs_[state_output_index_].n_elems > 0 &&
            output_attrs_[state_output_index_].n_elems < state_.size()) {
            *err = "Silero VAD state outputs are smaller than expected";
            return false;
        }

        const float* next_state = static_cast<const float*>(outputs[1].buf);
        std::copy(next_state, next_state + state_.size(), state_.begin());
        return true;
    }

    void prepare_stft_basis() {
        stft_real_.assign(kSTFTBins * kSTFTSize, 0.0f);
        stft_imag_.assign(kSTFTBins * kSTFTSize, 0.0f);
        const float pi = 3.14159265358979323846f;
        for (int freq = 0; freq < kSTFTBins; ++freq) {
            for (int n = 0; n < kSTFTSize; ++n) {
                const float window = 0.5f - 0.5f * std::cos((2.0f * pi * n) / kSTFTSize);
                const float angle = (2.0f * pi * freq * n) / kSTFTSize;
                stft_real_[freq * kSTFTSize + n] = window * std::cos(angle);
                stft_imag_[freq * kSTFTSize + n] = window * -std::sin(angle);
            }
        }
    }

    void compute_stft_features(const int16_t* pcm) {
        float padded[kFrameSamples + kSTFTSize / 4];
        std::fill(padded, padded + kFrameSamples + kSTFTSize / 4, 0.0f);
        for (int i = 0; i < kFrameSamples; ++i) {
            padded[i] = static_cast<float>(pcm[i]) / 32768.0f;
        }

        for (int frame = 0; frame < kSTFTFrames; ++frame) {
            const int offset = frame * kSTFTHop;
            for (int freq = 0; freq < kSTFTBins; ++freq) {
                float real = 0.0f;
                float imag = 0.0f;
                const float* real_basis = &stft_real_[freq * kSTFTSize];
                const float* imag_basis = &stft_imag_[freq * kSTFTSize];
                for (int n = 0; n < kSTFTSize; ++n) {
                    const float sample = padded[offset + n];
                    real += sample * real_basis[n];
                    imag += sample * imag_basis[n];
                }
                input_[freq * kSTFTFrames + frame] = std::sqrt(real * real + imag * imag);
            }
        }
    }

    void log_attr(const char* label, const rknn_tensor_attr& attr) const {
        std::cerr << "[rknn_vad] " << label
                  << "[" << attr.index << "] name=" << attr.name
                  << " dims=" << dims_string(attr)
                  << " elems=" << attr.n_elems
                  << " size=" << attr.size
                  << " fmt=" << tensor_format_name(attr.fmt)
                  << " type=" << tensor_type_name(attr.type)
                  << std::endl;
    }

    rknn_context ctx_ = 0;
    std::vector<rknn_tensor_attr> input_attrs_;
    std::vector<rknn_tensor_attr> output_attrs_;
    uint32_t input_index_ = 0;
    uint32_t state_input_index_ = 1;
    uint32_t sr_index_ = 2;
    uint32_t output_index_ = 0;
    uint32_t state_output_index_ = 1;
    bool feature_input_ = false;
    std::vector<float> input_;
    std::vector<float> state_ = std::vector<float>(kStateFloats, 0.0f);
    std::vector<float> stft_real_;
    std::vector<float> stft_imag_;
};

std::string parse_model_arg(int argc, char** argv) {
    for (int i = 1; i + 1 < argc; ++i) {
        if (std::string(argv[i]) == "--model") {
            return argv[i + 1];
        }
    }
    return "";
}

}  // namespace

int main(int argc, char** argv) {
    std::string model_path = parse_model_arg(argc, argv);
    if (model_path.empty()) {
        protocol_error("missing --model path");
        return 2;
    }

    SileroRKNNVAD vad;
    std::string err;
    if (!vad.init(model_path, &err)) {
        protocol_error(err);
        return 1;
    }

    std::cout << "READY" << std::endl;

    std::vector<int16_t> frame(kFrameSamples);
    while (true) {
        char command = 0;
        if (!read_exact(std::cin, &command, 1)) {
            return 0;
        }
        if (command == 'Q') {
            return 0;
        }
        if (command == 'R') {
            vad.reset();
            std::cout << "OK" << std::endl;
            continue;
        }
        if (command != 'F') {
            protocol_error("unknown command");
            continue;
        }
        if (!read_exact(std::cin, reinterpret_cast<char*>(frame.data()), frame.size() * sizeof(int16_t))) {
            return 0;
        }

        float probability = 0.0f;
        if (!vad.infer(frame.data(), &probability, &err)) {
            protocol_error(err);
            continue;
        }
        std::cout << "P " << probability << std::endl;
    }
}
