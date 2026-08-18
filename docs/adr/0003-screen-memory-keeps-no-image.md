# Screen Memory keeps derived text, not the captured image

A Screen Memory stores only what the vision model derived from the frame — a summary, the specific strings likely to be asked about, and tags and entities for retrieval. The JPEG is discarded once the call returns. Keeping it was rejected because Long-Term Memory is protected data that the Storage Manager never reclaims, and the device escalates to Emergency at 5 MB of free space; a few dozen presses could wedge it with no automatic recovery. `episodes/` already stores JPEGs with no cleaner covering that path, so adding a second unbounded image sink would compound an existing problem rather than avoid it.

## Consequences

- If the vision model misreads or overlooks something on screen, it cannot be recovered later. The derived text is the only record.
- Recovering from a failed vision call by storing a placeholder and backfilling it later is impossible, which is part of why a failed capture stores nothing at all.
- The name "Screen Memory" does not imply a stored screenshot. The glossary makes this explicit.
