# 自定义测试套件

在这里定义你自己的测试套件。

## 使用方法

### 1. 创建套件文件

```yaml
# my_tests.yaml
name: my_tests
description: 我的测试
tasks:
  - clock.CountAlarms
  - phone_control_v1.MakeCall
```

### 2. 运行套件

```bash
cd benchmark/mobilegym/docker
docker compose run --rm test --suite my_tests
```

## 套件格式

```yaml
name: suite_name
description: 套件描述
tasks:
  - task.id.1
  - task.id.2
```

## 示例套件

- `aiden_smoke.yaml` - 快速冒烟测试（3个任务）

## 内置套件

查看 MobileGym 内置套件：
```bash
docker compose run --rm test --help
```

常见内置套件：
- `phone_control_v1` - 电话控制
- `clock` - 闹钟操作
- `message_v1` - 消息管理
