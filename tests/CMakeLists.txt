# Host-native unit tests.
# Build only sources that compile without Pico hardware / SDK dependencies.

set(CMAKE_ARCHIVE_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/lib)
set(CMAKE_LIBRARY_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/lib)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/bin)

add_library(doctest INTERFACE)
target_include_directories(doctest INTERFACE
    ${CMAKE_SOURCE_DIR}/third_party/doctest
)

set(AIDEN_TEST_SOURCES
    ${CMAKE_SOURCE_DIR}/src/audio_playback_session.cpp
    ${CMAKE_SOURCE_DIR}/src/audio_record_session.cpp
    ${CMAKE_SOURCE_DIR}/src/wifi_config.cpp
    ${CMAKE_SOURCE_DIR}/src/service_status.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_service_protocol.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_ring_buffer.cpp
    ${CMAKE_SOURCE_DIR}/src/uds_message.cpp
    ${CMAKE_SOURCE_DIR}/src/uds_client.cpp
    ${CMAKE_SOURCE_DIR}/src/uds_server.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_ipc.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_capture_manager.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_processing.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_service_client.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_service_server.cpp
    ${CMAKE_SOURCE_DIR}/src/audio_service_protocol.cpp
    ${CMAKE_SOURCE_DIR}/src/audio_service_client.cpp
    ${CMAKE_SOURCE_DIR}/src/audio_session_manager.cpp
    ${CMAKE_SOURCE_DIR}/src/audio_volume_state.cpp
    ${CMAKE_SOURCE_DIR}/src/cJSON/cJSON.c
)

# Prefer system OpenCV for normal host tests. Bundled opencv-mobile is ARM/RV1106 only: use it
# when cross-compiling or when CMAKE_C_COMPILER is the Rockchip/ARM toolchain.
find_package(OpenCV QUIET COMPONENTS core imgproc)
if(NOT OpenCV_FOUND)
    if(CMAKE_CROSSCOMPILING OR CMAKE_C_COMPILER MATCHES "rockchip830|arm-rockchip|arm-linux")
        set(OpenCV_DIR "${CMAKE_SOURCE_DIR}/third_party/opencv-mobile/lib/cmake/opencv4")
        find_package(OpenCV QUIET COMPONENTS core imgproc)
    endif()
endif()

# Debian and Ubuntu keep OpenCVConfig.cmake in the all-module libopencv-dev
# metapackage. Host tests need only core and imgproc, so discover those component
# packages directly when the config file is intentionally not installed.
if(NOT OpenCV_FOUND AND NOT CMAKE_CROSSCOMPILING)
    find_path(OpenCV_COMPONENT_INCLUDE_DIR
        NAMES opencv2/core.hpp
        PATH_SUFFIXES opencv4
    )
    find_library(OpenCV_CORE_LIBRARY NAMES opencv_core)
    find_library(OpenCV_IMGPROC_LIBRARY NAMES opencv_imgproc)
    if(OpenCV_COMPONENT_INCLUDE_DIR AND OpenCV_CORE_LIBRARY AND OpenCV_IMGPROC_LIBRARY)
        add_library(opencv_core UNKNOWN IMPORTED)
        set_target_properties(opencv_core PROPERTIES
            IMPORTED_LOCATION "${OpenCV_CORE_LIBRARY}"
            INTERFACE_INCLUDE_DIRECTORIES "${OpenCV_COMPONENT_INCLUDE_DIR}"
        )
        add_library(opencv_imgproc UNKNOWN IMPORTED)
        set_target_properties(opencv_imgproc PROPERTIES
            IMPORTED_LOCATION "${OpenCV_IMGPROC_LIBRARY}"
            INTERFACE_INCLUDE_DIRECTORIES "${OpenCV_COMPONENT_INCLUDE_DIR}"
            INTERFACE_LINK_LIBRARIES opencv_core
        )
        set(OpenCV_FOUND TRUE)
        set(OpenCV_INCLUDE_DIRS "${OpenCV_COMPONENT_INCLUDE_DIR}")
        set(OpenCV_VERSION "system-components")
    endif()
endif()

set(AIDEN_IMAGE_TEST_SOURCES "")
if(OpenCV_FOUND)
    add_library(aiden_image STATIC ${CMAKE_SOURCE_DIR}/src/image_process.cpp)
    target_include_directories(aiden_image
        PUBLIC
            ${CMAKE_SOURCE_DIR}/src
            ${OpenCV_INCLUDE_DIRS}
            ${CMAKE_SOURCE_DIR}/third_party/stb
    )
    target_link_libraries(aiden_image PUBLIC opencv_imgproc)
    if(APPLE)
        foreach(_ocv_t opencv_core opencv_imgproc opencv_photo opencv_video opencv_features2d opencv_highgui)
            if(TARGET ${_ocv_t})
                get_target_property(_il ${_ocv_t} INTERFACE_LINK_LIBRARIES)
                if(_il)
                    string(REPLACE "$<LINK_ONLY:rt>;" "" _il "${_il}")
                    string(REPLACE ";$<LINK_ONLY:rt>" "" _il "${_il}")
                    set_target_properties(${_ocv_t} PROPERTIES INTERFACE_LINK_LIBRARIES "${_il}")
                endif()
            endif()
        endforeach()
    endif()
    list(APPEND AIDEN_IMAGE_TEST_SOURCES image_process_test.cpp)
    message(STATUS "OpenCV ${OpenCV_VERSION} found; image_process tests enabled in aiden_tests")
else()
    message(STATUS "OpenCV not found; image_process tests omitted from aiden_tests")
endif()

list(APPEND AIDEN_IMAGE_TEST_SOURCES frame_jpeg_encoder_stub.cpp)
list(APPEND AIDEN_IMAGE_TEST_SOURCES aiden_sdk_audio_stub.cpp)

add_executable(aiden_tests
    test_main.cpp
    aiden_log_test.cpp
    wifi_config_test.cpp
    frame_service_protocol_test.cpp
    frame_service_defaults_test.cpp
    uds_message_test.cpp
    uds_client_server_test.cpp
    frame_ring_buffer_test.cpp
    frame_ipc_test.cpp
    frame_service_client_test.cpp
    frame_service_server_test.cpp
    frame_capture_manager_test.cpp
    frame_processing_test.cpp
    frame_crop_bounds_test.cpp
    terminal_input_test.cpp
    service_status_test.cpp
    vad_common_test.cpp
    system_env_parser_test.cpp
    audio_service_protocol_test.cpp
    audio_service_client_test.cpp
    audio_playback_session_test.cpp
    audio_session_manager_test.cpp
    audio_volume_state_test.cpp
    ${CMAKE_SOURCE_DIR}/src/system_env_parser.cpp
    ${AIDEN_IMAGE_TEST_SOURCES}
    ${AIDEN_TEST_SOURCES}
)

target_include_directories(aiden_tests PRIVATE
    ${CMAKE_SOURCE_DIR}/src
)

target_compile_definitions(aiden_tests PRIVATE
    AIDEN_SOURCE_DIR="${CMAKE_SOURCE_DIR}"
)

target_link_libraries(aiden_tests PRIVATE doctest)
if(OpenCV_FOUND)
    target_link_libraries(aiden_tests PRIVATE aiden_image)
endif()

find_library(UTIL_LIBRARY util)
if(UTIL_LIBRARY)
    target_link_libraries(aiden_tests PRIVATE ${UTIL_LIBRARY})
endif()

add_test(NAME aiden_tests COMMAND $<TARGET_FILE:aiden_tests>)

# Config Web is implemented by the Go Agent binary. Keep its HTTP contract in
# the CTest suite by running the package tests directly; the retired C++ server
# is intentionally not built as a test target.
find_program(GO_EXECUTABLE go)
if(GO_EXECUTABLE)
    add_test(NAME config_web_go
        COMMAND ${GO_EXECUTABLE} test ./internal/configweb
        WORKING_DIRECTORY ${CMAKE_SOURCE_DIR}/src/agent
    )
endif()

add_test(NAME logger_shell_format
    COMMAND sh ${CMAKE_SOURCE_DIR}/scripts/test_logger_format.sh
)

add_test(NAME frame_service_bridge_policy
    COMMAND sh ${CMAKE_SOURCE_DIR}/scripts/test_frame_service_bridge_policy.sh
)

add_test(NAME board_low_level_smoke_scripts
    COMMAND sh ${CMAKE_SOURCE_DIR}/scripts/test_board_low_level_smoke.sh
)

find_program(NODE_EXECUTABLE node)
if(NODE_EXECUTABLE)
    add_test(NAME config_web_dynamic_form
        COMMAND ${NODE_EXECUTABLE} --experimental-vm-modules ${CMAKE_CURRENT_SOURCE_DIR}/config_web_dynamic_form_test.mjs
    )
    add_test(NAME config_web_i18n
        COMMAND ${NODE_EXECUTABLE} --experimental-vm-modules ${CMAKE_CURRENT_SOURCE_DIR}/config_web_i18n_test.mjs
    )
    add_test(NAME config_web_providers
        COMMAND ${NODE_EXECUTABLE} --experimental-vm-modules ${CMAKE_CURRENT_SOURCE_DIR}/config_web_providers_test.mjs
    )
    add_test(NAME config_web_storage
        COMMAND ${NODE_EXECUTABLE} --experimental-vm-modules ${CMAKE_CURRENT_SOURCE_DIR}/config_web_storage_test.mjs
    )
    add_test(NAME llm_logs_anthropic
        COMMAND ${NODE_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/llm_logs_anthropic_test.mjs
    )
endif()

# Shell-level test for the /opt bind mount that opkg installs into. Needs no
# build artifacts and no board: S22opt reads its paths and its mount/umount
# binaries from the environment, so the test stubs them out.
add_test(
    NAME s22opt_tests
    COMMAND /bin/sh ${CMAKE_CURRENT_SOURCE_DIR}/test_s22opt.sh
)

add_test(
    NAME aiden_opt_profile_tests
    COMMAND /bin/sh ${CMAKE_CURRENT_SOURCE_DIR}/test_aiden_opt_profile.sh
)

add_test(NAME usb0_dhcp_options
    COMMAND sh ${CMAKE_SOURCE_DIR}/scripts/test_usb0_dhcp_options.sh
)
