# 自定义测试套件

> ⚠️ **当前限制：** `run_aiden.py` 直接把 `--suite` 名字交给 MobileGym 内置 registry 解析，所以这个目录下的自定义 YAML 文件**目前不会被加载**。要跑自定义任务集合，请用 `--task-id` 一次列举多个，或选用内置 suite（`clock`, `phone_control_v1`, `calendar`, `wechat`, ...）。
>
> 如果想恢复 YAML suite 加载，需要在 `run_aiden.py` 的 `_load_tasks` 路径加一段：先尝试从 `/app/suites/<name>.yaml` 读 `tasks:` 列表展开成 `--task-id` 集合，再调 MobileGym。

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
