#include "service_status.h"
#include <string.h>

namespace aiden {

const char* service_status_to_string(AidenServiceStatus status) {
    switch (status) {
        case AidenServiceStatus::OK:                return "OK";
        case AidenServiceStatus::NO_NEW_FRAME:      return "NO_NEW_FRAME";
        case AidenServiceStatus::FRAME_NOT_FOUND:   return "FRAME_NOT_FOUND";
        case AidenServiceStatus::SESSION_NOT_FOUND: return "SESSION_NOT_FOUND";
        case AidenServiceStatus::SERVICE_RECOVERING:return "SERVICE_RECOVERING";
        case AidenServiceStatus::TIMEOUT:           return "TIMEOUT";
        case AidenServiceStatus::TRANSPORT_ERROR:   return "TRANSPORT_ERROR";
        case AidenServiceStatus::INTERNAL_ERROR:    return "INTERNAL_ERROR";
    }
    return "INTERNAL_ERROR";
}

bool service_status_from_string(const char* text, AidenServiceStatus* out) {
    if (!text || !out) return false;

    struct Mapping {
        const char* text;
        AidenServiceStatus status;
    };
    static const Mapping kMappings[] = {
        {"OK",                AidenServiceStatus::OK},
        {"NO_NEW_FRAME",      AidenServiceStatus::NO_NEW_FRAME},
        {"FRAME_NOT_FOUND",   AidenServiceStatus::FRAME_NOT_FOUND},
        {"SESSION_NOT_FOUND", AidenServiceStatus::SESSION_NOT_FOUND},
        {"SERVICE_RECOVERING",AidenServiceStatus::SERVICE_RECOVERING},
        {"TIMEOUT",           AidenServiceStatus::TIMEOUT},
        {"TRANSPORT_ERROR",   AidenServiceStatus::TRANSPORT_ERROR},
        {"INTERNAL_ERROR",    AidenServiceStatus::INTERNAL_ERROR},
    };
    for (size_t i = 0; i < sizeof(kMappings) / sizeof(kMappings[0]); ++i) {
        if (strcmp(text, kMappings[i].text) == 0) {
            *out = kMappings[i].status;
            return true;
        }
    }
    return false;
}

}  // namespace aiden
