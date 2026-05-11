if(NOT DEFINED OUTPUT_FILE OR OUTPUT_FILE STREQUAL "")
    message(FATAL_ERROR "OUTPUT_FILE is required")
endif()

if(NOT DEFINED SOURCE_DIR OR SOURCE_DIR STREQUAL "")
    message(FATAL_ERROR "SOURCE_DIR is required")
endif()

set(_version "unknown")
find_package(Git QUIET)

if(GIT_FOUND)
    execute_process(
        COMMAND "${GIT_EXECUTABLE}" describe --tags --exact-match HEAD
        WORKING_DIRECTORY "${SOURCE_DIR}"
        RESULT_VARIABLE _tag_result
        OUTPUT_VARIABLE _tag
        ERROR_QUIET
        OUTPUT_STRIP_TRAILING_WHITESPACE
    )

    if(_tag_result EQUAL 0 AND NOT _tag STREQUAL "")
        set(_version "${_tag}")
    else()
        execute_process(
            COMMAND "${GIT_EXECUTABLE}" rev-parse --short HEAD
            WORKING_DIRECTORY "${SOURCE_DIR}"
            RESULT_VARIABLE _hash_result
            OUTPUT_VARIABLE _hash
            ERROR_QUIET
            OUTPUT_STRIP_TRAILING_WHITESPACE
        )

        if(_hash_result EQUAL 0 AND NOT _hash STREQUAL "")
            set(_version "${_hash}")
        endif()
    endif()
endif()

set(_escaped_version "${_version}")
string(REPLACE "\\" "\\\\" _escaped_version "${_escaped_version}")
string(REPLACE "\"" "\\\"" _escaped_version "${_escaped_version}")

get_filename_component(_output_dir "${OUTPUT_FILE}" DIRECTORY)
file(MAKE_DIRECTORY "${_output_dir}")

set(_contents "#pragma once\n#define AIDEN_AGENT_VERSION \"${_escaped_version}\"\n")
if(EXISTS "${OUTPUT_FILE}")
    file(READ "${OUTPUT_FILE}" _existing_contents)
else()
    set(_existing_contents "")
endif()

if(NOT _existing_contents STREQUAL _contents)
    file(WRITE "${OUTPUT_FILE}" "${_contents}")
endif()
