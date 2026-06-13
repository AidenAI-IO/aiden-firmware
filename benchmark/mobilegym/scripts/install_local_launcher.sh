#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCHMARK_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
REPO_ROOT="$(cd "$BENCHMARK_ROOT/.." && pwd)"
LABEL="com.aiden.mobilegym-local-launcher"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG_DIR="$HOME/Library/Logs/Aiden"

mkdir -p "$(dirname "$PLIST")" "$LOG_DIR"

ENVIRONMENT_XML=""
if [[ -n "${MOBILEGYM_DOCKER_PROXY:-}" ]]; then
  ENVIRONMENT_XML="${ENVIRONMENT_XML}    <key>MOBILEGYM_DOCKER_PROXY</key>
    <string>${MOBILEGYM_DOCKER_PROXY}</string>
"
fi
ENVIRONMENT_XML="${ENVIRONMENT_XML}    <key>MOBILEGYM_PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST</key>
    <string>${MOBILEGYM_PLAYWRIGHT_CHROMIUM_DOWNLOAD_HOST:-https://playwright-akamai.azureedge.net}</string>"

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>-c</string>
    <string>cd "$REPO_ROOT" &amp;&amp; exec uv run --project benchmark python benchmark/mobilegym/scripts/local_launcher.py --host 127.0.0.1 --port 4174</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>EnvironmentVariables</key>
  <dict>
$ENVIRONMENT_XML
  </dict>
  <key>WorkingDirectory</key>
  <string>$REPO_ROOT</string>
  <key>StandardOutPath</key>
  <string>$LOG_DIR/mobilegym-local-launcher.out.log</string>
  <key>StandardErrorPath</key>
  <string>$LOG_DIR/mobilegym-local-launcher.err.log</string>
</dict>
</plist>
EOF

launchctl bootout gui/$UID "$PLIST" >/dev/null 2>&1 || true
launchctl bootstrap gui/$UID "$PLIST"
launchctl kickstart -k gui/$UID/$LABEL

echo "Installed and started $LABEL"
echo "Endpoint: http://127.0.0.1:4174"
echo "Logs: $LOG_DIR/mobilegym-local-launcher.{out,err}.log"
