#!/bin/bash
# Generate agent files visualization report on Luckfox device
# This script should be deployed to the device

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT="${1:-/userdata/agent/files_report.html}"

echo "Generating agent files report..."
python3 "$SCRIPT_DIR/generate_agent_files_report.py" \
    --memory-dir /userdata/agent/memory \
    --skills-dir /userdata/agent/skills \
    --skill-state-dir /userdata/agent/skill-state \
    --skillopt-dir /userdata/agent/benchmark/runs/skillopt \
    --output "$OUTPUT"

if [ $? -eq 0 ]; then
    echo ""
    echo "✓ Report generated successfully!"
    echo "  Location: $OUTPUT"
    echo ""
    echo "To view the report:"
    echo "  1. Access via web: http://<device-ip>/agent/files_report.html"
    echo "  2. Or download and open locally"
fi
