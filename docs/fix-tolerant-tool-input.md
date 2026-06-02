# Fix: Tolerant Tool Input Decoding for Memory Tools

## Problem

LLMs occasionally stringify structured tool arguments, emitting:
- `"tags": "[]"` instead of `"tags": []`
- `"limit": "3"` instead of `"limit": 3`

This caused tool call failures:
```
Tool error: decode recall_session_chunks input: json: cannot unmarshal string into Go struct field ChunkRecallQuery.tags of type []string
```

## Root Cause

The executor role generated malformed JSON:
```json
{"tags": "[]", "limit": "3"}
```

Strict `json.Unmarshal` rejected these stringified values, causing the entire tool invocation to fail.

## Solution

Added **tolerant input decoders** that accept:

### For array fields (flexStringSlice):
- ✅ Proper JSON array: `["a","b"]`
- ✅ String-wrapped array: `"[\"a\",\"b\"]"` (common LLM mistake)
- ✅ Empty string-wrapped array: `"[]"`
- ✅ Bare single string: `"foo"` → `["foo"]`
- ✅ Null/empty → `nil`

### For integer fields (flexInt):
- ✅ JSON number: `42`
- ✅ String-wrapped number: `"3"` (common LLM mistake)
- ✅ Float: `3.0` or `"3.0"`
- ✅ Null/empty → `0`

## Files Changed

### New Files
1. **`tool_input.go`** - Flex types (`flexStringSlice`, `flexInt`) and query decoders
2. **`tool_input_test.go`** - Unit tests for flex types and decoders
3. **`memory_tools_malformed_test.go`** - Integration tests simulating the production error

### Modified Files
1. **`memory_tools.go`** - Updated 3 tool `Call()` methods:
   - `RecallSessionChunksTool` → uses `decodeChunkRecallQuery()`
   - `RecallMemoryTool` → uses `decodeMemoryQuery()`
   - `RecallDeviceMemoryTool` → uses `decodeDeviceMemoryQuery()`

## Test Coverage

### Unit Tests (tool_input_test.go)
- ✅ `TestFlexStringSlice` - 7 cases covering all shapes
- ✅ `TestFlexInt` - 6 cases covering all shapes
- ✅ `TestDecodeChunkRecallQueryToleratesStringifiedArgs` - exact production payload
- ✅ `TestDecodeChunkRecallQueryWellFormed` - 4 well-formed cases
- ✅ `TestDecodeMemoryQueryToleratesStringifiedArgs`
- ✅ `TestDecodeDeviceMemoryQueryToleratesStringifiedArgs`

### Integration Tests (memory_tools_malformed_test.go)
- ✅ `TestRecallSessionChunksToolToleratesMalformedLLMInput` - end-to-end with malformed input
- ✅ `TestRecallMemoryToolToleratesMalformedLLMInput`
- ✅ `TestRecallDeviceMemoryToolToleratesMalformedLLMInput`

All existing tests continue to pass.

## Backward Compatibility

✅ **100% backward compatible**
- Well-formed inputs decode exactly as before
- Only adds fallback paths for malformed inputs
- No changes to query struct definitions or store APIs

## Impact

This fix prevents tool call failures when the LLM makes common formatting mistakes, improving system robustness without requiring perfect tool-calling discipline from the model.
