# 通用滑动交互设计

## 一、难点与现实边界

当前架构通过 USB HID 绝对坐标鼠标模拟触摸，通过 HDMI 截图获取视觉反馈。与原生测试框架（iOS XCTest、Android UIAutomator）相比，缺少关键的反馈通道：

| 能力 | 原生框架 | 当前架构 |
|------|----------|----------|
| 触摸事件确认 | ✅ AccessibilityEvent | ❌ 无 |
| 控件值读取 | ✅ AXPickerWheel | ❌ 只能视觉识别 |
| 滚动边界检测 | ✅ 事件回调 | ❌ 只能图像差分 |
| 惯性控制 | ✅ 精确 MotionEvent | ⚠️ 参数调优，不确定 |

**核心约束**：

- iOS/Android 惯性滚动是系统级行为，参数因设备/版本/控件实现（原生/Flutter/React Native）而异，无法精确控制
- 图像差分可以判断"有没有动"，但**不能可靠量化"动了多少"**（JPEG 压缩噪声、动画中间帧、相似内容干扰）
- LLM 视觉读值有误差，不能作为唯一依据，但作为最终确认手段是可行的

**结论**：放弃精确量化，转向**小步迭代 + 视觉确认**的闭环策略。

---

## 二、现有能力（无需改动）

### touch_gesture 工具

支持 `type: "swipe"`，已有完整参数：

```json
{
  "type": "swipe",
  "start": {"x": 0.5, "y": 0.6},
  "end":   {"x": 0.5, "y": 0.4},
  "duration_ms": 400,
  "steps": 16,
  "hold_before_ms": 80,
  "hold_after_ms": 100
}
```

默认值：700ms / 24步 / hold_before 80ms / hold_after 0ms。坐标系优先使用 `normalized`（0-1）。

### screenshot 工具

返回 base64 JPEG + 宽高。每次 HID 操作后自动截图（500ms 延迟），无需手动调用。

### save_memory / recall_memory

持久化跨会话记忆，用于缓存控件参数（见第五节）。

---

## 三、新增工具：image_diff

**唯一需要新增的工具**，纯计算，不过 LLM，用于快速判断滑动是否生效。

### 接口

```
输入：
  before:    string  — base64 JPEG（滑动前截图的 data 字段）
  after:     string  — base64 JPEG（滑动后截图的 data 字段）
  region:    object  — 可选，{x, y, w, h}，归一化坐标，限定比较区域

输出：
  changed:       bool    — diff_ratio > 0.01
  diff_ratio:    float   — 变化像素占比（0-1）
  primary_axis:  string  — "horizontal" | "vertical" | "none"
```

### 使用场景

- `changed: false`：滑动没有效果，需要加大 distance 或检查控件位置
- `diff_ratio < 0.03`：变化极小，可能是惯性还没停，等待后重试
- `primary_axis`：辅助判断滑动方向是否符合预期

### 不输出的字段

`shift_y_normalized`（量化位移）不输出，原因：JPEG 压缩引入的块效应会污染行亮度曲线，互相关结果不可靠，给出错误的位移量比不给更危险。

---

## 四、各场景滑动策略

### 通用原则

1. **每次滑动后等截图确认**，不要连续盲滑
2. **优先小步**（distance ≤ 0.05），确认有效后再加大
3. **用 image_diff 判断"有没有动"**，diff_ratio < 0.03 说明没效果
4. **迭代比精确更可靠**，不要试图一次滑到位
5. **最多重试 10 次**，超出后报告失败，不要无限循环

### Picker / 滚轮

典型场景：时间选择器、日期选择器、城市选择器。

```
策略：
1. 截图，识别 picker 当前值和目标值
2. 判断方向（目标值 > 当前值 → 向上滑；反之向下）
3. 执行 swipe：
   - distance: 0.03（约 1 格）
   - duration_ms: 400，steps: 16（慢速，减少惯性）
   - hold_after_ms: 100（抬起前停顿，抑制 iOS 惯性）
4. 等待截图，读取新值
5. 如果 image_diff.changed=false → 加大 distance 到 0.05，重试
6. 如果已到目标 → 结束
7. 如果过调 → 反向滑 1 格，重新确认
```

**关键参数**：慢速（duration_ms=400）+ hold_after_ms=100 是抑制 iOS 惯性最有效的组合，比 anti_overshoot 反向微移更可靠。

### 列表滚动

典型场景：联系人列表、设置列表、消息列表。

```
策略：
1. 截图，检查是否有搜索框 → 优先用搜索，避免盲滚
2. 无搜索时：
   - 粗滚：distance=0.35，duration_ms=500
   - 用 image_diff 确认滚动发生（diff_ratio > 0.05）
   - 截图判断目标是否可见
3. 目标可见但需微调：distance=0.05
4. image_diff.changed=false → 已到边界，停止
```

### 横向轮播 / Tab 切换

典型场景：图片浏览、Tab 页切换、引导页。

```
策略：
- distance: 0.4（不要 < 0.3，容易被识别为误触弹回）
- duration_ms: 400
- 截图确认页面切换（diff_ratio 通常 > 0.3）
- 如果没切换成功，检查是否在正确的可滑动区域
```

### 地图 / 画布平移

典型场景：地图导航、图片编辑。

```
策略：
- 估算目标方向，distance=0.2 开始
- 截图确认目标是否进入视野，迭代调整
- 不需要 image_diff，直接看截图判断
- 精细定位时改用 distance=0.05
```

---

## 五、控件参数缓存

通过现有 `save_memory` 实现，LLM 自主决定存储时机，不需要新工具。

### 存储格式

```json
{
  "type": "procedure",
  "title": "[AppName] [控件名] 滑动参数",
  "content": "app: 微信, screen: 时间选择器, picker: 小时列, 有效 distance: 0.035, hold_after_ms: 100, 中心位置: x=0.5 y=0.38",
  "tags": ["swipe", "picker", "calibration"],
  "entities": ["微信"],
  "priority": 70
}
```

### 触发时机

- **存**：首次成功操作某个滑动控件后，主动 save_memory
- **取**：遇到同类控件时，先 recall_memory 查是否有缓存参数，有则直接用，跳过试探阶段

---

## 六、不做的事

| 方案 | 放弃原因 |
|------|----------|
| 图像差分量化位移（shift_y_normalized） | JPEG 噪声 + 动画帧 + 相似内容，结果不可靠 |
| 自适应步长校准工具 | LLM + save_memory 已够用，不需要专用工具 |
| anti_overshoot 反向微移 | 经验参数，不同设备/版本效果差异大 |
| 专用 scroll_picker 工具 | touch_gesture swipe 已够用，差异在 prompt 策略 |
| OCR 工具（当前阶段） | LLM 视觉读值准确率 > 90% 时不需要，后续可按需加 |

---

## 七、工程改动清单

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `src/agent/internal/agent/tools_image_diff.go` | 新建 | ImageDiffTool 实现 |
| `src/agent/internal/agent/tools.go` | 修改 | 注册 image_diff |
| `src/agent/internal/agent/prompt.go` | 修改 | 补充滑动策略指导 |
