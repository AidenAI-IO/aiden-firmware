#!/bin/bash
# Integration test for config-check CLI integration with config_web.
#
# All payloads use the real wire format produced by config_web.cpp's
# config_to_json(): snake_case keys, agent-level settings nested under an
# "agent" object, and search reporting only has_api_key (the UI never echoes
# the stored secret). Using the actual wire shape here is the point: the
# earlier PascalCase fixtures bypassed the decode path and masked a contract
# mismatch that accepted every invalid config in production.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$SCRIPT_DIR/../build/bin"
AGENT_BIN="$BUILD_DIR/agent"

echo "=== Config-Check Integration Tests ==="
echo ""

# Test 1: Valid config
echo "Test 1: Valid configuration should pass"
VALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"duckduckgo"},"agent":{},"device":{"device_type":"iOS"}}'
RESULT=$(echo "$VALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json)
if echo "$RESULT" | grep -q '"valid".*:.*true'; then
    echo "✓ Valid config passed"
else
    echo "✗ Valid config failed"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 2: Invalid search provider (google)
echo "Test 2: Invalid search provider 'google' should fail"
INVALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"google"},"agent":{}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false' && echo "$RESULT" | grep -q 'search.provider'; then
    echo "✓ Invalid provider 'google' rejected"
else
    echo "✗ Invalid provider 'google' not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 3: Invalid search provider (bing)
echo "Test 3: Invalid search provider 'bing' should fail"
INVALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"bing"},"agent":{}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false'; then
    echo "✓ Invalid provider 'bing' rejected"
else
    echo "✗ Invalid provider 'bing' not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 4: Missing model provider
echo "Test 4: Missing model provider should fail"
INVALID_CONFIG='{"model":{"provider":"","model":"gpt-4"},"search":{"provider":"duckduckgo"},"agent":{}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false' && echo "$RESULT" | grep -q 'model.provider'; then
    echo "✓ Missing model provider rejected"
else
    echo "✗ Missing model provider not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 5: Invalid VAD threshold (nested under "agent" — the real wire position).
# This is the case that exposes the contract: a top-level/PascalCase payload
# would silently drop this field and report valid=true.
echo "Test 5: Invalid VAD threshold (nested under agent) should fail"
INVALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"duckduckgo"},"agent":{"vad_speech_threshold":1.5}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false' && echo "$RESULT" | grep -q 'vad_speech_threshold'; then
    echo "✓ Invalid VAD threshold rejected"
else
    echo "✗ Invalid VAD threshold not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 6: Invalid device_type (nested under "device"). Same contract concern as
# Test 5 but for a snake_case key inside a nested object.
echo "Test 6: Invalid device.device_type (nested under device) should fail"
INVALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"duckduckgo"},"device":{"device_type":"blackberry"},"agent":{}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false' && echo "$RESULT" | grep -q 'device_type'; then
    echo "✓ Invalid device_type rejected"
else
    echo "✗ Invalid device_type not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 7: brave with has_api_key=true should pass (UI does not echo the secret)
echo "Test 7: brave provider with has_api_key=true should pass"
VALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"brave","has_api_key":true},"agent":{}}'
RESULT=$(echo "$VALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json)
if echo "$RESULT" | grep -q '"valid".*:.*true'; then
    echo "✓ brave with key passed"
else
    echo "✗ brave with key failed"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 8: brave with has_api_key=false should fail
echo "Test 8: brave provider with has_api_key=false should fail"
INVALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"brave","has_api_key":false},"agent":{}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false'; then
    echo "✓ brave without key rejected"
else
    echo "✗ brave without key not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 9: tavily with has_api_key=true should pass
echo "Test 9: tavily provider with has_api_key=true should pass"
VALID_CONFIG='{"model":{"provider":"openai","model":"gpt-4"},"search":{"provider":"tavily","has_api_key":true},"agent":{}}'
RESULT=$(echo "$VALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json)
if echo "$RESULT" | grep -q '"valid".*:.*true'; then
    echo "✓ tavily with key passed"
else
    echo "✗ tavily with key failed"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

echo "=== All tests passed! ==="
