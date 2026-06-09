# 自定义测试套件

在这里定义你自己的测试套件，格式与 MobileGym 内置套件相同。

## 使用方法

### 1. 创建套件文件

```bash
# 创建自定义套件
cat > benchmark/mobilegym/suites/my_suite.yaml <<'YAML'
name: my_custom_suite
description: 我的自定义测试套件
tasks:
  - clock.CountAlarms
  - clock.ToggleAlarm
  - phone_control_v1.MakeCall
YAML
```

### 2. 运行套件

```bash
cd benchmark/mobilegym/docker

# 运行自定义套件
docker compose run --rm test \
  --suite my_custom_suite \
  --suite-dir /app/suites

# 或运行内置套件
docker compose run --rm test --suite phone_control_v1
```

## 套件格式

```yaml
name: suite_name
description: 套件描述
tasks:
  - task.id.1
  - task.id.2
  # ...
```

## 内置套件列表

在容器内查看：
```bash
docker compose run --rm test --list-suites
```

常见内置套件：
- `phone_control_v1` - 电话控制
- `clock` - 闹钟操作
- `message_v1` - 消息管理
- `memory_v1` - 记忆测试
- `calendar` - 日历事件
