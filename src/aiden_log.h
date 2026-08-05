#ifndef AIDEN_LOG_H
#define AIDEN_LOG_H

#include <stdio.h>

namespace aiden {

enum class LogLevel {
    DEBUG,
    INFO,
    WARN,
    ERROR,
};

// Sets the process-wide service name included in every subsequent log line.
// Names are normalized to lowercase snake_case.
void set_log_service(const char* service);

// Overrides the process-wide log destination. Passing NULL restores stderr.
// This hook is primarily intended for tests.
void set_log_output(FILE* output);

// Writes one event using the common format. The formatted message is emitted
// as a quoted message= field with control characters escaped, so one call
// always produces exactly one physical line.
void log_event(LogLevel level,
               const char* component,
               const char* event,
               const char* format,
               ...)
#if defined(__GNUC__) || defined(__clang__)
               __attribute__((format(printf, 4, 5)))
#endif
               ;

}  // namespace aiden

#define AIDEN_LOG_DEBUG(component, event, ...) \
    ::aiden::log_event(::aiden::LogLevel::DEBUG, component, event, __VA_ARGS__)
#define AIDEN_LOG_INFO(component, event, ...) \
    ::aiden::log_event(::aiden::LogLevel::INFO, component, event, __VA_ARGS__)
#define AIDEN_LOG_WARN(component, event, ...) \
    ::aiden::log_event(::aiden::LogLevel::WARN, component, event, __VA_ARGS__)
#define AIDEN_LOG_ERROR(component, event, ...) \
    ::aiden::log_event(::aiden::LogLevel::ERROR, component, event, __VA_ARGS__)

#endif  // AIDEN_LOG_H
