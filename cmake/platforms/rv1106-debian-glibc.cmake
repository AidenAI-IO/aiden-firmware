set(AIDEN_DEBIAN_VENDOR_LIB_DIR
    "${SDK_PATH}/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-glibc-rockchip/usr/lib"
    CACHE PATH "Rockchip RV1106 glibc library directory")
set(AIDEN_DEBIAN_OPENCV_DIR
    "${CMAKE_SOURCE_DIR}/output/debian-stage2/opencv-mobile/lib/cmake/opencv4"
    CACHE PATH "Debian armhf OpenCV CMake package path")
set(AIDEN_RKNPU2_ROOT
    "${CMAKE_SOURCE_DIR}/third_party/rknpu2/v2.3.2"
    CACHE PATH "Rockchip RKNPU2 armhf runtime root")

set(AIDEN_OPENCV_DIR "${AIDEN_DEBIAN_OPENCV_DIR}")
set(AIDEN_ROCKIT_INCLUDE_DIR
    "${SDK_PATH}/media/rockit/rockit/mpi/sdk/include")

add_library(Rockchip::Rockit STATIC IMPORTED GLOBAL)
set_target_properties(Rockchip::Rockit PROPERTIES
    IMPORTED_LOCATION "${AIDEN_DEBIAN_VENDOR_LIB_DIR}/librockit.a")

add_library(Rockchip::MPP STATIC IMPORTED GLOBAL)
set_target_properties(Rockchip::MPP PROPERTIES
    IMPORTED_LOCATION "${AIDEN_DEBIAN_VENDOR_LIB_DIR}/librockchip_mpp.a")

add_library(Rockchip::RGA SHARED IMPORTED GLOBAL)
set_target_properties(Rockchip::RGA PROPERTIES
    IMPORTED_LOCATION "${AIDEN_DEBIAN_VENDOR_LIB_DIR}/librga.so")

# The SDK contains no glibc RVE/IVE runtime. Aiden does not call RVE directly,
# so Debian intentionally omits the legacy unconditional dependency.
set(AIDEN_OPTIONAL_MEDIA_TARGETS "")
set(AIDEN_PLATFORM_COMPAT_SOURCES
    "${CMAKE_SOURCE_DIR}/src/rockchip_glibc_compat.c")

add_library(Rockchip::RKNNFull SHARED IMPORTED GLOBAL)
set_target_properties(Rockchip::RKNNFull PROPERTIES
    IMPORTED_LOCATION "${AIDEN_RKNPU2_ROOT}/lib/librknnrt.so")
set(AIDEN_RKNN_TARGET Rockchip::RKNNFull)
set(AIDEN_RKNN_INCLUDE_DIR "${AIDEN_RKNPU2_ROOT}/include")
set(AIDEN_RKNN_COMPILE_DEFINITIONS AIDEN_RKNN_FULL_RUNTIME=1)

# The glibc SDK ships MPP only as a static archive. OpenCV-Mobile's Rockchip
# JPEG path resolves MPP through dlsym(), so export the exact symbols it needs
# from frame_service and force the linker to retain their archive members.
set(_aiden_mpp_export_symbols
    mpp_buffer_get_with_tag
    mpp_buffer_put_with_caller
    mpp_buffer_get_ptr_with_caller
    mpp_frame_init
    mpp_frame_deinit
    mpp_frame_set_width
    mpp_frame_set_height
    mpp_frame_set_hor_stride
    mpp_frame_set_ver_stride
    mpp_frame_set_pts
    mpp_frame_set_eos
    mpp_frame_set_jpege_chan_id
    mpp_frame_set_buffer
    mpp_frame_set_fmt
    mpp_create_ext
    mpp_init
    mpp_destroy
    mpp_enc_cfg_init
    mpp_enc_cfg_deinit
    mpp_enc_cfg_set_s32
    mpp_enc_cfg_set_u32
)
foreach(_aiden_mpp_symbol IN LISTS _aiden_mpp_export_symbols)
    list(APPEND AIDEN_FRAME_SERVICE_LINK_OPTIONS
        "LINKER:--undefined=${_aiden_mpp_symbol}"
        "LINKER:--export-dynamic-symbol=${_aiden_mpp_symbol}")
endforeach()
unset(_aiden_mpp_symbol)
unset(_aiden_mpp_export_symbols)
