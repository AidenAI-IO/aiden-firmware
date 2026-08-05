# Model Provider 多配置支持

## 概述

从此版本开始，agent.toml 支持预配置多个 model provider，并通过修改一行配置快速切换。

## 设计理念

**关注点分离：**
- `[providers]` 部分：定义可用的服务提供商（包含认证信息、endpoint 等）
- `[model]` 部分：选择使用哪个 provider 和配置模型参数

类似于 Git 的 remote 或 Docker 的 registry，先定义可用的服务，再选择使用哪个。

## 配置结构

### 基本用法

```toml
# 1. 定义多个 providers
[providers.my-openai]
provider = "openai"
api_key = "sk-xxx"

[providers.my-kimi]
provider = "kimi"
api_key = "sk-yyy"

[providers.my-ollama]
provider = "ollama"
base_url = "http://localhost:11434"

# 2. 选择使用哪个 provider
[model]
provider = "my-openai"  # 引用上面定义的 provider
model = "gpt-4o"
temperature = 0.7
```

### Provider 配置字段

每个 provider 支持以下字段：

| 字段 | 必填 | 说明 | 示例 |
|------|------|------|------|
| `provider` | 是 | Provider 类型 | `"openai"`, `"kimi"`, `"ollama"` 等 |
| `api_key` | 否 | API 密钥 | `"sk-xxx"` |
| `token_env` | 否 | API 密钥的环境变量名 | `"OPENAI_API_KEY"` |
| `base_url` | 否 | 自定义 API endpoint，仅 `openai` 和 `ollama` 生效 | `"https://api.openai.com/v1"` |

`base_url` 的白名单和 `[model]` 一致：只有 `openai` 和 `ollama` 接受覆盖，
其他类型即使写了也会在加载时被丢弃（无论写在 `[providers.<name>]` 还是
`[model]` 上）。

### 支持的 Provider 类型

- `openai` - OpenAI 官方 API
- `kimi` - Moonshot AI (国际版)
- `kimi-cn` - Moonshot AI (中国版)
- `volcengine` - 火山引擎 (豆包)
- `openrouter` - OpenRouter
- `ollama` - Ollama 本地模型
- `fake` - 测试用假 provider

## 使用场景

### 场景 1: 工作/个人账号切换

```toml
[providers.work]
provider = "openai"
api_key = "sk-work-xxx"

[providers.personal]
provider = "openai"
api_key = "sk-personal-xxx"

[model]
provider = "work"      # 工作时间
model = "gpt-4o"

# 下班后改成: provider = "personal"
```

### 场景 2: 多供应商快速切换

```toml
[providers.primary]
provider = "kimi"
api_key = "sk-kimi-xxx"

[providers.backup]
provider = "volcengine"
api_key = "sk-volcengine-xxx"

[providers.local]
provider = "ollama"
base_url = "http://localhost:11434"

[model]
provider = "primary"   # 主力：kimi
model = "moonshot-v1-32k"

# 需要时改成: provider = "backup" 或 "local"
```

### 场景 3: 开发/测试/生产环境

```toml
[providers.dev]
provider = "ollama"
base_url = "http://localhost:11434"

[providers.staging]
provider = "kimi"
api_key = "sk-staging-xxx"

[providers.prod]
provider = "openai"
api_key = "sk-prod-xxx"

[model]
provider = "dev"       # 开发环境用本地
model = "qwen2.5:14b"

# 部署时改成: provider = "prod"
```

### 场景 4: model 和 model_text 使用不同 provider

```toml
[providers.openai-main]
provider = "openai"
api_key = "sk-openai-xxx"

[providers.kimi-fast]
provider = "kimi"
api_key = "sk-kimi-xxx"

[model]
provider = "openai-main"  # 语音模式用 OpenAI
model = "gpt-4o"

[model_text]
provider = "kimi-fast"    # 文字模式用 Kimi
model = "moonshot-v1-8k"
```

## 解析规则

`[model].provider` 的取值按以下顺序解析：

1. 如果匹配某个 `[providers.<name>]` 的名字，就使用该 section；
2. 否则当作 provider 类型处理（向后兼容旧配置）；
3. 两者都不匹配则报错 —— 拼错的名字或删掉 section 后残留的引用会在加载时
   被拒绝，而不是等到真正调用模型时才失败。

两条需要注意的边界：

- **名字遮蔽类型**：section 名和内置类型同名时，section 优先。
  `[providers.openai] provider = "ollama"` 会让 `[model] provider = "openai"`
  解析成 **ollama**。不建议这样命名。
- **section 名不能嵌套**：只支持 `[providers.<name>]` 一层，且名字限于
  `[A-Za-z0-9_-]`。

## 高级用法

### 覆盖 Provider 配置

model 配置中的字段会覆盖 provider 中的配置：

```toml
[providers.my-openai]
provider = "openai"
api_key = "sk-default-key"
base_url = "https://api.openai.com/v1"

[model]
provider = "my-openai"
model = "gpt-4o"
api_key = "sk-override-key"  # 覆盖 provider 中的 api_key
# base_url 仍使用 provider 中的配置
```

### 使用环境变量

```toml
[providers.my-openai]
provider = "openai"
token_env = "OPENAI_API_KEY"  # 从环境变量读取

[model]
provider = "my-openai"
model = "gpt-4o"
```

## 向后兼容

旧的配置方式仍然有效，不需要定义 providers：

```toml
# 传统方式（不使用 providers）
[model]
provider = "openai"     # 直接指定 provider 类型
model = "gpt-4o"
api_key = "sk-xxx"
```

## 切换步骤

要切换 provider，只需：

1. 修改 `agent.toml` 中的 `provider` 字段
2. 根据需要修改 `model` 字段
3. 重启 agent 服务

```bash
# 编辑配置
vi /root/agent.toml

# 重启服务
/etc/init.d/S99agent restart
```

## 验证配置

启动后检查日志确认使用的 provider：

```bash
tail -f /var/log/agent/agent.log | grep provider
```

## 错误处理

### Provider 不存在

```toml
[model]
provider = "non-existent"  # 这个 provider 没有定义
model = "gpt-4o"
```

**行为：** 当作直接的 provider 类型（向后兼容），如果类型也不支持则报错。

### Provider 缺少必填字段

```toml
[providers.broken]
# 缺少 provider 字段
api_key = "sk-xxx"

[model]
provider = "broken"
model = "gpt-4o"
```

**错误：** `model: provider "broken" has no provider type specified`

### 不支持的 Provider 类型

```toml
[providers.invalid]
provider = "unknown-provider"
api_key = "sk-xxx"

[model]
provider = "invalid"
model = "gpt-4o"
```

**错误：** `providers.invalid: unsupported provider type "unknown-provider"`

## 最佳实践

1. **命名规范：** 使用描述性的 provider 名称
   - ✅ `openai-work`, `kimi-main`, `ollama-local`
   - ❌ `p1`, `test`, `xxx`

2. **密钥管理：** 敏感信息建议使用环境变量
   ```toml
   [providers.secure]
   provider = "openai"
   token_env = "OPENAI_API_KEY"
   ```

3. **注释说明：** 为每个 provider 添加注释
   ```toml
   [providers.work]
   provider = "openai"
   api_key = "sk-xxx"
   # 工作账号，配额：10M tokens/月
   ```

4. **备份配置：** 切换前备份当前配置
   ```bash
   cp /root/agent.toml /root/agent.toml.bak
   ```

## 未来规划

后续版本将支持：
- Config Web UI 中的可视化 provider 切换
- 运行时动态切换（无需重启）
- Provider 使用统计和成本跟踪
