#ifndef AIDEN_RKNN_API_MINIMAL_H
#define AIDEN_RKNN_API_MINIMAL_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define RKNN_SUCC 0
#define RKNN_MAX_DIMS 16
#define RKNN_MAX_NAME_LEN 256

#ifdef __arm__
typedef uint32_t rknn_context;
#else
typedef uint64_t rknn_context;
#endif

typedef enum _rknn_query_cmd {
    RKNN_QUERY_IN_OUT_NUM = 0,
    RKNN_QUERY_INPUT_ATTR = 1,
    RKNN_QUERY_OUTPUT_ATTR = 2,
    RKNN_QUERY_NATIVE_INPUT_ATTR = 8,
    RKNN_QUERY_NATIVE_OUTPUT_ATTR = 9,
} rknn_query_cmd;

typedef enum _rknn_tensor_type {
    RKNN_TENSOR_FLOAT32 = 0,
    RKNN_TENSOR_FLOAT16 = 1,
    RKNN_TENSOR_INT8 = 2,
    RKNN_TENSOR_UINT8 = 3,
    RKNN_TENSOR_INT16 = 4,
    RKNN_TENSOR_UINT16 = 5,
    RKNN_TENSOR_INT32 = 6,
    RKNN_TENSOR_UINT32 = 7,
    RKNN_TENSOR_INT64 = 8,
    RKNN_TENSOR_BOOL = 9,
    RKNN_TENSOR_INT4 = 10,
    RKNN_TENSOR_BFLOAT16 = 11,
} rknn_tensor_type;

typedef enum _rknn_tensor_qnt_type {
    RKNN_TENSOR_QNT_NONE = 0,
    RKNN_TENSOR_QNT_DFP = 1,
    RKNN_TENSOR_QNT_AFFINE_ASYMMETRIC = 2,
} rknn_tensor_qnt_type;

typedef enum _rknn_tensor_format {
    RKNN_TENSOR_NCHW = 0,
    RKNN_TENSOR_NHWC = 1,
    RKNN_TENSOR_NC1HWC2 = 2,
    RKNN_TENSOR_UNDEFINED = 3,
} rknn_tensor_format;

typedef struct _rknn_input_output_num {
    uint32_t n_input;
    uint32_t n_output;
} rknn_input_output_num;

typedef struct _rknn_tensor_attr {
    uint32_t index;
    uint32_t n_dims;
    uint32_t dims[RKNN_MAX_DIMS];
    char name[RKNN_MAX_NAME_LEN];
    uint32_t n_elems;
    uint32_t size;
    rknn_tensor_format fmt;
    rknn_tensor_type type;
    rknn_tensor_qnt_type qnt_type;
    int8_t fl;
    int32_t zp;
    float scale;
    uint32_t w_stride;
    uint32_t size_with_stride;
    uint8_t pass_through;
    uint32_t h_stride;
} rknn_tensor_attr;

typedef struct _rknn_input {
    uint32_t index;
    void* buf;
    uint32_t size;
    uint8_t pass_through;
    rknn_tensor_type type;
    rknn_tensor_format fmt;
} rknn_input;

typedef struct _rknn_output {
    uint8_t want_float;
    uint8_t is_prealloc;
    uint32_t index;
    void* buf;
    uint32_t size;
} rknn_output;

int rknn_init(rknn_context* context, void* model, uint32_t size, uint32_t flag, void* extend);
int rknn_destroy(rknn_context context);
int rknn_query(rknn_context context, rknn_query_cmd cmd, void* info, uint32_t size);
int rknn_inputs_set(rknn_context context, uint32_t n_inputs, rknn_input inputs[]);
int rknn_run(rknn_context context, void* extend);
int rknn_outputs_get(rknn_context context, uint32_t n_outputs, rknn_output outputs[], void* extend);
int rknn_outputs_release(rknn_context context, uint32_t n_outputs, rknn_output outputs[]);

#ifdef __cplusplus
}
#endif

#endif
