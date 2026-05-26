#include "rknn_api_minimal.h"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstring>
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
            ret = rknn_query(ctx_, RKNN_QUERY_NATIVE_OUTPUT_ATTR, &output_attrs_[i], sizeof(rknn_tensor_attr));
            if (ret != RKNN_SUCC) {
                *err = "rknn_query native output attr failed at index " + std::to_string(i) + ": " + std::to_string(ret);
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
        if (state_input_index_ >= input_attrs_.size()) {
            *err = "Silero VAD state input is missing";
            return false;
        }
        if (output_index_ >= output_attrs_.size() || state_output_index_ >= output_attrs_.size()) {
            *err = "Silero VAD outputs are missing";
            return false;
        }
        feature_input_ = input_attrs_[input_index_].n_elems == kFeatureFloats;
        if (!feature_input_ && input_attrs_[input_index_].n_elems < kMinInputFloats) {
            *err = "Silero VAD audio input is smaller than one frame";
            return false;
        }
        if (input_attrs_[state_input_index_].n_elems == 0) {
            *err = "Silero VAD state input is empty";
            return false;
        }
        input_.assign(input_attrs_[input_index_].n_elems, 0.0f);
        state_.assign(input_attrs_[state_input_index_].n_elems, 0.0f);
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

        if (!write_float_input(input_index_, input_, &input_bytes_, err)) {
            return false;
        }

        if (!write_float_input(state_input_index_, state_, &state_bytes_, err)) {
            return false;
        }

        if (sr_index_ != kMissingTensorIndex) {
            if (!write_int64_input(sr_index_, sr, &sr_bytes_, err)) {
                return false;
            }
        }

        int ret = rknn_run(ctx_, nullptr);
        if (ret != RKNN_SUCC) {
            *err = "rknn_run failed: " + std::to_string(ret);
            return false;
        }

        return copy_outputs(output_buffer(output_index_), output_buffer(state_output_index_), probability, err);
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

    bool copy_outputs(const void* probability_buffer,
                      const void* state_buffer,
                      float* probability,
                      std::string* err) {
        if (probability_buffer == nullptr || state_buffer == nullptr) {
            *err = "RKNN returned a null output buffer";
            return false;
        }

        if (!read_tensor_float(output_attrs_[output_index_], probability_buffer, 0, probability, err)) {
            return false;
        }

        if (state_output_index_ < output_attrs_.size() &&
            output_attrs_[state_output_index_].n_elems > 0 &&
            output_attrs_[state_output_index_].n_elems < state_.size()) {
            *err = "Silero VAD state outputs are smaller than expected";
            return false;
        }

        const rknn_tensor_attr& state_attr = output_attrs_[state_output_index_];
        for (uint32_t i = 0; i < state_.size(); ++i) {
            if (!read_tensor_float(state_attr, state_buffer, i, &state_[i], err)) {
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
    uint32_t sr_index_ = 2;
    uint32_t output_index_ = 0;
    uint32_t state_output_index_ = 1;
    bool feature_input_ = false;
    std::vector<float> input_;
    std::vector<float> state_ = std::vector<float>(kStateFloats, 0.0f);
    std::vector<uint8_t> input_bytes_;
    std::vector<uint8_t> state_bytes_;
    std::vector<uint8_t> sr_bytes_;
    std::vector<float> stft_real_;
    std::vector<float> stft_imag_;
};

struct Args {
    std::string model_path;
    bool self_test = false;
};

Args parse_args(int argc, char** argv) {
    Args args;
    for (int i = 1; i < argc; ++i) {
        const std::string arg(argv[i]);
        if (arg == "--model" && i + 1 < argc) {
            args.model_path = argv[++i];
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

    SileroRKNNVAD vad;
    std::string err;
    if (!vad.init(args.model_path, &err)) {
        protocol_error(err);
        return 1;
    }

    if (args.self_test) {
        std::vector<int16_t> frame(kFrameSamples, 0);
        float probability = 0.0f;
        if (!vad.infer(frame.data(), &probability, &err)) {
            protocol_error(err);
            return 1;
        }
        std::cout << "P " << probability << std::endl;
        return 0;
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
