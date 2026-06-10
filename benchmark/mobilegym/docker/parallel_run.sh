#!/usr/bin/env bash
# parallel_run.sh - Run multiple MobileGym tests in parallel with full isolation
#
# Each test runs in its own Docker container with isolated simulator/daemon/bridge.
# This provides complete environment isolation for stateful tests (e.g., modifying
# alarms, contacts, settings).
#
# Usage:
#   ./parallel_run.sh [task1] [task2] [task3] ...
#
# Examples:
#   # Run specific tasks
#   ./parallel_run.sh clock.CountAlarms clock.ToggleAlarm
#
#   # Run a suite across multiple containers
#   ./parallel_run.sh --suite aiden_smoke
#
#   # Custom parallel count with suite
#   PARALLEL=4 ./parallel_run.sh --suite full

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Default number of parallel containers
PARALLEL="${PARALLEL:-4}"

# Parse arguments
ARGS=("$@")
if [[ ${#ARGS[@]} -eq 0 ]]; then
    echo "Usage: $0 <task-id> [task-id...] | --suite <suite-name>"
    echo ""
    echo "Examples:"
    echo "  $0 clock.CountAlarms clock.ToggleAlarm"
    echo "  $0 --suite aiden_smoke"
    echo "  PARALLEL=8 $0 --suite full"
    exit 1
fi

# Check if running a suite or individual tasks
if [[ "${ARGS[0]}" == "--suite" ]]; then
    SUITE="${ARGS[1]:-}"
    if [[ -z "$SUITE" ]]; then
        echo "Error: --suite requires a suite name"
        exit 1
    fi

    echo "Running suite '$SUITE' with $PARALLEL parallel containers..."
    echo ""

    # Run test containers in parallel, MobileGym will distribute tasks
    pids=()
    for i in $(seq 0 $((PARALLEL - 1))); do
        echo "Starting container $i..."
        docker compose run --rm test --suite "$SUITE" --parallel 1 &
        pids+=($!)
    done

    echo ""
    echo "Waiting for $PARALLEL containers to complete..."

    # Wait for all and collect exit codes
    failed=0
    for pid in "${pids[@]}"; do
        if ! wait "$pid"; then
            failed=$((failed + 1))
        fi
    done

    echo ""
    if [[ $failed -eq 0 ]]; then
        echo "✓ All containers completed successfully"
        exit 0
    else
        echo "✗ $failed container(s) failed"
        exit 1
    fi
else
    # Run individual tasks in parallel
    TASKS=("${ARGS[@]}")
    echo "Running ${#TASKS[@]} tasks in parallel (1 task per container)..."
    echo ""

    pids=()
    for task in "${TASKS[@]}"; do
        echo "Starting: $task"
        docker compose run --rm test --task-id "$task" &
        pids+=($!)
    done

    echo ""
    echo "Waiting for ${#TASKS[@]} tasks to complete..."

    # Wait for all and collect exit codes
    failed=0
    for i in "${!pids[@]}"; do
        pid="${pids[$i]}"
        task="${TASKS[$i]}"
        if wait "$pid"; then
            echo "✓ $task"
        else
            echo "✗ $task (failed)"
            failed=$((failed + 1))
        fi
    done

    echo ""
    if [[ $failed -eq 0 ]]; then
        echo "✓ All tasks completed successfully"
        exit 0
    else
        echo "✗ $failed task(s) failed"
        exit 1
    fi
fi
