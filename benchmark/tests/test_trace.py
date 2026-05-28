from runner.trace import extract_trace, extract_step_screenshots

HISTORY = [
    {"type": "user", "content": "请打开设置"},
    {"type": "tool_call", "tool_name": "screenshot", "tool_input": "{}"},
    {"type": "tool_result", "tool_name": "screenshot",
     "content": '{"width":1080,"height":1920,"format":"jpeg","size":4,"data":"AAAA"}'},
    {"type": "tool_call", "tool_name": "mouse_click", "tool_input": '{"x":540,"y":1200}'},
    {"type": "tool_result", "tool_name": "mouse_click",
     "content": '{"width":1080,"height":1920,"format":"jpeg","size":4,"data":"BBBB","action_output":"ok"}'},
    {"type": "assistant", "content": "已打开。"},
]

def test_extract_trace_collects_tool_calls_in_order():
    trace = extract_trace(HISTORY)
    assert trace.total_tool_calls == 2
    assert trace.tool_calls[0].tool == "screenshot"
    assert trace.tool_calls[1].tool == "mouse_click"
    assert trace.tool_calls[1].input == {"x": 540, "y": 1200}
    assert trace.final_response == "已打开。"

def test_extract_trace_marks_has_screenshot_when_data_present():
    trace = extract_trace(HISTORY)
    assert trace.tool_calls[0].has_screenshot is True
    assert trace.tool_calls[1].has_screenshot is True

def test_extract_step_screenshots_returns_base64_payloads():
    shots = extract_step_screenshots(HISTORY)
    assert len(shots) == 2
    assert shots[0] == ("screenshot", "AAAA")
    assert shots[1] == ("mouse_click", "BBBB")

def test_extract_trace_handles_malformed_input_gracefully():
    history = [
        {"type": "tool_call", "tool_name": "screenshot", "tool_input": "not-json"},
        {"type": "tool_result", "tool_name": "screenshot", "content": "also-not-json"},
        {"type": "assistant", "content": ""},
    ]
    trace = extract_trace(history)
    assert trace.total_tool_calls == 1
    assert trace.tool_calls[0].input == {}
    assert trace.tool_calls[0].has_screenshot is False
