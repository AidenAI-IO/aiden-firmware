# Coordinate Debug 页面脚本编辑器

## 概述

Coordinate Debug 页面新增了脚本编辑器功能，允许你直接在浏览器中编写、编辑和运行 JSONL 格式的自动化脚本，无需保存文件。

## 功能特性

- ✏️ **实时编辑**: 在页面内直接编辑 JSONL 脚本
- ▶️ **一键运行**: 点击运行按钮立即执行脚本
- 📊 **结果展示**: 实时显示每一步的执行状态、耗时和输出
- 🔄 **自动刷新**: 脚本调用 screenshot 工具时自动更新画面
- ❌ **错误提示**: 清晰展示执行失败的步骤和原因

## 脚本格式

每行一个 JSON 对象，支持三种操作类型：

### 1. wait - 等待延迟

```jsonl
{"type":"wait","ms":500}
{"wait":500}
```

### 2. tts - 语音播报（异步）

```jsonl
{"type":"tts","text":"开始演示"}
{"tts":"开始演示"}
```

### 3. call - 调用工具

```jsonl
{"type":"call","tool":"screenshot","input":{}}
{"call":{"tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}}
```

## 示例脚本

### 基础演示脚本

```jsonl
{"tts":"开始演示"}
{"wait":500}
{"call":{"tool":"screenshot","input":{}}}
{"tts":"点击屏幕中心"}
{"call":{"tool":"touch_gesture","input":{"type":"tap","point":{"x":500,"y":500}}}}
{"wait":1000}
{"call":{"tool":"screenshot","input":{}}}
{"tts":"演示完成"}
```

### 滑动操作脚本

```jsonl
{"tts":"准备向下滑动"}
{"wait":300}
{"call":{"tool":"touch_gesture","input":{"type":"swipe","start":{"x":500,"y":800},"end":{"x":500,"y":200}}}}
{"wait":800}
{"call":{"tool":"screenshot","input":{}}}
```

### 输入文字脚本

```jsonl
{"tts":"输入测试文字"}
{"call":{"tool":"keyboard_text","input":{"text":"Hello World"}}}
{"wait":500}
{"call":{"tool":"keyboard_tap","input":{"keys":["enter"]}}}
{"wait":1000}
{"call":{"tool":"screenshot","input":{}}}
```

## 使用流程

1. **访问页面**: 打开 Aiden Agent 首页，点击 "🎯 坐标调试" 链接
2. **编写脚本**: 在 "📝 脚本编辑器" 区域输入你的 JSONL 脚本
3. **运行脚本**: 点击 "▶️ 运行脚本" 按钮执行
4. **查看结果**: 在下方的结果面板查看每一步的执行状态

## 支持的工具

脚本可以调用所有通过 HTTP API 暴露的工具，包括：

- `screenshot` - 截图
- `touch_gesture` - 触摸手势（tap, swipe, long_press 等）
- `keyboard_text` - 键盘输入文字
- `keyboard_tap` - 键盘按键
- `mouse_click` - 鼠标点击
- `mouse_move` - 鼠标移动
- `quick_action` - 快捷操作
- 等等...

查看所有可用工具: `GET /api/tools`

## 注意事项

1. **TTS 是异步的**: `tts` 步骤会立即返回，不会等待语音播报完成
2. **错误会中断**: 脚本遇到第一个同步错误会停止执行
3. **最大限制**: 
   - 每行最大 1MB
   - 每个脚本最多 1000 步
4. **调试建议**: 
   - 先测试单个步骤
   - 使用合适的 wait 延迟
   - 检查工具参数是否正确

## 与文件脚本的区别

| 特性 | 页面编辑器 | run_script 工具 |
|------|-----------|----------------|
| 编辑方式 | 浏览器内实时编辑 | 需要保存为文件 |
| 运行方式 | 点击按钮 | 通过 LLM 调用工具 |
| 结果展示 | 实时可视化 | JSON 响应 |
| TTS 支持 | 仅标记 | 实际播放 |
| 适用场景 | 快速测试调试 | 正式演示录制 |

## 快捷键

- `Esc` - 清除坐标标记

## 技术实现

- 前端通过 `/api/tools/{toolName}` HTTP API 直接调用工具
- 无需保存文件，直接在内存中解析执行
- 支持 JSONL 的完整格式和简写格式
- 自动解析 screenshot 工具的返回值并更新画面

## 调试技巧

1. **逐步执行**: 先运行单行测试每个操作是否正确
2. **观察输出**: 查看每步的输出判断工具是否按预期工作
3. **调整延迟**: 根据设备响应速度调整 wait 时长
4. **使用截图**: 在关键步骤后添加 screenshot 确认状态
