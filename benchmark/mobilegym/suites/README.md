# 自定义测试套件

Aiden benchmark suite 使用 `benchmark/suites/<name>.json`。MobileGym 作为
environment bridge 运行时，benchmark WebUI 会列出这些 Aiden JSON suite，并通过
`benchmark-task-id` 把并发 task worker 路由到同一个 MobileGym instance 内的不同 env。
本目录下的 YAML 暂不被 benchmark runner 加载。

## 当前可工作的方式

### 方式 1：列举 task-id

```bash
docker compose run --rm test \
  --task-id clock.CountAlarms \
  --task-id clock.ToggleAlarm \
  --task-id phone_control_v1.MakeCall \
  --aiden-control-token "$(cat ../config/control_token)"
```

### 方式 2：用内置 suite + `--limit`

```bash
docker compose run --rm test --suite clock --limit 5
docker compose run --rm test --suite phone_control_v1 --limit 3
```

### 方式 3：Benchmark WebUI 并发

使用以下命令启动 benchmark WebUI：

```bash
cd benchmark
uv run python -m runner webui
```

创建 MobileGym
environment 时把 `Envs` 设为大于 1，随后选择该 environment 运行 Aiden JSON suite。
WebUI 会为并发 task worker 启动独立 daemon，并通过 `benchmark-task-id` 路由到同一
MobileGym instance 内的不同 env。

## YAML 格式（保留供未来加载逻辑使用）

```yaml
name: suite_name
description: 套件描述
tasks:
  - task.id.1
  - task.id.2
```

## 内置 suite 列表

参考 MobileGym registry：`account, alipay, bilibili, calendar, clock, crossapp_*, ebay, file_manager, launcher, map, notes, payment, railway12306, redbook, reddit, sms, spotify, tencent_meeting, weather, wechat, wechat_reading, x`

## 示例文件

- `aiden_smoke.yaml` — 历史示例，**当前不能直接用 `--suite aiden_smoke` 跑**，仅作为任务清单参考
