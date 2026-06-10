#!/bin/bash
# Integration test for config-check CLI integration with config_web

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$SCRIPT_DIR/../build/bin"
AGENT_BIN="$BUILD_DIR/agent"

echo "=== Config-Check Integration Tests ==="
echo ""

# Test 1: Valid config
echo "Test 1: Valid configuration should pass"
VALID_CONFIG='{"Model":{"Provider":"openai","Model":"gpt-4"},"Search":{"Provider":"duckduckgo"}}'
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
INVALID_CONFIG='{"Model":{"Provider":"openai","Model":"gpt-4"},"Search":{"Provider":"google"}}'
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
INVALID_CONFIG='{"Model":{"Provider":"openai","Model":"gpt-4"},"Search":{"Provider":"bing"}}'
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
INVALID_CONFIG='{"Model":{"Provider":"","Model":"gpt-4"},"Search":{"Provider":"duckduckgo"}}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false' && echo "$RESULT" | grep -q 'model.provider'; then
    echo "✓ Missing model provider rejected"
else
    echo "✗ Missing model provider not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 5: Invalid VAD threshold
echo "Test 5: Invalid VAD threshold should fail"
INVALID_CONFIG='{"Model":{"Provider":"openai","Model":"gpt-4"},"Search":{"Provider":"duckduckgo"},"VADSpeechThreshold":1.5}'
RESULT=$(echo "$INVALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json 2>&1 || true)
if echo "$RESULT" | grep '"valid"' | grep -q 'false'; then
    echo "✓ Invalid VAD threshold rejected"
else
    echo "✗ Invalid VAD threshold not properly rejected"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 6: Valid brave provider
echo "Test 6: Valid brave provider should pass"
VALID_CONFIG='{"Model":{"Provider":"openai","Model":"gpt-4"},"Search":{"Provider":"brave","APIKey":"test-key"}}'
RESULT=$(echo "$VALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json)
if echo "$RESULT" | grep -q '"valid".*:.*true'; then
    echo "✓ Valid brave provider passed"
else
    echo "✗ Valid brave provider failed"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

# Test 7: Valid tavily provider
echo "Test 7: Valid tavily provider should pass"
VALID_CONFIG='{"Model":{"Provider":"openai","Model":"gpt-4"},"Search":{"Provider":"tavily","APIKey":"test-key"}}'
RESULT=$(echo "$VALID_CONFIG" | "$AGENT_BIN" config-check --stdin --format=json)
if echo "$RESULT" | grep -q '"valid".*:.*true'; then
    echo "✓ Valid tavily provider passed"
else
    echo "✗ Valid tavily provider failed"
    echo "Result: $RESULT"
    exit 1
fi
echo ""

echo "=== All tests passed! ==="
