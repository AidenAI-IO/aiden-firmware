# Agent Benchmark 模块改造提案（中文版）

日期：2026-05-21
状态：提案，详细 spec 见 `docs/superpowers/specs/2026-05-21-agent-driven-benchmark-design.md`

## 一、为什么要改

现有 `scripts/aiden_benchmark.py` + `benchmark/suites/full_smoke.json` 实际只是"工具冒烟脚本"：

- **不验证真实效果**：`tool` 任务只检查 CLI 返回码和 stdout 子串。`touch_click 16384 16384` 哪怕没点到任何东西，只要二进制返回 0 就算 PASS。
- **e2e 任务等于没断言**：所有 `agent_prompt` 任务的成功条件都是 stdout 出现 `[tools]`，等同于"agent 调过任何工具就算通过"，跟任务目标无关。
- **任务之间状态污染**：手机停在哪一页完全取决于上一任务，不可重复。
- **缺乏指标和历史**：每次跑只输出 pass/fail 和 duration_ms，运行结果互相覆盖，看不出回归。

目标变成：**用 agent 跑所有任务，验证它在手机上的实际操作能力。**

## 二、方案总览

把 benchmark 重新定位为"agent 能力评测"，所有任务通过 Go agent 的 HTTP API（`POST /api/chat`）执行。Agent 以 daemon 形式常驻运行，runner 只负责发请求、抓副产物、判分。

调用链：

```
runner (Python，本机执行)
  ├── POST /api/clear    (清空 agent 对话历史，隔离任务)
  ├── 全局 reset          (通过 /api/tools/invoke 直接调 HID 工具，把手机弄回主屏)
  ├── per-task setup      (可选，直接 tool 调用构造起点；setup 失败 → 任务 skipped)
  ├── pre 截图            (POST /api/tools/invoke screenshot)
  ├── POST /api/chat      ← 唯一被评测的接口
  │     request:  {"message": "任务 prompt"}
  │     response: ChatResponse{Response, History}
  │     History 包含结构化的 tool_call/tool_result 消息
  │     每个输入 tool 自动返回 post-action screenshot
  ├── 硬断言              (tool call 数下限/上限、超时、response 存在)
  └── LLM judge           (吃 artifact，不碰硬件，可离线重跑)
```

判分采用 **硬断言 + LLM judge** 两段式：

- **硬断言**用来卡明显失败（agent 卡死、根本没调工具、超时、截图全黑）。这一关挂了直接 fail，不再调 judge，省钱也省噪声。
- **LLM judge** 吃任务描述、rubric（每个任务 2~4 条独立验收点）、pre/post 截图、tool trace，逐项给 yes/no + 一句理由。任务级 pass 要求所有 rubric 全 yes。
- **Judge 默认换 provider**：被测如果是 OpenRouter，judge 用 Anthropic（或反之），降低同模型自评偏差。

## 三、能力维度

Benchmark 度量四个核心维度：

1. **单步操作正确性**（B）：给明确目标，agent 是否能正确点对位置、输入对内容。
2. **多步任务完成度**（C）：完整任务（"开设置 → 搜蓝牙 → 进蓝牙页"），看终态。
3. **错误恢复**（D）：从异常状态（深层 app 页 / 全屏弹窗）开始，看 agent 能不能识别并回到可操作状态。
4. **效率**（E）：相同任务用了多少步、多少 tool call、多少时间。

另外两个辅助维度：

- **感知准确性**（A）：单独的 `perception_v1.json` 子 suite，专门考"看图说话"，出问题时帮助定位是看错了还是做错了。
- **一致性**（F）：通过 `--repeats N` 让同一任务跑多次，得到 success_rate 而非单次 pass/fail。v1 默认 1 次，runner 稳定后再开。

## 四、关键设计决策

1. **任务集 v1 共 15 个主任务 + 3 个诊断任务**，覆盖 single_step / multi_step 两类，围绕 Go agent 实际可用的 tool 集（screenshot、keyboard_tap、keyboard_text、mouse_click、mouse_scroll、touch_gesture 等 13 个）设计。
2. **状态隔离**：每任务前调 `POST /api/clear` 清空对话历史 + 全局 reset 回主屏；部分任务在 reset 后用直接 tool 调用构造特定起点。
3. **Agent 是常驻 daemon**：Go agent 跑在 `:8080`，runner 通过 HTTP API 交互。不需要 spawn 子进程、不需要 `--once` 开关。
4. **Trace 结构化获取**：`ChatResponse.History` 直接返回 tool_call/tool_result 消息列表，不需要 regex 解析 stdout。每个输入 tool 自动附带 post-action screenshot。
5. **跨 run 对比**：`compare --runs A B` 输出两次 run 之间哪些任务 flip、效率指标差多少。这是回归检测的核心。
6. **不上 docker**：硬件在环（HID 设备、frame_service socket 都在测试机本机），docker 化只增加复杂度不增加隔离。Python 依赖用 venv + requirements.txt 锁版本即可。

## 4.1 v1 任务清单

### single_step（7 个）— 考单次操作正确性

| ID                        | 目标                                   | 主要考验的 tool 组合                     |
| ------------------------- | -------------------------------------- | ---------------------------------------- |
| `open_settings`           | 从桌面打开系统设置                     | screenshot → mouse_click                 |
| `open_clock`              | 从桌面打开时钟 app                     | screenshot → mouse_click                 |
| `tap_back`                | 从设置子页返回上一层（setup 进入子页） | screenshot → mouse_click 或 keyboard_tap |
| `type_in_search`          | 在搜索框输入 "hello"                   | screenshot → mouse_click → keyboard_text |
| `scroll_page_down`        | 向下滑动一屏                           | screenshot → touch_gesture(垂直)         |
| `swipe_between_pages`     | 在桌面左右滑动切换页面                 | screenshot → touch_gesture(水平)         |
| `open_notification_shade` | 从顶部下滑打开通知栏                   | screenshot → touch_gesture(从顶部向下)   |

### multi_step（8 个）— 考多步规划 + 工具组合

| ID                           | 目标                                             | 主要考验的 tool 组合                                     |
| ---------------------------- | ------------------------------------------------ | -------------------------------------------------------- |
| `settings_search_bluetooth`  | 进设置 → 搜索蓝牙 → 进入蓝牙页                   | screenshot + mouse_click + keyboard_text + touch_gesture |
| `toggle_wifi`                | 进设置 → 找 WiFi 开关 → 关闭再打开               | screenshot → mouse_click 连续                            |
| `add_clock_alarm`            | 时钟 → 新建 7:30 闹钟 → 保存                     | screenshot → mouse_click → keyboard_text                 |
| `scroll_to_bottom`           | 在设置页反复滑动直到到底（agent 需判断何时停止） | screenshot → touch_gesture(循环)                         |
| `type_long_mixed_text`       | 输入中英混合文字 "Aiden测试 benchmark-2026!"     | screenshot → mouse_click → keyboard_text                 |
| `select_all_and_delete`      | 在已有文字的输入框里全选并删除                   | screenshot → keyboard_tap META+A → BACKSPACE             |
| `copy_paste_text`            | 输入文字 → 全选 → 复制 → 点另一输入框 → 粘贴     | keyboard_text → keyboard_tap META+A/C/V → mouse_click    |
| `find_and_tap_specific_item` | 在设置列表中滑动找到"关于手机"并点击             | screenshot → touch_gesture(多次) → mouse_click           |

### Tool 覆盖矩阵

| Tool            | single_step | multi_step | 总计                                     |
| --------------- | ----------- | ---------- | ---------------------------------------- |
| `screenshot`    | 7/7         | 8/8        | 15（每个任务都用，auto via post-action） |
| `mouse_click`   | 4           | 7          | 11                                       |
| `touch_gesture` | 3           | 3          | 6                                        |
| `keyboard_text` | 1           | 4          | 5                                        |
| `keyboard_tap`  | 1           | 4          | 5                                        |

### 诊断子 suite（perception_v1.json，3 个，不计入主分数）

| ID                  | 目标                                 |
| ------------------- | ------------------------------------ |
| `name_current_page` | 看截图说出当前在哪个 app/页面        |
| `find_button`       | 找到屏幕上的"保存"按钮并给出坐标     |
| `count_list_items`  | 当前列表可见几个条目？列出前三个文字 |

## 五、目录结构

```
benchmark/
├── runner/              # Python 包，新写
│   ├── main.py          # CLI: run | rejudge | compare
│   ├── suite.py         # 加载/校验 suite
│   ├── reset.py         # 全局 reset + per-task setup（直接调 agent tool API）
│   ├── agent_client.py  # Go agent HTTP 客户端（/api/chat, /api/clear, /api/history）
│   ├── capture.py       # pre 截图（via /api/tools/invoke screenshot）
│   ├── trace.py         # 从 /api/history 提取结构化 trace
│   ├── assertions.py    # 硬断言
│   ├── judge.py         # LLM judge（纯函数，吃 artifact 写 judge.json）
│   ├── metrics.py       # 效率指标聚合
│   └── report.py        # JSONL + summary.md
├── suites/
│   ├── phone_control_v1.json   # 主 suite，15 个任务
│   └── perception_v1.json      # 诊断子 suite，3 个任务
└── runs/<run_id>/
    ├── manifest.json    # 整次 run 的环境信息（git sha、模型、suite 版本）
    ├── results.jsonl    # 每任务一行，机器读
    ├── summary.md       # 人读摘要
    └── tasks/<task_id>/
        ├── pre.jpg      # 执行前截图
        ├── history.json # 原始 /api/history 响应
        ├── trace.json   # 解析后的 tool call 序列
        ├── steps/       # 逐步 post-action 截图（从 tool_result 提取）
        │   ├── step_01_screenshot.jpg
        │   ├── step_02_mouse_click.jpg
        │   └── ...
        └── judge.json   # judge 的逐 rubric 输出 + 理由
```

## 六、实施步骤（粗略工期）

按依赖顺序：

1. **Runner 骨架 + Agent HTTP 客户端**（~2 天）
   `agent_client.py` / `suite.py` / `reset.py` / `capture.py` / `trace.py` / `assertions.py`，加上 `main.py run` 命令。先不接 judge，能产出 trace.json 和 pre + 逐步截图就行。Go agent 不需要改动——它已经是 HTTP daemon。
2. **LLM Judge**（~1 天）
   `judge.py` 实现纯函数 + 缓存；接 Anthropic 或 OpenRouter 的多模态 API。`main.py rejudge` 命令复用同一函数。
3. **报告 / 聚合**（~1 天）
   `metrics.py` + `report.py`：results.jsonl、manifest.json、summary.md、`main.py compare`。
4. **Suite 内容**（~2 天）
   写 `phone_control_v1.json` 的 15 个任务 + `perception_v1.json` 的 3 个诊断任务；每个任务的 rubric 需要在真机上跑几轮调出来。
5. **`scripts/aiden_benchmark.py` 跳板 + 文档更新**（~半天）
   旧入口转发到新 runner；`docs/BENCHMARK.md` 重写；`full_smoke.json` 标 deprecated。
6. **跑通端到端**（~1 天）
   真机上拉一遍完整 suite，对照人工复核调 rubric 直到 judge 判定基本一致。

总计大概 **6~8 个工作日**，主要不确定性在第 4 步（rubric 调试）和第 6 步（judge 一致性）。比之前估计少了 1 天——不需要改 agent 代码了。

## 七、风险点 / 待验证

- **Agent HTTP API 稳定性**：runner 依赖 `/api/chat` 返回的 `History` 字段结构。如果 Go agent 改了 response schema，trace 提取就坏。降级方案是 runner 做 schema 版本检查，不兼容时报错。
- **HOME 键能否回桌面**：global_reset 现在按 HOME。如果测试机的 USB HID HOME 不能可靠回桌面，要换成 touch_gesture swipe up 或多次 ESC。需要在真机上验证。
- **Judge 一致性**：判图模型对"是不是设置主页"这种问题的判定可能有 10~20% 边缘情况。需要在第 6 步用人工 spot check 校准 rubric 措辞。
- **META+C/V 组合键兼容性**：`copy_paste_text` 和 `select_all_and_delete` 依赖 META 组合键。部分 Android 厂商 ROM 可能不支持。如果测试机不支持，这些任务标 skipped。
- **Judge 费用**：每任务一次多模态调用（含两张截图）。15 任务一次 run 大约 15 次 judge 调用，单次 run 成本应该在 $0.2~$0.8 量级（看用哪个 judge 模型）。日常跑得起，重 rejudge 有缓存基本不花钱。

## 八、不在 v1 范围（v2 候选）

- 把每一步截图都给 judge 看（v1 只看 pre/post 终态）
- agent stdout 增加 `[usage]` 行做 token / 成本计量
- 跑前对手机起始状态做 baseline diff 校验
- 网页可视化报告
- 多台测试机并行

详细 spec 见 `docs/superpowers/specs/2026-05-21-agent-driven-benchmark-design.md`。
