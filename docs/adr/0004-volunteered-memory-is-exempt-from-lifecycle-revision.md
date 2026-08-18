# Volunteered memory is exempt from confidence, supersession, and conflict

The existing memory lifecycle — confidence starting below 1.0 and moving with run outcomes, `DecideAction` superseding on tag and entity overlap, `MarkConflict` marking both sides conflicted — was built for Derived Memory, which is inferred from a run and can be wrong. Screen Memory is Volunteered Memory: the user pressed a button while looking at the screen. It is an observation with no truth value to revise, so every one of those mechanisms produces a wrong result on it. It is therefore written at confidence 1.0 and excluded from all three.

The exclusions are not uniform, because the existing code is not symmetric. `shouldPenalizeMemoryType` already gates the failure side, so a new memory type is exempt from penalties by default. The success side has no such gate: it unconditionally raises confidence, refreshes `LastValidatedAt`, and calls `refreshMemoryExpiry`. That last one is the damaging part — TTL is the only automatic reclamation path for long-term memory, so refreshing it on every successful recall means a frequently-asked Screen Memory never expires. A gate mirroring `shouldPenalizeMemoryType` is needed on the success side.

## Consequences

- Two presses on the same screen produce two independent memories, both active. This is the point: they are two moments, not two versions. Had `DecideAction` been in the path, the older one would have been marked `replaced` and become invisible to `Search` — silent data loss.
- Confidence being pinned is load-bearing for ordering, not just semantics. It is the third sort tie-break, ahead of recency; if it ever diverged, an older frequently-recalled entry would outrank a just-saved one and break "the one I just saved".
- The type appears as an exception in several lifecycle sites. A future reader may take this for an oversight and "fix" it, so the regression tests assert the exemptions directly.
- Explicit deletion via `forget_memory` becomes the only user-side cleanup path, since nothing supersedes or merges these entries.
