package agent

const benchmarkPerceptionSystemPrompt = `You generate a single PERCEPTION-class benchmark task for a device-control agent.

Output ONLY a single JSON object (no markdown, no explanation, no fences) with this exact shape:

{
  "task": {
    "id": "<task_id>",
    "category": "perception",
    "input_screenshot": "screenshots/<task_id>.jpg",
    "prompt": "<short instruction in same language as user_intent>",
    "description_for_judge": "<English explanation of what success looks like>",
    "rubric": [
      {"id": "called_click_tool", "check": "The tool trace contains at least one touch_gesture or mouse_click call."},
      {"id": "click_targets_<slug>", "check": "The touch/click coordinates target the <target_name> area: normalized x in [PLACEHOLDER_X], y in [PLACEHOLDER_Y] (0-1000 normalized space, where 500 is center)."}
    ],
    "hard_assertions": {"min_tool_calls": 1, "max_tool_calls": 5, "must_complete_within_sec": 120, "response_required": true}
  }
}

The user supplies the screenshot, target rectangle, target_name, task_id, and user_intent. You will receive these in the user message. Use them to fill in:
- "prompt": short imperative in the user's language (Chinese stays Chinese)
- "description_for_judge": English description of how the agent should succeed (mention the visible UI element)
- The rubric "check" wording. Leave the literal placeholders PLACEHOLDER_X and PLACEHOLDER_Y in the second rubric — the server will substitute exact coordinate ranges.
- The slug placeholder <slug> — leave as the literal text "<slug>"; the server replaces it.

Do not invent a screenshot path other than "screenshots/<task_id>.jpg". Do not fabricate coordinate numbers. Output JSON only.`
