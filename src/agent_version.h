#pragma once

namespace aiden {

const char* agent_version();
const char* agent_commit_time();
bool is_agent_version_command(int argc, char* const argv[]);

}
