# Host-native unit tests.
# Build only sources that compile without Pico hardware / SDK dependencies.

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
    ${CMAKE_SOURCE_DIR}/src/frame_ipc.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_capture_manager.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_processing.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_service_client.cpp
    ${CMAKE_SOURCE_DIR}/src/frame_service_server.cpp
    ${CMAKE_SOURCE_DIR}/src/tool_dispatch.cpp
    ${CMAKE_SOURCE_DIR}/src/tool_image_attachment.cpp
    ${CMAKE_SOURCE_DIR}/src/wav_codec.cpp
    ${CMAKE_SOURCE_DIR}/src/provider_factory.cpp
    ${CMAKE_SOURCE_DIR}/src/llm_api_url.cpp
    ${CMAKE_SOURCE_DIR}/src/cJSON/cJSON.c
)

add_executable(aiden_tests
    test_main.cpp
    config_test.cpp
    vad_test.cpp
    openrouter_codec_test.cpp
    openai_codec_test.cpp
    minimax_codec_test.cpp
    frame_service_protocol_test.cpp
    frame_service_defaults_test.cpp
    frame_ring_buffer_test.cpp
    frame_ipc_test.cpp
    frame_service_client_test.cpp
    frame_service_server_test.cpp
    frame_capture_manager_test.cpp
    frame_processing_test.cpp
    tool_dispatch_test.cpp
    tool_image_attachment_test.cpp
    wav_codec_test.cpp
    provider_factory_test.cpp
    llm_api_url_test.cpp
    ${AIDEN_TEST_SOURCES}
)

target_include_directories(aiden_tests PRIVATE
    ${CMAKE_SOURCE_DIR}/src
)

target_link_libraries(aiden_tests PRIVATE doctest)

add_test(NAME aiden_tests COMMAND aiden_tests)
