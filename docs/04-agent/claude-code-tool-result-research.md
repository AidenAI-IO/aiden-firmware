# Claude Code Large Tool Result and Compaction Research

> Date: 2026-07-29
> Scope: Anthropic official documentation and the public `anthropics/claude-code` repository only
> Public repository snapshot: `7ef6eec9d9ba84ea6f233f26c45f1df5c5991843`

## Summary

Claude Code uses separate mechanisms for four different problems:

1. **Intrinsic large output** is normally moved out of the model-visible message and persisted as a session artifact; the model receives a preview and file reference.
2. **Large file reads** are paged from the original file rather than copied into a second artifact. An oversized whole-file read returns the first page plus a `PARTIAL view` continuation notice.
3. **Context pressure** first removes older tool outputs from active model context, then summarizes the conversation if more space is required.
4. **Session persistence** is independent of active model context. The complete transcript and spilled tool-result files remain on disk for resume, rewind, and targeted reads until retention cleanup.

The important design distinction is therefore:

```text
durable transcript / raw artifact
             !=
current model-visible context
```

Claude Code does not publicly document a fixed “keep the latest N tool results” rule. It documents an age-based preference—older tool outputs are cleared first—and manual compaction can preserve the turns after a selected boundary. There is no public guarantee that the latest unconsumed result is always kept inline in full.

## Behavior Comparison

| Case | Trigger | Model-visible result | Full data availability | Evidence |
| --- | --- | --- | --- | --- |
| Foreground Bash output | More than 30,000 characters by default | Short preview from the start plus file path | Full output saved under the session directory; Claude may `Read` or search it | [Tools reference: Timeout and output limits](https://code.claude.com/docs/en/tools-reference#timeout-and-output-limits) |
| Generic large tool result | More than 50K characters as of Claude Code 2.1.51 | Bounded inline result plus file reference | Persisted in the session `tool-results/` directory | [2.1.2 introduced persistence](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3928-L3947), [2.1.51 lowered the threshold to 50K](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3369-L3381), [application-data paths](https://code.claude.com/docs/en/claude-directory#application-data) |
| Background Bash/task output | Large background output; documented fix caps inline content at 30K | Truncated inline content plus output-file path | Background task output file remains readable | [2.1.0 background output behavior](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3986-L4000), [TaskOutput deprecation](https://code.claude.com/docs/en/tools-reference) |
| Hook output | More than 50K characters | Preview plus path instead of direct injection | Full hook output saved to disk | [Claude Code changelog](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L2603-L2610) |
| Oversized whole-file `Read` | Read exceeds the file-read token budget | First page plus `PARTIAL view`, received amount, and offset/limit guidance | Original source file remains the artifact; Claude issues another paged read | [Tools reference: Read tool behavior](https://code.claude.com/docs/en/tools-reference#read-tool-behavior), [behavior change](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L1394-L1400) |
| Explicit paged `Read` still too large | Requested offset/limit range exceeds the token budget | Error asking for a smaller range or targeted search | Original source file remains unchanged | [Tools reference: Read tool behavior](https://code.claude.com/docs/en/tools-reference#read-tool-behavior) |
| Context approaching its limit | Active prompt becomes full | Older tool outputs are cleared first; conversation is summarized if still necessary | Full local transcript is separate from the reduced active context | [How Claude Code works: When context fills up](https://code.claude.com/docs/en/how-claude-code-works#when-context-fills-up), [session storage](https://code.claude.com/docs/en/how-claude-code-works#work-with-sessions) |

## 1. Output Truncation Is Usually a Preview Boundary, Not Data Loss

The current Bash documentation is explicit:

- the default inline output limit is **30,000 characters**;
- `BASH_MAX_OUTPUT_LENGTH` can raise it, up to **150,000 characters**;
- output beyond the limit is written to a session file;
- Claude receives the path and a short preview from the start, then reads or searches the file when more detail is required.

This is progressive disclosure: the first result remains useful for the common case, while exact details stay recoverable without occupying every future prompt.

The generic spill layer is broader than Bash. The public changelog says large Bash and large tool outputs changed from truncation to disk persistence in 2.1.2, and 2.1.51 lowered the generic persistence threshold from 100K to 50K characters. The application-data documentation identifies the storage location as:

```text
~/.claude/projects/<project>/<session>/tool-results/
```

The public changelog also records that large processed results are released from process memory. Persistence to disk therefore does not imply keeping a second full in-memory copy for the session lifetime. [Source](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3400-L3404)

### MCP exceptions

MCP servers can set `_meta["anthropic/maxResultSizeChars"]` to raise the size allowed through the inline/persist decision layer, up to 500K characters. This is an explicit producer hint for results such as database schemas; it is not a global context-budget increase. [Source](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L2528-L2534)

Binary MCP content follows the same representation split: PDFs, office documents, and audio are decoded to files instead of injecting base64 into conversation context. [Source](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3264-L3269)

## 2. File Reads Use Source Paging

`Read` is different from a command whose stdout would otherwise disappear:

- the source file already provides durable raw storage;
- an oversized whole-file read returns a first-page `PARTIAL view` instead of failing;
- the notice tells Claude how much it received and how to continue with `offset` and `limit`;
- an explicit range that is still too large errors rather than silently returning an ambiguous subset;
- the file-read token limit can be overridden with `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS`.

This design avoids copying every large file into a tool-result artifact. It also makes continuation semantics visible to the model. A partial read does not satisfy Claude Code's read-before-edit check, which prevents the model from treating incomplete evidence as a complete file view. [Tools reference](https://code.claude.com/docs/en/tools-reference#read-tool-behavior), [limit override](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3983-L3987)

Claude Code additionally deduplicates unchanged re-reads to reduce context usage. The changelog does not publish the exact replacement payload or cache key, so this should be treated as an observed product guarantee, not a reusable implementation specification. [Source](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L2644-L2653)

## 3. Persistence and References Are Session-Scoped

Claude Code writes each message, tool use, and tool result to a plaintext session transcript:

```text
~/.claude/projects/<project>/<session>.jsonl
```

Large spilled results are stored separately under the matching session's `tool-results/` directory. This supports resume, rewind, debugging, and targeted reads without requiring the raw payload to stay in the active prompt. [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works#work-with-sessions), [Application data](https://code.claude.com/docs/en/claude-directory#application-data)

Retention is bounded. `cleanupPeriodDays` defaults to 30 days with a minimum of one day, and startup cleanup deletes session files and other application data older than that setting. Transcript persistence can also be disabled explicitly. [Settings](https://code.claude.com/docs/en/settings#available-settings)

The practical contract is therefore “durable for the configured session-retention period,” not permanent archival storage.

## 4. Recent-Result Retention

Anthropic documents a relative policy, not a fixed count:

> Claude Code clears older tool outputs first, then summarizes the conversation if needed.

This establishes recency preference for active context. It does **not** publicly specify:

- how many recent results remain;
- whether size can override recency;
- whether the latest unconsumed result is always protected;
- whether the preview/reference for every old artifact survives the later summary.

Manual selective compaction provides one stronger recent-history control: the rewind menu's “Summarize up to here” operation compresses history before a chosen boundary while keeping later turns intact. [Source](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L1513-L1524)

Background-agent completion messages explicitly include their output-file path so a parent can recover the result after context compaction. This is evidence that Anthropic treats durable references as the recovery mechanism when recent conversational detail may disappear. [Source](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L3130-L3135)

## 5. Context Compaction

Claude Code performs two levels of pressure relief:

```text
remove older tool outputs from active context
                    ->
summarize conversation history if still necessary
```

`/compact` replaces conversation history with a structured summary. Automatic compaction uses the same general mechanism near the context limit. The official documentation states that user requests and key code snippets are preserved, but detailed instructions from early conversation may be lost; persistent rules should therefore live in `CLAUDE.md`, not only in chat history. A user can focus the summary with `/compact focus on ...` or a `Compact Instructions` section. [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works#when-context-fills-up)

Compaction is not a uniform summary of every context component:

- system prompt and output style remain unchanged;
- root `CLAUDE.md`, unscoped rules, and auto memory are re-injected from disk;
- path-scoped rules and nested `CLAUDE.md` content are lost until triggered again;
- invoked skill bodies are re-injected with per-skill and total caps, dropping oldest skills first when necessary;
- the local session transcript remains the durable record even though active context now uses the summary.

See [Explore the context window: What survives compaction](https://code.claude.com/docs/en/context-window#what-survives-compaction).

Compaction also has a circuit breaker: if a single file or result immediately refills context after repeated summaries, Claude Code stops retrying and reports an actionable error rather than burning calls indefinitely. [Official documentation](https://code.claude.com/docs/en/how-claude-code-works#when-context-fills-up), [changelog](https://github.com/anthropics/claude-code/blob/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843/CHANGELOG.md#L2585-L2589)

## Design Lessons for Aiden

1. **Keep materialization and compaction separate.** Large-result handling should happen immediately when the result is produced; session compaction is later pressure relief for accumulated history.
2. **Preserve raw artifacts outside model context.** Persist full output, but store only a decision-useful preview and opaque reference inline.
3. **Page original files rather than duplicating them.** A `PARTIAL` marker must be explicit and must not count as complete observation.
4. **Degrade consumed history before the current observation.** Claude Code documents clearing older tool results first. Aiden can strengthen this with an explicit latest-unconsumed protection class.
5. **Treat references as durable recovery handles.** Artifact paths/IDs should survive transcript persistence and be preferentially preserved by compaction summaries.
6. **Bound artifact retention and reads.** Artifact cleanup needs a documented TTL, and artifact retrieval must itself be paged so it cannot recreate the original overflow.
7. **Do not confuse UI collapse with context removal.** Terminal expansion controls affect presentation; model-context pruning, artifact persistence, and transcript retention are separate concerns.

## Source Limitations

The public `anthropics/claude-code` repository does not contain the core Claude Code runtime implementation. It publishes the official README, changelog, plugins, examples, and operational material, but not the source that selects previews, writes tool-result artifacts, or constructs compacted prompts.

Consequently, this report does not infer undocumented details such as:

- exact preview head/tail selection for generic non-Bash tools;
- exact artifact filenames or atomic-write behavior;
- exact last-N retention counts;
- the scoring rule used to choose summary content;
- a guarantee that every artifact reference survives automatic compaction.

Those details would require either an official implementation publication or black-box testing and should not be represented as source-confirmed Claude Code behavior.

## Primary Sources

- [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference)
- [How Claude Code works](https://code.claude.com/docs/en/how-claude-code-works)
- [Explore the context window](https://code.claude.com/docs/en/context-window)
- [Claude directory and application data](https://code.claude.com/docs/en/claude-directory#application-data)
- [Claude Code settings](https://code.claude.com/docs/en/settings#available-settings)
- [`anthropics/claude-code` at the reviewed commit](https://github.com/anthropics/claude-code/tree/7ef6eec9d9ba84ea6f233f26c45f1df5c5991843)
