# Host-native unit tests.
# Build only sources that compile without Pico hardware / SDK dependencies.

add_library(doctest INTERFACE)
target_include_directories(doctest INTERFACE
    ${CMAKE_SOURCE_DIR}/third_party/doctest
)

set(AIDEN_TEST_SOURCES
    ${CMAKE_SOURCE_DIR}/src/config.cpp
    ${CMAKE_SOURCE_DIR}/src/vad.cpp
)

add_executable(aiden_tests
    test_main.cpp
    config_test.cpp
    vad_test.cpp
    ${AIDEN_TEST_SOURCES}
)

target_include_directories(aiden_tests PRIVATE
    ${CMAKE_SOURCE_DIR}/src
)

target_link_libraries(aiden_tests PRIVATE doctest)

add_test(NAME aiden_tests COMMAND aiden_tests)
