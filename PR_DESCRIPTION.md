# Disable chat_history Injection into Planner Context

## Summary

**BREAKING CHANGE**: Disables automatic injection of `chat_history` into the planner's context to prevent architectural conflicts with the session system.

## Problem

The `chat_history` injection mechanism (introduced in PR #140) creates several issues:

1. **Duplicate Context**: Both `session/events.jsonl` and `chat_history/events.jsonl` contain conversation history, but only session has compaction. This creates duplicate, competing sources of truth.

2. **Unbounded Growth**: `chat_history` has no compaction mechanism. It relies only on filtering completed episodes, leading to unbounded growth over time.

3. **Confused Boundaries**: Session system has clear hot-window boundaries and compression. Injecting chat_history muddles these carefully managed boundaries.

4. **Always-On Behavior**: History is injected even when not needed, without user intent detection or selective loading.

## Solution

Disable `chat_history` injection into planner context entirely. The session system already provides:
- ✅ Comprehensive history management with token-based compaction
- ✅ Hot window + archived chunks with recall
- ✅ Session boundary detection and rotation
- ✅ Split-turn support for long conversations

## Changes

### Core Changes
- **`runtime.go`**: Disable `newChatHistoryPlannerMemory` wrapper with detailed comment explaining rationale
- **`chat_history_memory.go`**: No changes needed - the formatting code remains but becomes unused (dead code)

### Documentation
- **`context-lifecycle.md`**: Add prominent NOTE about disabled injection, explain why, document what chat_history is still used for
- **`session-memory.md`**: Add note referencing the rationale in context-lifecycle.md

### Tests
- **`TestRuntimeRunDoesNotInjectPersistedChatHistory`**: Verify chat_history is NOT injected (renamed from `TestRuntimeRunIncludesPersistedInterruptedEpisodeInPlannerHistory`)
- **Other tests**: Existing tests like `TestFormatChatHistoryForPlannerDoesNotHardCapMessageCount` remain unchanged - they test dead code that may be useful for future explicit loading features

## What chat_history is Still Used For

The `chat_history` store remains available for:
- **UI display**: Show conversation history across sessions in the web interface
- **Audit logging**: Persistent record of all user interactions
- **Future features**: Explicit session restore mechanisms

## Migration Path for "Resume Task" Feature

Instead of always-on injection, implement explicit intent-driven restore:

1. **Intent Detection**: Detect when user says "继续" or "resume"
2. **Explicit Restore**: Use session restore API or recall tools to load specific episodes
3. **Clear Communication**: Tell user which episode is being resumed
4. **UI Support**: Add session management UI to browse and restore archived sessions

## Testing

All tests pass:
```bash
go test ./internal/agent -run ".*ChatHistory.*" -v
# PASS: 6 tests including TestRuntimeRunDoesNotInjectPersistedChatHistory
```

Note: Tests for `formatChatHistoryForPlanner` still exist and pass, but that function is now dead code (not called since injection is disabled).

## Related

- Introduced in: PR #140 (`feat(agent): persist chat history with episode traces`)
- Initial commit in this PR removed message caps (46b267b) - that change is preserved as the function is no longer called
- This commit disables injection and adds test to verify (2fea05f)

## Breaking Change Impact

**Low Risk**: The feature was always-on but likely had minimal visible impact since:
- Only interrupted episodes were injected
- Most users probably don't have many interrupted episodes
- The 20-message cap limited the actual injection size

Users who relied on implicit cross-session context will need to explicitly resume sessions (future feature).
