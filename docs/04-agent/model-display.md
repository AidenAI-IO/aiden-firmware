# Model Display

## Overview

Model display provides the config web UI with a per-provider model list carrying
bilingual descriptions, so the configuration page can offer a pick list instead
of a free-text model field.

## Design principles

1. **The name is the ID.** A model's display name is exactly its config value,
   so there is nothing to map back.
2. **Localized descriptions.** Descriptions cover the locales aiden already
   supports (`zh-CN`, `en-US`).
3. **Recommended models.** Each provider marks a recommended model.
4. **Grouped by provider.** The list is organized by provider for the UI.

## Data structures

### ModelDisplayInfo

```go
type ModelDisplayInfo struct {
    ID           string            // model ID, also the display name
    Descriptions map[string]string // locale -> description
    Recommended  bool
}
```

### LocalizedModelInfo

```go
type LocalizedModelInfo struct {
    ID          string // model ID
    Description string // description for the requested locale
    Recommended bool
}
```

## API

### GetDisplayModelsForProvider

Returns the raw model list for a provider:

```go
models := GetDisplayModelsForProvider("openai")
// []ModelDisplayInfo
```

### GetLocalizedModelsForProvider

Returns the localized model list for a provider:

```go
models := GetLocalizedModelsForProvider("openai", "zh-CN")
// []LocalizedModelInfo, descriptions already resolved
```

### ModelDisplayInfo.Localized

Localizes a single entry:

```go
model := ModelDisplayInfo{...}
localized := model.Localized("zh-CN")
// LocalizedModelInfo
```

## Usage

### From the config web API

`GET /api/models?provider=<name>&locale=<locale>` is served by `handleModels`.
When `locale` is omitted it falls back to the configured locale, and then to
`en-US`:

```go
func handleGetModels(w http.ResponseWriter, r *http.Request) {
    provider := r.URL.Query().Get("provider")
    locale := r.URL.Query().Get("locale")

    models := GetLocalizedModelsForProvider(provider, locale)

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "provider": provider,
        "models":   models,
    })
}
```

An unknown provider returns an empty list rather than an error.

**Response for `locale=zh-CN`:**

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

**Response for `locale=en-US`:**

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

The Chinese strings above are sample data, not documentation prose: they are the
`zh-CN` descriptions this endpoint actually returns.

### From the frontend

```html
<select id="provider" onchange="loadModels()">
  <option value="openai">OpenAI</option>
  <option value="kimi">Kimi</option>
  <option value="ollama">Ollama</option>
</select>

<div id="models">
  <!-- populated dynamically -->
</div>

<script>
async function loadModels() {
  const provider = document.getElementById('provider').value;

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

Omitting `locale` lets the server apply the configured one, which is usually
what the UI wants.

## Currently listed models

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

## Adding a model

Add an entry to `displayModelsByProvider` in `model_display.go`:

```go
var displayModelsByProvider = map[string][]ModelDisplayInfo{
    "openai": {
        // ... existing models ...
        {
            ID: "gpt-6.0",
            Descriptions: map[string]string{
                localeEnglishUS:         "Next generation model",
                localeSimplifiedChinese: "下一代模型",
            },
            Recommended: false,
        },
    },
}
```

The `Descriptions` map holds user-facing copy per locale, so the
`localeSimplifiedChinese` value is Chinese by definition.

## Tests

```bash
go test -v -run TestModelDisplay ./internal/agent/
```

Covered:

- Fetching the model list per provider
- Description lookup and locale fallback
- Localizing a single entry
- Every model has an English description
- Every provider has a recommended model

## Notes

1. **Name consistency.** A model ID must match the value used in the config file
   exactly.
2. **English description required.** It is the fallback for any unknown locale.
3. **Chinese description recommended.** Preferred for every model.
4. **Recommended flag.** Each provider should mark at least one model.
5. **No enforced consistency.** The display list and `modelSpecRegistry` are
   maintained independently and not cross-validated.

## Relationship to modelSpecRegistry

- `modelSpecRegistry` holds technical specs (context window, max output) used at
  runtime.
- `displayModelsByProvider` holds UI copy (description, recommended) used by the
  config web.

The two are maintained independently, with no enforced consistency. If a model
absent from `modelSpecRegistry` is selected, the runtime falls back to defaults
or to the overrides set in the config.
