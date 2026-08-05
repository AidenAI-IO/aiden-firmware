# Model Display 功能文档

## 概述

Model Display 功能为 config_web UI 提供了按 provider 分组的模型列表，支持中英文双语描述，方便用户在配置界面选择合适的模型。

## 设计原则

1. **名称即 ID**：模型的显示名称与配置 ID 完全一致，减少理解成本
2. **多语言支持**：描述信息支持中英文，与 aiden 的 locale 系统对齐（`zh-CN`、`en-US`）
3. **推荐标识**：为每个 provider 标记推荐的模型
4. **按 provider 分组**：模型列表按 provider 组织，便于 UI 展示

## 数据结构

### ModelDisplayInfo

```go
type ModelDisplayInfo struct {
    ID           string            // 模型 ID（同时作为显示名称）
    Descriptions map[string]string // locale -> description
    Recommended  bool              // 是否推荐
}
```

### LocalizedModelInfo

```go
type LocalizedModelInfo struct {
    ID          string // 模型 ID
    Description string // 本地化后的描述
    Recommended bool   // 是否推荐
}
```

## API 接口

### GetDisplayModelsForProvider

获取指定 provider 的原始模型列表：

```go
models := GetDisplayModelsForProvider("openai")
// 返回：[]ModelDisplayInfo
```

### GetLocalizedModelsForProvider

获取指定 provider 的本地化模型列表：

```go
models := GetLocalizedModelsForProvider("openai", "zh-CN")
// 返回：[]LocalizedModelInfo，描述已本地化
```

### ModelDisplayInfo.Localized

将单个模型信息本地化：

```go
model := ModelDisplayInfo{...}
localized := model.Localized("zh-CN")
// 返回：LocalizedModelInfo
```

## 使用示例

### 在 Config Web API 中使用

```go
func handleGetModels(w http.ResponseWriter, r *http.Request) {
    provider := r.URL.Query().Get("provider")
    locale := r.Header.Get("Accept-Language") // 或从配置获取
    
    // 获取本地化的模型列表
    models := GetLocalizedModelsForProvider(provider, locale)
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "provider": provider,
        "models":   models,
    })
}
```

**返回示例（zh-CN）：**
```json
{
  "provider": "openai",
  "models": [
    {
      "id": "gpt-5.5",
      "description": "最新旗舰模型，100万+ 上下文",
      "recommended": true
    },
    {
      "id": "gpt-5.4-mini",
      "description": "快速且经济的小模型",
      "recommended": false
    }
  ]
}
```

**返回示例（en-US）：**
```json
{
  "provider": "openai",
  "models": [
    {
      "id": "gpt-5.5",
      "description": "Latest flagship model with 1M+ context",
      "recommended": true
    },
    {
      "id": "gpt-5.4-mini",
      "description": "Fast and economical small model",
      "recommended": false
    }
  ]
}
```

### 在前端 UI 中使用

```html
<select id="provider" onchange="loadModels()">
  <option value="openai">OpenAI</option>
  <option value="kimi">Kimi</option>
  <option value="ollama">Ollama</option>
</select>

<div id="models">
  <!-- 动态加载 -->
</div>

<script>
async function loadModels() {
  const provider = document.getElementById('provider').value;
  const locale = 'zh-CN'; // 从配置获取
  
  const response = await fetch(`/api/models?provider=${provider}`);
  const data = await response.json();
  
  const modelsDiv = document.getElementById('models');
  modelsDiv.innerHTML = data.models.map(m => `
    <label>
      <input type="radio" name="model" value="${m.id}">
      ${m.id} ${m.recommended ? '⭐' : ''}
      <br><small>${m.description}</small>
    </label>
  `).join('');
}
</script>
```

**渲染效果：**
```
○ gpt-5.5 ⭐
  最新旗舰模型，100万+ 上下文

○ gpt-5.4-mini
  快速且经济的小模型

○ 自定义输入: [_________________]
```

## 当前支持的 Provider

### openai
- gpt-5.5 ⭐
- gpt-5.4
- gpt-5.4-mini
- gpt-5.4-nano
- gpt-4o
- gpt-4o-mini

### kimi / kimi-cn
- kimi-k3 ⭐

### volcengine
- doubao-seed-2-1-pro-260628 ⭐

### openrouter
- anthropic/claude-opus-4.8 ⭐
- anthropic/claude-sonnet-4.6
- google/gemini-3.5-pro
- google/gemini-3.5-flash

### ollama
- qwen2.5:14b ⭐
- qwen2.5:7b
- llama3.1:8b
- llama3.1:70b

## 添加新模型

在 `model_display.go` 的 `displayModelsByProvider` 中添加：

```go
var displayModelsByProvider = map[string][]ModelDisplayInfo{
    "openai": {
        // ... 现有模型 ...
        {
            ID: "gpt-6.0", // 新模型
            Descriptions: map[string]string{
                localeEnglishUS:         "Next generation model",
                localeSimplifiedChinese: "下一代模型",
            },
            Recommended: false,
        },
    },
}
```

## 测试

运行测试验证：

```bash
go test -v -run TestModelDisplay ./internal/agent/
```

测试覆盖：
- ✅ 按 provider 获取模型列表
- ✅ 多语言描述获取和回退
- ✅ 模型本地化
- ✅ 所有模型都有英文描述
- ✅ 每个 provider 都有推荐模型

## 注意事项

1. **名称一致性**：模型 ID 必须与配置文件中使用的值完全一致
2. **英文描述必填**：所有模型必须提供英文描述（作为回退）
3. **中文描述推荐**：建议为所有模型提供中文描述
4. **推荐标识**：每个 provider 至少应有一个推荐模型
5. **不强制一致性**：display 列表与 modelSpecRegistry 独立维护，不做强制校验

## 与 modelSpecRegistry 的关系

- `modelSpecRegistry`：存储模型的技术规格（上下文窗口、最大输出等），用于运行时
- `displayModelsByProvider`：存储模型的 UI 展示信息（描述、推荐），用于 config_web

两者独立维护，没有强制一致性要求。如果用户在 UI 中选择了不在 `modelSpecRegistry` 中的模型，系统会使用默认值或配置的覆盖值。
