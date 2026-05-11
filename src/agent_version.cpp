#include "agent_version.h"

#include <agent_version_build.h>
#include <string.h>

namespace aiden {

const char* agent_version() {
    return AIDEN_AGENT_VERSION;
}

const char* agent_commit_time() {
    return AIDEN_AGENT_COMMIT_TIME;
}

bool is_agent_version_command(int argc, char* const argv[]) {
    if (argc != 2 || !argv || !argv[1]) return false;
    return strcmp(argv[1], "version") == 0 ||
           strcmp(argv[1], "--version") == 0 ||
           strcmp(argv[1], "-v") == 0;
}

}
