# 自定义测试套件

`run_aiden.py --aiden-suite <name>` 会从 `benchmark/suites/<name>.json` 加载 Aiden JSON
suite 并即时转换为 MobileGym Task。Web UI（`/benchmark`，MobileGym 模式）也会列出
这些 suite。本目录下的 YAML 暂不被加载。

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

### 方式 3：并发隔离运行

```bash
cd ../docker
./parallel_run.sh clock.CountAlarms clock.ToggleAlarm phone_control_v1.MakeCall
```

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
