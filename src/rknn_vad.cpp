#include "rknn_api_minimal.h"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <fstream>
#include <iostream>
#include <limits>
#include <sstream>
#include <string>
#include <vector>

namespace {

constexpr int kSampleRate = 16000;
constexpr int kFrameSamples = 512;
constexpr int kSTFTSize = 256;
constexpr int kSTFTHop = 128;
constexpr int kSTFTBins = 129;
constexpr int kSileroContextSamples = 64;
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

const char* tensor_qnt_type_name(rknn_tensor_qnt_type type) {
    switch (type) {
    case RKNN_TENSOR_QNT_NONE: return "NONE";
    case RKNN_TENSOR_QNT_DFP: return "DFP";
    case RKNN_TENSOR_QNT_AFFINE_ASYMMETRIC: return "AFFINE";
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

uint32_t tensor_type_bytes(rknn_tensor_type type) {
    switch (type) {
    case RKNN_TENSOR_FLOAT32:
    case RKNN_TENSOR_INT32:
    case RKNN_TENSOR_UINT32:
        return 4;
    case RKNN_TENSOR_FLOAT16:
    case RKNN_TENSOR_BFLOAT16:
    case RKNN_TENSOR_INT16:
    case RKNN_TENSOR_UINT16:
        return 2;
    case RKNN_TENSOR_INT8:
    case RKNN_TENSOR_UINT8:
    case RKNN_TENSOR_BOOL:
        return 1;
    case RKNN_TENSOR_INT64:
        return 8;
    default:
        return 0;
    }
}

uint32_t tensor_storage_bytes(const rknn_tensor_attr& attr) {
    if (attr.size_with_stride > 0) {
        return attr.size_with_stride;
    }
    if (attr.size > 0) {
        return attr.size;
    }
    const uint32_t bytes_per_elem = tensor_type_bytes(attr.type);
    return bytes_per_elem == 0 ? 0 : attr.n_elems * bytes_per_elem;
}

template <typename T>
void write_pod(std::vector<uint8_t>* bytes, uint32_t index, T value) {
    std::memcpy(bytes->data() + index * sizeof(T), &value, sizeof(T));
}

template <typename T>
T read_pod(const uint8_t* bytes, uint32_t index) {
    T value;
    std::memcpy(&value, bytes + index * sizeof(T), sizeof(T));
    return value;
}

int32_t clamp_int32(int32_t value, int32_t low, int32_t high) {
    if (value < low) return low;
    if (value > high) return high;
    return value;
}

int32_t quantized_from_float(float value, const rknn_tensor_attr& attr) {
    float q = value;
    if (attr.qnt_type == RKNN_TENSOR_QNT_AFFINE_ASYMMETRIC) {
        if (attr.scale != 0.0f) {
            q = value / attr.scale + static_cast<float>(attr.zp);
        } else {
            q = static_cast<float>(attr.zp);
        }
    } else if (attr.qnt_type == RKNN_TENSOR_QNT_DFP) {
        q = std::ldexp(value, attr.fl);
    }
    return static_cast<int32_t>(std::nearbyint(q));
}

float float_from_quantized(int32_t value, const rknn_tensor_attr& attr) {
    if (attr.qnt_type == RKNN_TENSOR_QNT_AFFINE_ASYMMETRIC) {
        if (attr.scale == 0.0f) {
            return static_cast<float>(value);
        }
        return (static_cast<float>(value) - static_cast<float>(attr.zp)) * attr.scale;
    }
    if (attr.qnt_type == RKNN_TENSOR_QNT_DFP) {
        return std::ldexp(static_cast<float>(value), -attr.fl);
    }
    return static_cast<float>(value);
}

uint16_t float_to_half(float value) {
    uint32_t bits = 0;
    std::memcpy(&bits, &value, sizeof(bits));

    const uint32_t sign = (bits >> 16) & 0x8000u;
    int32_t exponent = static_cast<int32_t>((bits >> 23) & 0xffu) - 127 + 15;
    uint32_t mantissa = bits & 0x7fffffu;

    if (exponent <= 0) {
        if (exponent < -10) {
            return static_cast<uint16_t>(sign);
        }
        mantissa = (mantissa | 0x800000u) >> (1 - exponent);
        return static_cast<uint16_t>(sign | ((mantissa + 0x1000u) >> 13));
    }
    if (exponent >= 31) {
        if (mantissa == 0) {
            return static_cast<uint16_t>(sign | 0x7c00u);
        }
        return static_cast<uint16_t>(sign | 0x7c00u | (mantissa >> 13) | 1u);
    }

    return static_cast<uint16_t>(sign | (static_cast<uint32_t>(exponent) << 10) |
                                 ((mantissa + 0x1000u) >> 13));
}

float half_to_float(uint16_t value) {
    const uint32_t sign = (static_cast<uint32_t>(value & 0x8000u)) << 16;
    uint32_t exponent = (value >> 10) & 0x1fu;
    uint32_t mantissa = value & 0x03ffu;
    uint32_t bits = 0;

    if (exponent == 0) {
        if (mantissa == 0) {
            bits = sign;
        } else {
            exponent = 1;
            while ((mantissa & 0x0400u) == 0) {
                mantissa <<= 1;
                --exponent;
            }
            mantissa &= 0x03ffu;
            bits = sign | ((exponent + 127 - 15) << 23) | (mantissa << 13);
        }
    } else if (exponent == 31) {
        bits = sign | 0x7f800000u | (mantissa << 13);
    } else {
        bits = sign | ((exponent + 127 - 15) << 23) | (mantissa << 13);
    }

    float result = 0.0f;
    std::memcpy(&result, &bits, sizeof(result));
    return result;
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
            release_mems();
            rknn_destroy(ctx_);
        }
    }

    bool init(const std::string& model_path, std::string* err) {
        int ret = rknn_init(&ctx_, const_cast<char*>(model_path.c_str()), 0, 0, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "rknn_init failed: " + std::to_string(ret);
            return false;
        }
        log_sdk_version();

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
            ret = rknn_query(ctx_, RKNN_QUERY_NATIVE_INPUT_ATTR, &input_attrs_[i], sizeof(rknn_tensor_attr));
            if (ret != RKNN_SUCC) {
                *err = "rknn_query native input attr failed at index " + std::to_string(i) + ": " + std::to_string(ret);
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

        const uint32_t x_index = find_tensor_index(input_attrs_, "x", kMissingTensorIndex);
        const uint32_t h_index = find_tensor_index(input_attrs_, "h", kMissingTensorIndex);
        const uint32_t c_index = find_tensor_index(input_attrs_, "c", kMissingTensorIndex);
        const uint32_t prob_index = find_tensor_index(output_attrs_, "prob", kMissingTensorIndex);
        const uint32_t next_h_index = find_tensor_index(output_attrs_, "next_h", kMissingTensorIndex);
        const uint32_t next_c_index = find_tensor_index(output_attrs_, "next_c", kMissingTensorIndex);

        split_state_ = (x_index != kMissingTensorIndex &&
                        h_index != kMissingTensorIndex &&
                        c_index != kMissingTensorIndex &&
                        prob_index != kMissingTensorIndex &&
                        next_h_index != kMissingTensorIndex &&
                        next_c_index != kMissingTensorIndex) ||
                       (io_num.n_input == 3 && io_num.n_output >= 3 &&
                        find_tensor_index(input_attrs_, "state", kMissingTensorIndex) == kMissingTensorIndex);

        if (split_state_) {
            input_index_ = x_index != kMissingTensorIndex ? x_index : 0;
            h_input_index_ = h_index != kMissingTensorIndex ? h_index : 1;
            c_input_index_ = c_index != kMissingTensorIndex ? c_index : 2;
            sr_index_ = kMissingTensorIndex;
            output_index_ = prob_index != kMissingTensorIndex ? prob_index : 0;
            h_output_index_ = next_h_index != kMissingTensorIndex ? next_h_index : 1;
            c_output_index_ = next_c_index != kMissingTensorIndex ? next_c_index : 2;
        } else {
            input_index_ = find_tensor_index(input_attrs_, "input", 0);
            state_input_index_ = find_tensor_index(input_attrs_, "state", 1);
            sr_index_ = find_tensor_index(input_attrs_, "sr", kMissingTensorIndex);
            output_index_ = find_tensor_index(output_attrs_, "output", 0);
            state_output_index_ = find_tensor_index(output_attrs_, "stateN", 1);
        }

        if (input_index_ >= input_attrs_.size()) {
            *err = "Silero VAD audio input is missing";
            return false;
        }
        if (!split_state_ && state_input_index_ >= input_attrs_.size()) {
            *err = "Silero VAD state input is missing";
            return false;
        }
        if (split_state_ && (h_input_index_ >= input_attrs_.size() || c_input_index_ >= input_attrs_.size())) {
            *err = "Silero VAD split state inputs are missing";
            return false;
        }
        if (!split_state_ && (output_index_ >= output_attrs_.size() || state_output_index_ >= output_attrs_.size())) {
            *err = "Silero VAD outputs are missing";
            return false;
        }
        if (split_state_ && (output_index_ >= output_attrs_.size() ||
                             h_output_index_ >= output_attrs_.size() ||
                             c_output_index_ >= output_attrs_.size())) {
            *err = "Silero VAD split state outputs are missing";
            return false;
        }
        feature_input_ = input_attrs_[input_index_].n_elems % kSTFTBins == 0 &&
                         input_attrs_[input_index_].n_elems >= kSTFTBins * 3;
        if (feature_input_) {
            feature_frames_ = input_attrs_[input_index_].n_elems / kSTFTBins;
            const uint32_t context_samples = feature_frames_ >= 4 ? kSileroContextSamples : 0;
            context_.assign(context_samples, 0.0f);
        }
        if (!feature_input_ && input_attrs_[input_index_].n_elems < kMinInputFloats) {
            *err = "Silero VAD audio input is smaller than one frame";
            return false;
        }
        if (!split_state_ && input_attrs_[state_input_index_].n_elems == 0) {
            *err = "Silero VAD state input is empty";
            return false;
        }
        if (split_state_ && (input_attrs_[h_input_index_].n_elems == 0 || input_attrs_[c_input_index_].n_elems == 0)) {
            *err = "Silero VAD split state input is empty";
            return false;
        }
        input_.assign(input_attrs_[input_index_].n_elems, 0.0f);
        if (split_state_) {
            h_.assign(input_attrs_[h_input_index_].n_elems, 0.0f);
            c_.assign(input_attrs_[c_input_index_].n_elems, 0.0f);
        } else {
            state_.assign(input_attrs_[state_input_index_].n_elems, 0.0f);
        }
        if (feature_input_) {
            prepare_stft_basis();
        }
        if (!setup_io_mems(err)) {
            return false;
        }

        reset();
        return true;
    }

    void reset() {
        if (split_state_) {
            std::fill(h_.begin(), h_.end(), 0.0f);
            std::fill(c_.begin(), c_.end(), 0.0f);
        } else {
            std::fill(state_.begin(), state_.end(), 0.0f);
        }
        std::fill(context_.begin(), context_.end(), 0.0f);
    }

    bool infer(const int16_t* pcm, float* probability, std::string* err) {
        std::fill(input_.begin(), input_.end(), 0.0f);
        if (feature_input_) {
            compute_stft_features(pcm);
        } else {
            uint32_t offset = 0;
            if (input_.size() >= kFrameSamples + context_.size()) {
                for (uint32_t i = 0; i < context_.size(); ++i) {
                    input_[i] = context_[i];
                }
                offset = static_cast<uint32_t>(context_.size());
            }
            for (int i = 0; i < kFrameSamples; ++i) {
                input_[offset + i] = static_cast<float>(pcm[i]) / 32768.0f;
            }
        }

        int64_t sr = kSampleRate;

        if (!write_float_input(input_index_, input_, &input_bytes_, err)) {
            return false;
        }

        if (split_state_) {
            if (!write_float_input(h_input_index_, h_, &h_bytes_, err)) {
                return false;
            }
            if (!write_float_input(c_input_index_, c_, &c_bytes_, err)) {
                return false;
            }
        } else if (!write_float_input(state_input_index_, state_, &state_bytes_, err)) {
            return false;
        }

        if (!split_state_ && sr_index_ != kMissingTensorIndex) {
            if (!write_int64_input(sr_index_, sr, &sr_bytes_, err)) {
                return false;
            }
        }

        int ret = rknn_run(ctx_, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "rknn_run failed: " + std::to_string(ret);
            return false;
        }

        if (!copy_outputs(probability, err)) {
            return false;
        }
        update_context(pcm);
        return true;
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

    bool write_float_input(uint32_t index,
                           const std::vector<float>& values,
                           std::vector<uint8_t>* bytes,
                           std::string* err) const {
        if (index >= input_attrs_.size()) {
            *err = "RKNN input index is out of range: " + std::to_string(index);
            return false;
        }
        const rknn_tensor_attr& attr = input_attrs_[index];
        if (!pack_float_tensor(attr, values.data(), values.size(), bytes, err)) {
            return false;
        }
        return copy_to_input_mem(index, *bytes, err);
    }

    bool write_int64_input(uint32_t index,
                           int64_t value,
                           std::vector<uint8_t>* bytes,
                           std::string* err) const {
        if (index >= input_attrs_.size()) {
            *err = "RKNN input index is out of range: " + std::to_string(index);
            return false;
        }
        const rknn_tensor_attr& attr = input_attrs_[index];
        if (!pack_int64_tensor(attr, value, bytes, err)) {
            return false;
        }
        return copy_to_input_mem(index, *bytes, err);
    }

    bool pack_float_tensor(const rknn_tensor_attr& attr,
                           const float* values,
                           std::size_t value_count,
                           std::vector<uint8_t>* bytes,
                           std::string* err) const {
        if (value_count < attr.n_elems) {
            *err = "RKNN input " + std::string(attr.name) + " has too few values: got " +
                   std::to_string(value_count) + ", want " + std::to_string(attr.n_elems);
            return false;
        }

        const uint32_t elem_bytes = tensor_type_bytes(attr.type);
        const uint32_t storage_bytes = tensor_storage_bytes(attr);
        if (elem_bytes == 0 || storage_bytes < attr.n_elems * elem_bytes) {
            *err = "RKNN input " + std::string(attr.name) + " uses unsupported tensor type " +
                   tensor_type_name(attr.type);
            return false;
        }

        bytes->assign(storage_bytes, 0);
        for (uint32_t i = 0; i < attr.n_elems; ++i) {
            const float value = values[i];
            switch (attr.type) {
            case RKNN_TENSOR_FLOAT32:
                write_pod<float>(bytes, i, value);
                break;
            case RKNN_TENSOR_FLOAT16:
                write_pod<uint16_t>(bytes, i, float_to_half(value));
                break;
            case RKNN_TENSOR_INT8: {
                const int32_t q = clamp_int32(quantized_from_float(value, attr),
                                              std::numeric_limits<int8_t>::min(),
                                              std::numeric_limits<int8_t>::max());
                (*bytes)[i] = static_cast<uint8_t>(static_cast<int8_t>(q));
                break;
            }
            case RKNN_TENSOR_UINT8: {
                const int32_t q = clamp_int32(quantized_from_float(value, attr),
                                              std::numeric_limits<uint8_t>::min(),
                                              std::numeric_limits<uint8_t>::max());
                (*bytes)[i] = static_cast<uint8_t>(q);
                break;
            }
            case RKNN_TENSOR_INT16: {
                const int32_t q = clamp_int32(quantized_from_float(value, attr),
                                              std::numeric_limits<int16_t>::min(),
                                              std::numeric_limits<int16_t>::max());
                write_pod<int16_t>(bytes, i, static_cast<int16_t>(q));
                break;
            }
            case RKNN_TENSOR_UINT16: {
                const int32_t q = clamp_int32(quantized_from_float(value, attr),
                                              std::numeric_limits<uint16_t>::min(),
                                              std::numeric_limits<uint16_t>::max());
                write_pod<uint16_t>(bytes, i, static_cast<uint16_t>(q));
                break;
            }
            case RKNN_TENSOR_INT32:
                write_pod<int32_t>(bytes, i, quantized_from_float(value, attr));
                break;
            case RKNN_TENSOR_UINT32: {
                const double rounded = std::nearbyint(value);
                const double limited = std::max(0.0, std::min(rounded, static_cast<double>(std::numeric_limits<uint32_t>::max())));
                write_pod<uint32_t>(bytes, i, static_cast<uint32_t>(limited));
                break;
            }
            default:
                *err = "RKNN input " + std::string(attr.name) + " uses unsupported tensor type " +
                       tensor_type_name(attr.type);
                return false;
            }
        }
        return true;
    }

    bool pack_int64_tensor(const rknn_tensor_attr& attr,
                           int64_t value,
                           std::vector<uint8_t>* bytes,
                           std::string* err) const {
        if (attr.n_elems != 1) {
            *err = "RKNN scalar input " + std::string(attr.name) + " has unexpected element count " +
                   std::to_string(attr.n_elems);
            return false;
        }
        const uint32_t elem_bytes = tensor_type_bytes(attr.type);
        const uint32_t storage_bytes = tensor_storage_bytes(attr);
        if (elem_bytes == 0 || storage_bytes < elem_bytes) {
            *err = "RKNN scalar input " + std::string(attr.name) + " uses unsupported tensor type " +
                   tensor_type_name(attr.type);
            return false;
        }

        bytes->assign(storage_bytes, 0);
        switch (attr.type) {
        case RKNN_TENSOR_INT64:
            write_pod<int64_t>(bytes, 0, value);
            return true;
        case RKNN_TENSOR_INT32:
            write_pod<int32_t>(bytes, 0, static_cast<int32_t>(value));
            return true;
        default:
            *err = "RKNN scalar input " + std::string(attr.name) + " uses unsupported tensor type " +
                   tensor_type_name(attr.type);
            return false;
        }
    }

    bool copy_outputs(float* probability, std::string* err) {
        const void* probability_buffer = output_buffer(output_index_);
        if (probability_buffer == nullptr) {
            *err = "RKNN returned a null output buffer";
            return false;
        }

        if (!read_tensor_float(output_attrs_[output_index_], probability_buffer, 0, probability, err)) {
            return false;
        }

        if (split_state_) {
            return copy_state_output(h_output_index_, &h_, err) &&
                   copy_state_output(c_output_index_, &c_, err);
        }
        return copy_state_output(state_output_index_, &state_, err);
    }

    bool copy_state_output(uint32_t output_index, std::vector<float>* state, std::string* err) {
        const void* state_buffer = output_buffer(output_index);
        if (state_buffer == nullptr) {
            *err = "RKNN returned a null state output buffer";
            return false;
        }

        const rknn_tensor_attr& state_attr = output_attrs_[output_index];
        if (state_attr.n_elems > 0 && state_attr.n_elems < state->size()) {
            *err = "Silero VAD state output " + std::string(state_attr.name) +
                   " is smaller than expected";
            return false;
        }

        for (uint32_t i = 0; i < state->size(); ++i) {
            if (!read_tensor_float(state_attr, state_buffer, i, &(*state)[i], err)) {
                return false;
            }
        }
        return true;
    }

    bool read_tensor_float(const rknn_tensor_attr& attr,
                           const void* buffer,
                           uint32_t index,
                           float* value,
                           std::string* err) const {
        if (index >= attr.n_elems) {
            *err = "RKNN output " + std::string(attr.name) + " index is out of range";
            return false;
        }

        const uint8_t* bytes = static_cast<const uint8_t*>(buffer);
        switch (attr.type) {
        case RKNN_TENSOR_FLOAT32:
            *value = read_pod<float>(bytes, index);
            return true;
        case RKNN_TENSOR_FLOAT16:
            *value = half_to_float(read_pod<uint16_t>(bytes, index));
            return true;
        case RKNN_TENSOR_INT8:
            *value = float_from_quantized(static_cast<int8_t>(bytes[index]), attr);
            return true;
        case RKNN_TENSOR_UINT8:
            *value = float_from_quantized(bytes[index], attr);
            return true;
        case RKNN_TENSOR_INT16:
            *value = float_from_quantized(read_pod<int16_t>(bytes, index), attr);
            return true;
        case RKNN_TENSOR_UINT16:
            *value = float_from_quantized(read_pod<uint16_t>(bytes, index), attr);
            return true;
        case RKNN_TENSOR_INT32:
            *value = float_from_quantized(read_pod<int32_t>(bytes, index), attr);
            return true;
        case RKNN_TENSOR_UINT32:
            *value = static_cast<float>(read_pod<uint32_t>(bytes, index));
            return true;
        default:
            *err = "RKNN output " + std::string(attr.name) + " uses unsupported tensor type " +
                   tensor_type_name(attr.type);
            return false;
        }
    }

    bool setup_io_mems(std::string* err) {
        if (!create_and_bind_io_mems(&input_attrs_, &input_mems_, "input", true, err)) {
            return false;
        }
        return create_and_bind_io_mems(&output_attrs_, &output_mems_, "output", false, err);
    }

    bool create_and_bind_io_mems(std::vector<rknn_tensor_attr>* attrs,
                                 std::vector<rknn_tensor_mem*>* mems,
                                 const char* label,
                                 bool input,
                                 std::string* err) {
        mems->assign(attrs->size(), nullptr);
        for (uint32_t i = 0; i < attrs->size(); ++i) {
            rknn_tensor_attr& attr = (*attrs)[i];
            attr.index = i;
            if (input) {
                attr.pass_through = 1;
            }
            const uint32_t storage_bytes = tensor_storage_bytes(attr);
            if (storage_bytes == 0) {
                *err = "RKNN " + std::string(label) + " " + std::string(attr.name) +
                       " has no storage size";
                return false;
            }

            rknn_tensor_mem* mem = rknn_create_mem(ctx_, storage_bytes);
            if (mem == nullptr) {
                *err = "rknn_create_mem failed for " + std::string(label) + " " +
                       std::string(attr.name) + " (" + std::to_string(storage_bytes) + " bytes)";
                return false;
            }
            if (mem->virt_addr == nullptr || mem->size < storage_bytes) {
                rknn_destroy_mem(ctx_, mem);
                *err = "rknn_create_mem returned invalid memory for " + std::string(label) + " " +
                       std::string(attr.name);
                return false;
            }
            std::memset(mem->virt_addr, 0, mem->size);

            if (!bind_io_mem(mem, &attr, label, err)) {
                rknn_destroy_mem(ctx_, mem);
                return false;
            }
            (*mems)[i] = mem;
        }
        return true;
    }

    bool bind_io_mem(rknn_tensor_mem* mem,
                     rknn_tensor_attr* attr,
                     const char* label,
                     std::string* err) const {
        const int ret = rknn_set_io_mem(ctx_, mem, attr);
        if (ret == RKNN_SUCC) {
            return true;
        }

        rknn_tensor_attr retry_attr = *attr;
        retry_attr.pass_through = attr->pass_through == 0 ? 1 : 0;
        const int retry_ret = rknn_set_io_mem(ctx_, mem, &retry_attr);
        if (retry_ret == RKNN_SUCC) {
            *attr = retry_attr;
            std::cerr << "[rknn_vad] " << label << "[" << attr->index << "] name="
                      << attr->name << " bound with pass_through="
                      << static_cast<int>(attr->pass_through) << std::endl;
            return true;
        }

        if (attr->pass_through == 0) {
            *err = "rknn_set_io_mem failed for " + std::string(label) + " " +
                   std::string(attr->name) + ": " + std::to_string(ret) +
                   "; pass-through retry failed: " + std::to_string(retry_ret);
        } else {
            *err = "rknn_set_io_mem failed for " + std::string(label) + " " +
                   std::string(attr->name) + ": " + std::to_string(ret) +
                   "; non-pass-through retry failed: " + std::to_string(retry_ret);
        }
        return false;
    }

    bool copy_to_input_mem(uint32_t index, const std::vector<uint8_t>& bytes, std::string* err) const {
        if (index >= input_mems_.size() || input_mems_[index] == nullptr) {
            *err = "RKNN input memory is missing at index " + std::to_string(index);
            return false;
        }
        rknn_tensor_mem* mem = input_mems_[index];
        if (mem->virt_addr == nullptr) {
            *err = "RKNN input memory has null virtual address at index " + std::to_string(index);
            return false;
        }
        if (bytes.size() > mem->size) {
            *err = "RKNN input " + std::string(input_attrs_[index].name) + " buffer is too large: got " +
                   std::to_string(bytes.size()) + ", memory has " + std::to_string(mem->size);
            return false;
        }
        std::memset(mem->virt_addr, 0, mem->size);
        if (!bytes.empty()) {
            std::memcpy(mem->virt_addr, bytes.data(), bytes.size());
        }
        return true;
    }

    const void* output_buffer(uint32_t index) const {
        if (index >= output_mems_.size() || output_mems_[index] == nullptr) {
            return nullptr;
        }
        return output_mems_[index]->virt_addr;
    }

    void release_mems() {
        for (std::vector<rknn_tensor_mem*>::iterator it = input_mems_.begin(); it != input_mems_.end(); ++it) {
            if (*it != nullptr) {
                rknn_destroy_mem(ctx_, *it);
                *it = nullptr;
            }
        }
        for (std::vector<rknn_tensor_mem*>::iterator it = output_mems_.begin(); it != output_mems_.end(); ++it) {
            if (*it != nullptr) {
                rknn_destroy_mem(ctx_, *it);
                *it = nullptr;
            }
        }
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
        const int audio_samples = kFrameSamples + static_cast<int>(context_.size());
        std::vector<float> padded(audio_samples + kSTFTSize / 4, 0.0f);
        int write_index = 0;
        for (uint32_t i = 0; i < context_.size(); ++i) {
            padded[write_index++] = context_[i];
        }
        for (int i = 0; i < kFrameSamples; ++i) {
            padded[write_index++] = static_cast<float>(pcm[i]) / 32768.0f;
        }
        for (int i = 0; i < kSTFTSize / 4; ++i) {
            padded[write_index + i] = padded[audio_samples - 2 - i];
        }

        for (uint32_t frame = 0; frame < feature_frames_; ++frame) {
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
                input_[freq * feature_frames_ + frame] = std::sqrt(real * real + imag * imag);
            }
        }
    }

    void update_context(const int16_t* pcm) {
        if (context_.empty()) {
            return;
        }
        const int start = kFrameSamples - static_cast<int>(context_.size());
        for (uint32_t i = 0; i < context_.size(); ++i) {
            context_[i] = static_cast<float>(pcm[start + static_cast<int>(i)]) / 32768.0f;
        }
    }

    void log_sdk_version() const {
        rknn_sdk_version version;
        std::memset(&version, 0, sizeof(version));
        if (rknn_query(ctx_, RKNN_QUERY_SDK_VERSION, &version, sizeof(version)) == RKNN_SUCC) {
            std::cerr << "[rknn_vad] api_version=" << version.api_version
                      << " drv_version=" << version.drv_version << std::endl;
        }
    }

    void log_attr(const char* label, const rknn_tensor_attr& attr) const {
        std::cerr << "[rknn_vad] " << label
                  << "[" << attr.index << "] name=" << attr.name
                  << " dims=" << dims_string(attr)
                  << " elems=" << attr.n_elems
                  << " size=" << attr.size
                  << " stride_size=" << attr.size_with_stride
                  << " fmt=" << tensor_format_name(attr.fmt)
                  << " type=" << tensor_type_name(attr.type)
                  << " qnt=" << tensor_qnt_type_name(attr.qnt_type)
                  << " zp=" << attr.zp
                  << " scale=" << attr.scale
                  << " fl=" << static_cast<int>(attr.fl)
                  << " pass_through=" << static_cast<int>(attr.pass_through)
                  << std::endl;
    }

    rknn_context ctx_ = 0;
    std::vector<rknn_tensor_attr> input_attrs_;
    std::vector<rknn_tensor_attr> output_attrs_;
    std::vector<rknn_tensor_mem*> input_mems_;
    std::vector<rknn_tensor_mem*> output_mems_;
    uint32_t input_index_ = 0;
    uint32_t state_input_index_ = 1;
    uint32_t h_input_index_ = 1;
    uint32_t c_input_index_ = 2;
    uint32_t sr_index_ = 2;
    uint32_t output_index_ = 0;
    uint32_t state_output_index_ = 1;
    uint32_t h_output_index_ = 1;
    uint32_t c_output_index_ = 2;
    bool split_state_ = false;
    bool feature_input_ = false;
    uint32_t feature_frames_ = 0;
    std::vector<float> input_;
    std::vector<float> state_ = std::vector<float>(kStateFloats, 0.0f);
    std::vector<float> h_;
    std::vector<float> c_;
    std::vector<float> context_;
    std::vector<uint8_t> input_bytes_;
    std::vector<uint8_t> state_bytes_;
    std::vector<uint8_t> h_bytes_;
    std::vector<uint8_t> c_bytes_;
    std::vector<uint8_t> sr_bytes_;
    std::vector<float> stft_real_;
    std::vector<float> stft_imag_;
};

class SplitSileroVAD {
public:
    ~SplitSileroVAD() {
        if (encoder_ctx_ != 0) {
            if (input_mem_) rknn_destroy_mem(encoder_ctx_, input_mem_);
            if (output_mem_) rknn_destroy_mem(encoder_ctx_, output_mem_);
            rknn_destroy(encoder_ctx_);
        }
    }

    bool init(const std::string& encoder_path, const std::string& weights_path, std::string* err) {
        if (!load_weights(weights_path, err)) return false;
        if (!init_encoder(encoder_path, err)) return false;
        prepare_stft_basis();
        reset();
        return true;
    }

    void reset() {
        std::memset(h_, 0, sizeof(h_));
        std::memset(c_, 0, sizeof(c_));
        std::memset(context_, 0, sizeof(context_));
    }

    bool infer(const int16_t* pcm, float* probability, std::string* err) {
        compute_stft_features(pcm);
        if (!run_encoder(err)) return false;
        lstm_cell();
        *probability = decoder();
        update_context(pcm);
        return true;
    }

private:
    static constexpr int kHidden = 128;
    static constexpr int kGates = 4 * kHidden;

    bool load_weights(const std::string& path, std::string* err) {
        std::ifstream file(path, std::ios::binary);
        if (!file) { *err = "cannot open weights: " + path; return false; }
        char magic[4];
        file.read(magic, 4);
        if (std::memcmp(magic, "SVLW", 4) != 0) {
            *err = "invalid weights magic"; return false;
        }
        uint32_t version, hidden, input_sz;
        file.read(reinterpret_cast<char*>(&version), 4);
        file.read(reinterpret_cast<char*>(&hidden), 4);
        file.read(reinterpret_cast<char*>(&input_sz), 4);
        if (hidden != kHidden || input_sz != kHidden) {
            *err = "weights size mismatch"; return false;
        }
        file.read(reinterpret_cast<char*>(lstm_W_), sizeof(lstm_W_));
        file.read(reinterpret_cast<char*>(lstm_R_), sizeof(lstm_R_));
        file.read(reinterpret_cast<char*>(lstm_B_), sizeof(lstm_B_));
        file.read(reinterpret_cast<char*>(dec_weight_), sizeof(dec_weight_));
        file.read(reinterpret_cast<char*>(&dec_bias_), sizeof(dec_bias_));
        if (!file) { *err = "weights file truncated"; return false; }
        return true;
    }

    bool init_encoder(const std::string& path, std::string* err) {
        int ret = rknn_init(&encoder_ctx_, const_cast<char*>(path.c_str()), 0, 0, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "encoder rknn_init failed: " + std::to_string(ret);
            return false;
        }
        rknn_input_output_num io_num;
        std::memset(&io_num, 0, sizeof(io_num));
        rknn_query(encoder_ctx_, RKNN_QUERY_IN_OUT_NUM, &io_num, sizeof(io_num));
        if (io_num.n_input < 1 || io_num.n_output < 1) {
            *err = "encoder model must have at least 1 input and 1 output";
            return false;
        }
        std::memset(&input_attr_, 0, sizeof(input_attr_));
        input_attr_.index = 0;
        rknn_query(encoder_ctx_, RKNN_QUERY_NATIVE_INPUT_ATTR, &input_attr_, sizeof(input_attr_));
        std::memset(&output_attr_, 0, sizeof(output_attr_));
        output_attr_.index = 0;
        rknn_query(encoder_ctx_, RKNN_QUERY_OUTPUT_ATTR, &output_attr_, sizeof(output_attr_));

        std::cerr << "[split_vad] input: elems=" << input_attr_.n_elems
                  << " type=" << tensor_type_name(input_attr_.type)
                  << " scale=" << input_attr_.scale
                  << " zp=" << input_attr_.zp << std::endl;
        std::cerr << "[split_vad] output: elems=" << output_attr_.n_elems
                  << " type=" << tensor_type_name(output_attr_.type)
                  << " scale=" << output_attr_.scale
                  << " zp=" << output_attr_.zp << std::endl;

        input_attr_.pass_through = 1;
        uint32_t in_bytes = tensor_storage_bytes(input_attr_);
        input_mem_ = rknn_create_mem(encoder_ctx_, in_bytes);
        if (!input_mem_ || !input_mem_->virt_addr) {
            *err = "encoder input mem alloc failed"; return false;
        }
        ret = rknn_set_io_mem(encoder_ctx_, input_mem_, &input_attr_);
        if (ret != RKNN_SUCC) {
            *err = "encoder set input mem failed: " + std::to_string(ret);
            return false;
        }
        uint32_t out_bytes = tensor_storage_bytes(output_attr_);
        output_mem_ = rknn_create_mem(encoder_ctx_, out_bytes);
        if (!output_mem_ || !output_mem_->virt_addr) {
            *err = "encoder output mem alloc failed"; return false;
        }
        ret = rknn_set_io_mem(encoder_ctx_, output_mem_, &output_attr_);
        if (ret != RKNN_SUCC) {
            *err = "encoder set output mem failed: " + std::to_string(ret);
            return false;
        }
        return true;
    }

    bool run_encoder(std::string* err) {
        uint8_t* dst = static_cast<uint8_t*>(input_mem_->virt_addr);
        for (int i = 0; i < kSTFTBins * kFeatureFrames; ++i) {
            int32_t q = static_cast<int32_t>(
                std::nearbyint(features_[i] / input_attr_.scale + input_attr_.zp));
            if (q < -128) q = -128;
            if (q > 127) q = 127;
            dst[i] = static_cast<uint8_t>(static_cast<int8_t>(q));
        }
        int ret = rknn_run(encoder_ctx_, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "encoder rknn_run failed: " + std::to_string(ret);
            return false;
        }
        const uint8_t* src = static_cast<const uint8_t*>(output_mem_->virt_addr);
        for (int i = 0; i < kHidden; ++i) {
            int8_t raw = static_cast<int8_t>(src[i]);
            encoder_out_[i] = (static_cast<float>(raw) - output_attr_.zp) * output_attr_.scale;
        }
        return true;
    }

    void lstm_cell() {
        float gates[kGates];
        for (int g = 0; g < kGates; ++g) {
            float sum = lstm_B_[g] + lstm_B_[kGates + g];
            const float* w_row = &lstm_W_[g * kHidden];
            const float* r_row = &lstm_R_[g * kHidden];
            for (int k = 0; k < kHidden; ++k) {
                sum += w_row[k] * encoder_out_[k] + r_row[k] * h_[k];
            }
            gates[g] = sum;
        }
        for (int i = 0; i < kHidden; ++i) {
            float ig = 1.0f / (1.0f + std::exp(-gates[i]));
            float og = 1.0f / (1.0f + std::exp(-gates[kHidden + i]));
            float fg = 1.0f / (1.0f + std::exp(-gates[2 * kHidden + i]));
            float cg = std::tanh(gates[3 * kHidden + i]);
            c_[i] = fg * c_[i] + ig * cg;
            h_[i] = og * std::tanh(c_[i]);
        }
    }

    float decoder() {
        float logit = dec_bias_;
        for (int i = 0; i < kHidden; ++i) {
            float relu_h = h_[i] > 0.0f ? h_[i] : 0.0f;
            logit += dec_weight_[i] * relu_h;
        }
        return 1.0f / (1.0f + std::exp(-logit));
    }

    void prepare_stft_basis() {
        const float pi = 3.14159265358979323846f;
        for (int freq = 0; freq < kSTFTBins; ++freq) {
            for (int n = 0; n < kSTFTSize; ++n) {
                float window = 0.5f - 0.5f * std::cos((2.0f * pi * n) / kSTFTSize);
                float angle = (2.0f * pi * freq * n) / kSTFTSize;
                stft_real_[freq * kSTFTSize + n] = window * std::cos(angle);
                stft_imag_[freq * kSTFTSize + n] = window * -std::sin(angle);
            }
        }
    }

    void compute_stft_features(const int16_t* pcm) {
        const int audio_samples = kFrameSamples + kSileroContextSamples;
        float padded[audio_samples + kSTFTSize / 4];
        int wi = 0;
        for (int i = 0; i < kSileroContextSamples; ++i)
            padded[wi++] = context_[i];
        for (int i = 0; i < kFrameSamples; ++i)
            padded[wi++] = static_cast<float>(pcm[i]) / 32768.0f;
        for (int i = 0; i < kSTFTSize / 4; ++i)
            padded[wi + i] = padded[audio_samples - 2 - i];

        for (int frame = 0; frame < kFeatureFrames; ++frame) {
            const int offset = frame * kSTFTHop;
            for (int freq = 0; freq < kSTFTBins; ++freq) {
                float real = 0.0f, imag = 0.0f;
                const float* rb = &stft_real_[freq * kSTFTSize];
                const float* ib = &stft_imag_[freq * kSTFTSize];
                for (int n = 0; n < kSTFTSize; ++n) {
                    float s = padded[offset + n];
                    real += s * rb[n];
                    imag += s * ib[n];
                }
                features_[freq * kFeatureFrames + frame] = std::sqrt(real * real + imag * imag);
            }
        }
    }

    void update_context(const int16_t* pcm) {
        const int start = kFrameSamples - kSileroContextSamples;
        for (int i = 0; i < kSileroContextSamples; ++i)
            context_[i] = static_cast<float>(pcm[start + i]) / 32768.0f;
    }

    static constexpr int kFeatureFrames = 4;
    rknn_context encoder_ctx_ = 0;
    rknn_tensor_attr input_attr_;
    rknn_tensor_attr output_attr_;
    rknn_tensor_mem* input_mem_ = nullptr;
    rknn_tensor_mem* output_mem_ = nullptr;

    float lstm_W_[kGates * kHidden];
    float lstm_R_[kGates * kHidden];
    float lstm_B_[2 * kGates];
    float dec_weight_[kHidden];
    float dec_bias_ = 0.0f;

    float h_[kHidden];
    float c_[kHidden];
    float context_[kSileroContextSamples];
    float features_[kSTFTBins * kFeatureFrames];
    float encoder_out_[kHidden];
    float stft_real_[kSTFTBins * kSTFTSize];
    float stft_imag_[kSTFTBins * kSTFTSize];
};

struct Args {
    std::string model_path;
    std::string weights_path;
    bool self_test = false;
};

Args parse_args(int argc, char** argv) {
    Args args;
    for (int i = 1; i < argc; ++i) {
        const std::string arg(argv[i]);
        if (arg == "--model" && i + 1 < argc) {
            args.model_path = argv[++i];
        } else if (arg == "--weights" && i + 1 < argc) {
            args.weights_path = argv[++i];
        } else if (arg == "--self-test") {
            args.self_test = true;
        }
    }
    return args;
}

}  // namespace

int main(int argc, char** argv) {
    Args args = parse_args(argc, argv);
    if (args.model_path.empty()) {
        protocol_error("missing --model path");
        return 2;
    }

    std::string err;
    std::vector<int16_t> frame(kFrameSamples);

    if (!args.weights_path.empty()) {
        SplitSileroVAD vad;
        if (!vad.init(args.model_path, args.weights_path, &err)) {
            protocol_error(err);
            return 1;
        }
        if (args.self_test) {
            std::fill(frame.begin(), frame.end(), 0);
            float probability = 0.0f;
            if (!vad.infer(frame.data(), &probability, &err)) {
                protocol_error(err);
                return 1;
            }
            std::cout << "P " << probability << std::endl;
            return 0;
        }
        std::cout << "READY" << std::endl;
        while (true) {
            char command = 0;
            if (!read_exact(std::cin, &command, 1)) return 0;
            if (command == 'Q') return 0;
            if (command == 'R') { vad.reset(); std::cout << "OK" << std::endl; continue; }
            if (command != 'F') { protocol_error("unknown command"); continue; }
            if (!read_exact(std::cin, reinterpret_cast<char*>(frame.data()), frame.size() * sizeof(int16_t)))
                return 0;
            float probability = 0.0f;
            if (!vad.infer(frame.data(), &probability, &err)) {
                protocol_error(err);
                continue;
            }
            std::cout << "P " << probability << std::endl;
        }
    }

    SileroRKNNVAD vad;
    if (!vad.init(args.model_path, &err)) {
        protocol_error(err);
        return 1;
    }

    if (args.self_test) {
        std::fill(frame.begin(), frame.end(), 0);
        float probability = 0.0f;
        if (!vad.infer(frame.data(), &probability, &err)) {
            protocol_error(err);
            return 1;
        }
        std::cout << "P " << probability << std::endl;
        return 0;
    }

    std::cout << "READY" << std::endl;
    while (true) {
        char command = 0;
        if (!read_exact(std::cin, &command, 1)) return 0;
        if (command == 'Q') return 0;
        if (command == 'R') { vad.reset(); std::cout << "OK" << std::endl; continue; }
        if (command != 'F') { protocol_error("unknown command"); continue; }
        if (!read_exact(std::cin, reinterpret_cast<char*>(frame.data()), frame.size() * sizeof(int16_t)))
            return 0;
        float probability = 0.0f;
        if (!vad.infer(frame.data(), &probability, &err)) {
            protocol_error(err);
            continue;
        }
        std::cout << "P " << probability << std::endl;
    }
}
