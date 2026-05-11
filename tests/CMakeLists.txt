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
    ${CMAKE_SOURCE_DIR}/src/config.cpp
    ${CMAKE_SOURCE_DIR}/src/vad.cpp
    ${CMAKE_SOURCE_DIR}/src/openrouter_codec.cpp
    ${CMAKE_SOURCE_DIR}/src/openai_codec.cpp
    ${CMAKE_SOURCE_DIR}/src/minimax_codec.cpp
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
    ${CMAKE_SOURCE_DIR}/src/tool_dispatch.cpp
    ${CMAKE_SOURCE_DIR}/src/tool_image_attachment.cpp
    ${CMAKE_SOURCE_DIR}/src/agent_version.cpp
    ${CMAKE_SOURCE_DIR}/src/wav_codec.cpp
    ${CMAKE_SOURCE_DIR}/src/provider_factory.cpp
    ${CMAKE_SOURCE_DIR}/src/llm_api_url.cpp
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

add_executable(aiden_tests
    test_main.cpp
    config_test.cpp
    vad_test.cpp
    openrouter_codec_test.cpp
    openai_codec_test.cpp
    minimax_codec_test.cpp
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
    tool_dispatch_test.cpp
    tool_image_attachment_test.cpp
    agent_version_test.cpp
    wav_codec_test.cpp
    provider_factory_test.cpp
    llm_api_url_test.cpp
    ${AIDEN_IMAGE_TEST_SOURCES}
    ${AIDEN_TEST_SOURCES}
)

target_include_directories(aiden_tests PRIVATE
    ${CMAKE_SOURCE_DIR}/src
)

target_link_libraries(aiden_tests PRIVATE doctest)
if(OpenCV_FOUND)
    target_link_libraries(aiden_tests PRIVATE aiden_image)
endif()

add_test(NAME aiden_tests COMMAND $<TARGET_FILE:aiden_tests>)
