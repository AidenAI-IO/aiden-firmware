cmake_minimum_required(VERSION 3.16)
project(aiden_sdk LANGUAGES C CXX)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_CXX_STANDARD 11)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

option(AIDEN_TESTS "Build host-native unit tests only (skips Pico targets)" OFF)

if(AIDEN_TESTS)
    enable_testing()
    add_subdirectory(tests)
    return()
endif()

add_link_options(-Wl,-rpath,/oem/usr/lib)

set(CMAKE_ARCHIVE_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/lib)
set(CMAKE_LIBRARY_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/lib)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/bin)

set(SDK_PATH ${CMAKE_SOURCE_DIR}/pico-sdk)
set(ROCKIT_PATH ${SDK_PATH}/media/rockit/rockit)
set(MPP_PATH ${SDK_PATH}/media/mpp/release_mpp_rv1106_arm-rockchip830-linux-uclibcgnueabihf)
set(RGA_PATH ${SDK_PATH}/media/rga/release_rga_rv1106_arm-rockchip830-linux-uclibcgnueabihf)
set(IVE_PATH ${SDK_PATH}/media/ive/ive)

add_library(aiden STATIC
    src/aiden_sdk.cpp
)

target_include_directories(aiden
    PUBLIC
        ${CMAKE_SOURCE_DIR}/src
        ${ROCKIT_PATH}/mpi/sdk/include
)

target_link_directories(aiden
    PUBLIC
        ${ROCKIT_PATH}/lib/lib32
        ${MPP_PATH}/lib
        ${RGA_PATH}/lib
        ${IVE_PATH}/lib
)

target_link_libraries(aiden
    PUBLIC
        rockit
        rockchip_mpp
        rga
        rve
        pthread
        m
)

add_executable(hello src/hello.c)
add_executable(trigger src/trigger.c)

add_executable(example_wakeup src/example_wakeup.cpp)
target_link_libraries(example_wakeup PRIVATE aiden)

add_executable(example_audio_capture src/example_audio_capture.cpp)
target_link_libraries(example_audio_capture PRIVATE aiden)

add_executable(example_audio_play src/example_audio_play.cpp)
target_link_libraries(example_audio_play PRIVATE aiden)

add_executable(example_camera_capture src/example_camera_capture.cpp)
target_link_libraries(example_camera_capture PRIVATE aiden)

add_executable(example_usb_hid src/example_usb_hid.cpp src/hid_server.cpp)
target_include_directories(example_usb_hid PRIVATE ${CMAKE_SOURCE_DIR}/src)
target_link_libraries(example_usb_hid PRIVATE aiden)

add_executable(audio_stream src/audio_stream.cpp)
target_link_libraries(audio_stream PRIVATE aiden)

# AI Agent
add_library(cjson STATIC src/cJSON/cJSON.c)
target_include_directories(cjson PUBLIC ${CMAKE_SOURCE_DIR}/src)

add_executable(agent_main
    src/agent_main.cpp
    src/config.cpp
    src/http_client.cpp
    src/openrouter_client.cpp
    src/openrouter_codec.cpp
    src/openai_client.cpp
    src/openai_codec.cpp
    src/llm_api_url.cpp
    src/minimax_tts.cpp
    src/minimax_codec.cpp
    src/tool_dispatch.cpp
    src/wav_codec.cpp
    src/provider_factory.cpp
    src/provider_factory_runtime.cpp
    src/vad.cpp
)
target_link_libraries(agent_main PRIVATE aiden cjson pthread)
