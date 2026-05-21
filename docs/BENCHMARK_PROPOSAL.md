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

把 benchmark 重新定位为"agent 能力评测"，所有任务一律通过 `agent_main --mode=text` 执行。Runner 只负责搭测试环境、抓副产物、判分。

调用链：

```
runner (Python，本机执行)
  ├── 全局 reset      (固定 tool 序列，把手机弄回主屏)
  ├── per-task setup  (可选，用来构造非主屏起点；setup 失败 → 任务 skipped)
  ├── pre 截图
  ├── agent_main --mode=text --once   ← 唯一被评测的进程
  │     stdin: 任务 prompt
  │     stdout: trace 行
  ├── post 截图
  ├── 硬断言        (返回码、tool call 数下限/上限、超时、post 截图存在)
  └── LLM judge     (吃 artifact，不碰硬件，可离线重跑)
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

1. **任务集 v1 共 10 个主任务 + 2 个诊断任务**，覆盖 single_step / multi_step / recovery 三类。任务清单见 spec 第 "Task Set v1"。
2. **状态隔离**：每任务前都跑全局 reset 回主屏；recovery 类任务在 reset 后再用 tool 序列把手机推到非主屏起点。
3. **Agent 一次性退出**：给 `agent_main` 加 `--once` 开关，跑完一个 prompt 自然退出。Runner 不用 SIGTERM 收尾，干净很多。改动很小，集中在 `src/agent_main.cpp` 的 MODE_TEXT 循环里。
4. **Judge 离线可重跑**：`rejudge --run-dir runs/<id>` 命令只读取已有 artifact 重跑 judge，不碰硬件。改 rubric 或换 judge 模型时不需要重测一次手机。
5. **跨 run 对比**：`compare --runs A B` 输出两次 run 之间哪些任务 flip、效率指标差多少。这是回归检测的核心。
6. **不上 docker**：硬件在环（HID 设备、frame_service socket 都在测试机本机），docker 化只增加复杂度不增加隔离。Python 依赖用 venv + requirements.txt 锁版本即可。

## 五、目录结构

```
benchmark/
├── runner/              # Python 包，新写
│   ├── main.py          # CLI: run | rejudge | compare
│   ├── suite.py         # 加载/校验 suite
│   ├── reset.py         # 全局 reset + per-task setup
│   ├── agent_session.py # spawn agent_main，捕获 stdout，超时管理
│   ├── capture.py       # pre/post 截图
│   ├── trace.py         # 解析 stdout 成结构化 tool call 序列
│   ├── assertions.py    # 硬断言
│   ├── judge.py         # LLM judge（纯函数，吃 artifact 写 judge.json）
│   ├── metrics.py       # 效率指标聚合
│   └── report.py        # JSONL + summary.md
├── suites/
│   ├── phone_control_v1.json   # 主 suite，10 个任务
│   └── perception_v1.json      # 诊断子 suite，2 个任务
└── runs/<run_id>/
    ├── manifest.json    # 整次 run 的环境信息（git sha、模型、suite 版本）
    ├── results.jsonl    # 每任务一行，机器读
    ├── summary.md       # 人读摘要
    └── tasks/<task_id>/
        ├── pre.png      # 执行前截图
        ├── post.png     # 执行后截图
        ├── agent_stdout.log
        ├── agent_stderr.log
        ├── trace.json   # 解析后的 tool call 序列
        └── judge.json   # judge 的逐 rubric 输出 + 理由
```

## 六、实施步骤（粗略工期）

按依赖顺序：

1. **`agent_main --once`**（小改动，~1 天）
   在 `src/agent_main.cpp` MODE_TEXT 分支加一个开关，处理完一个 prompt 后 exit。其他 mode 不动。
2. **Runner 骨架**（~2 天）
   `suite.py` / `reset.py` / `capture.py` / `agent_session.py` / `trace.py` / `assertions.py`，加上 `main.py run` 命令，先不接 judge，能产出 trace.json 和 pre/post 截图就行。
3. **LLM Judge**（~1 天）
   `judge.py` 实现纯函数 + 缓存；接 Anthropic 或 OpenRouter 的多模态 API。`main.py rejudge` 命令复用同一函数。
4. **报告 / 聚合**（~1 天）
   `metrics.py` + `report.py`：results.jsonl、manifest.json、summary.md、`main.py compare`。
5. **Suite 内容**（~1~2 天）
   写 `phone_control_v1.json` 的 10 个任务；每个任务的 rubric 需要在真机上跑几轮调出来。
6. **`scripts/aiden_benchmark.py` 跳板 + 文档更新**（~半天）
   旧入口转发到新 runner；`docs/BENCHMARK.md` 重写；`full_smoke.json` 标 deprecated。
7. **跑通端到端**（~1 天）
   真机上拉一遍完整 suite，对照人工复核调 rubric 直到 judge 判定基本一致。

总计大概 **7~10 个工作日**，主要不确定性在第 5 步（rubric 调试）和第 7 步（judge 一致性）。

## 七、风险点 / 待验证

- **`agent_main` stdout 格式**：trace 解析依赖 `[tool] name(args)`、`[tools] Executing N` 等行的稳定性。一旦改了日志格式 trace 就坏。降级方案是后续在 `agent_main.cpp` 加一行 JSON 格式的 `[trace] {...}`，做机器可读旁路。
- **HOME 键能否回桌面**：global_reset 现在按 HOME。如果测试机的 USB HID HOME 不能可靠回桌面，要换成 swipe up 或多次 ESC。需要在真机上验证。
- **Judge 一致性**：判图模型对"是不是设置主页"这种问题的判定可能有 10~20% 边缘情况。需要在第 7 步用人工 spot check 校准 rubric 措辞。
- **锁屏 / 全屏弹窗能否稳定模拟**：`recover_from_blocked_state` 任务依赖能造出"无法操作的页面"。如果不行就降级成普通全屏 modal。
- **Judge 费用**：每任务一次多模态调用（含两张截图）。10 任务一次 run 大约 10 次 judge 调用，单次 run 成本应该在 $0.1~$0.5 量级（看用哪个 judge 模型）。日常跑得起，重 rejudge 有缓存基本不花钱。

## 八、不在 v1 范围（v2 候选）

- 把每一步截图都给 judge 看（v1 只看 pre/post 终态）
- agent stdout 增加 `[usage]` 行做 token / 成本计量
- 跑前对手机起始状态做 baseline diff 校验
- 网页可视化报告
- 多台测试机并行

详细 spec 见 `docs/superpowers/specs/2026-05-21-agent-driven-benchmark-design.md`。
