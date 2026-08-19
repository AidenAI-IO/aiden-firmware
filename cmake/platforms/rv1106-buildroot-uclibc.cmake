set(AIDEN_OPENCV_DIR
    "${CMAKE_SOURCE_DIR}/third_party/opencv-mobile/lib/cmake/opencv4")
set(AIDEN_ROCKIT_INCLUDE_DIR
    "${SDK_PATH}/media/rockit/rockit/mpi/sdk/include")

add_library(Rockchip::Rockit STATIC IMPORTED GLOBAL)
set_target_properties(Rockchip::Rockit PROPERTIES
    IMPORTED_LOCATION "${SDK_PATH}/media/rockit/rockit/lib/lib32/librockit.a")

add_library(Rockchip::MPP STATIC IMPORTED GLOBAL)
set_target_properties(Rockchip::MPP PROPERTIES
    IMPORTED_LOCATION
        "${SDK_PATH}/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf/lib/librockchip_mpp.a")

add_library(Rockchip::RGA SHARED IMPORTED GLOBAL)
set_target_properties(Rockchip::RGA PROPERTIES
    IMPORTED_LOCATION
        "${SDK_PATH}/media/rga/release_rga_rv1106_arm-rockchip830-linux-uclibcgnueabihf/lib/librga.so")

add_library(Rockchip::RVE SHARED IMPORTED GLOBAL)
set_target_properties(Rockchip::RVE PROPERTIES
    IMPORTED_LOCATION "${SDK_PATH}/media/ive/ive/lib/librve.so")
set(AIDEN_OPTIONAL_MEDIA_TARGETS Rockchip::RVE)

set(_aiden_rknn_micro_runtime
    "${SDK_PATH}/media/iva/iva/librockiva/rockiva-rv1106-Linux/lib/librknnmrt.so")
if(EXISTS "${CMAKE_SOURCE_DIR}/overlay/oem/usr/lib/librknnmrt.so")
    set(_aiden_rknn_micro_runtime
        "${CMAKE_SOURCE_DIR}/overlay/oem/usr/lib/librknnmrt.so")
endif()
add_library(Rockchip::RKNNMicro SHARED IMPORTED GLOBAL)
set_target_properties(Rockchip::RKNNMicro PROPERTIES
    IMPORTED_LOCATION "${_aiden_rknn_micro_runtime}")
set(AIDEN_RKNN_TARGET Rockchip::RKNNMicro)
set(AIDEN_RKNN_INCLUDE_DIR "${CMAKE_SOURCE_DIR}/src")
set(AIDEN_RKNN_COMPILE_DEFINITIONS AIDEN_RKNN_MICRO_RUNTIME=1)
