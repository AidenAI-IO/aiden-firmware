# Named Model Providers

## Overview

`agent.toml` supports pre-configuring several model providers and switching
between them by editing a single line.

## Design

**Separation of concerns:**

- `[model_providers]` sections: define the available services (credentials, endpoint)
- `[model]` section: pick which provider to use and set model parameters

This mirrors Git remotes or Docker registries: declare the available services
first, then choose one.

## Configuration

### Basic usage

```toml
# 1. Define several providers
[model_providers.my-openai]
type = "openai"
api_key = "sk-xxx"

[model_providers.my-kimi]
type = "kimi"
api_key = "sk-yyy"

[model_providers.my-ollama]
type = "ollama"
base_url = "http://localhost:11434"

# 2. Pick one
[model]
provider = "my-openai"  # references [model_providers.my-openai] above
model = "gpt-4o"
temperature = 0.7
```

### Provider fields

| Field | Required | Description | Example |
|-------|----------|-------------|---------|
| `type` | Yes | Provider type | `"openai"`, `"kimi"`, `"ollama"`, ... |
| `api_key` | No | API key | `"sk-xxx"` |
| `token_env` | No | Environment variable holding the API key. Provider-only: `[model]` has no `token_env` | `"OPENAI_API_KEY"` |
| `base_url` | No | Custom endpoint; honored only for `openai` and `ollama` | `"https://api.openai.com/v1"` |

The `base_url` whitelist matches `[model]`: only `openai` and `ollama` accept an
override. For any other type the value is dropped at load, whether it is set on
`[model_providers.<name>]` or directly on `[model]`.

### Supported provider types

- `openai` - OpenAI official API
- `kimi` - Moonshot AI (international)
- `kimi-cn` - Moonshot AI (China)
- `volcengine` - Volcengine (Doubao)
- `openrouter` - OpenRouter
- `ollama` - Ollama local models
- `fake` - fake provider for tests

This list is enforced: a type outside it is rejected at load. Voice provider
types (`minimax`, `fish-audio`, `tencent-asr`, ...) are **not** accepted here.

### Voice providers use their own namespaces

`[tts]` and `[stt]` have the same named-record mechanism, in separate maps:
`[tts_providers.<name>]` and `[stt_providers.<name>]`. They are deliberately not
this map — the `[tts]` `volcengine` provider speaks a different protocol with its
own host and credentials than the Ark LLM provider of the same name, so a single
map could not serve both, and a shared type whitelist would let
`[model] provider = "minimax"` pass validation only to fail when the model client
is built.

See [Configuration](configuration.md) for the voice record fields and the
migration rules.

## Use cases

### Work and personal accounts

```toml
[model_providers.work]
type = "openai"
api_key = "sk-work-xxx"

[model_providers.personal]
type = "openai"
api_key = "sk-personal-xxx"

[model]
provider = "work"
model = "gpt-4o"

# Off the clock, change to: provider = "personal"
```

### Switching vendors

```toml
[model_providers.primary]
type = "kimi"
api_key = "sk-kimi-xxx"

[model_providers.backup]
type = "volcengine"
api_key = "sk-volcengine-xxx"

[model_providers.local]
type = "ollama"
base_url = "http://localhost:11434"

[model]
provider = "primary"   # kimi as the main provider
model = "moonshot-v1-32k"

# When needed, change to: provider = "backup" or "local"
```

### Development, staging, production

```toml
[model_providers.dev]
type = "ollama"
base_url = "http://localhost:11434"

[model_providers.staging]
type = "kimi"
api_key = "sk-staging-xxx"

[model_providers.prod]
type = "openai"
api_key = "sk-prod-xxx"

[model]
provider = "dev"       # local models while developing
model = "qwen2.5:14b"

# On deploy, change to: provider = "prod"
```

## Resolution rules

`[model].provider` resolves in this order:

1. If it matches the name of a `[model_providers.<name>]` section, that section is used.
2. Otherwise it is treated as a provider type (keeps older configs working).
3. If it matches neither, loading fails. A misspelled name, or a reference left
   behind after its section was deleted, is rejected at load instead of failing
   later when the model client is built.

Two edge cases worth knowing:

- **A section name shadows a provider type.** When a section is named after a
  built-in type, the section wins: `[model_providers.openai] type = "ollama"`
  makes `[model] provider = "openai"` resolve to **ollama**. Avoid such names.
- **Section names cannot nest.** Only a single level, `[model_providers.<name>]`, is
  supported, and names are limited to `[A-Za-z0-9_-]`.

## Advanced usage

### Overriding provider fields

Fields set on the model section win over the ones inherited from the provider:

```toml
[model_providers.my-openai]
type = "openai"
api_key = "sk-default-key"
base_url = "https://api.openai.com/v1"

[model]
provider = "my-openai"
model = "gpt-4o"
api_key = "sk-override-key"  # overrides the provider's api_key
# base_url is still inherited from the provider
```

`token_env` is not overridable this way: it exists only on the provider, so
the provider's value always applies.

### Reading keys from the environment

```toml
[model_providers.my-openai]
type = "openai"
token_env = "OPENAI_API_KEY"  # read from the environment

[model]
provider = "my-openai"
model = "gpt-4o"
```

## Backward compatibility

The old `[providers.<name>]` namespace and record-level `provider` field are
accepted when loading existing files. If old and canonical names are both
present, `[model_providers.<name>]` and `type` win. Saving always writes the
canonical namespace and field, so compatibility is read-only.

The traditional flat style also still works; defining named providers is
optional:

```toml
# Traditional form, no [model_providers] sections
[model]
provider = "openai"     # a provider type directly
model = "gpt-4o"
api_key = "sk-xxx"
```

## Adding providers from the web UI

The Model Providers card's **Add Provider** dialog asks for the provider, the API
key, an optional base URL, and the name, in that order.

- **Name** is filled in for you: the provider you pick, or the host label of the
  base URL when `openai` runs against a custom endpoint (`https://api.deepseek.com/v1`
  becomes `deepseek`). Edit it and your value sticks.
- Adding a second entry for a provider you already have suffixes the name
  (`openai`, `openai-2`, `openai-3`), so several keys for one provider coexist.
- **API Key** doubles as the environment-variable field: a value starting with
  `$` is stored as `token_env`, so `$OPENAI_API_KEY` reads the key from that
  variable at runtime. Anything else is stored as `api_key`.
- Renaming an existing provider rewrites the `provider` reference in `[model]`
  in the same save, so the reference cannot dangle.

## Switching providers

### From the web UI

The `provider` dropdown in the Model section lists your configured
`[model_providers.*]` sections, labelled `name (type)`, and nothing else: a bare
provider type carries no credentials, so it is not offered. Pick one, save, and
restart the agent. Selecting a provider also reloads the model list for its
underlying type.

The last entry is **+ Add Provider...**, which opens the same Add Provider
dialog as the Model Providers card. Save it and the new provider is selected for
you, so a first-time setup is one trip through the dropdown. Adding a provider
from the card's own button leaves your current selection alone.

If a config already points `[model] provider` at a bare type (the traditional
form above), that value stays in the dropdown so the config round-trips
untouched. Switch it to a named provider when you want credentials attached.

The model list is served by the agent daemon. While the agent is stopped the
selector shows a notice and you can still type a model ID by hand.

### By editing agent.toml

1. Edit the `provider` field in `agent.toml`
2. Adjust `model` if needed
3. Restart the agent service

```bash
vi /root/agent.toml
/etc/init.d/S99agent restart
```

## Verifying the active provider

```bash
tail -f /var/log/agent/agent.log | grep provider
```

## Errors

### Provider reference does not resolve

```toml
[model]
provider = "non-existent"  # neither a section name nor a provider type
model = "gpt-4o"
```

**Error:** `model.provider: unknown provider "non-existent"`

When other providers are configured, the message also lists them, so a typo is
easy to spot.

### Provider is missing its type

```toml
[model_providers.broken]
# no type field
api_key = "sk-xxx"

[model]
provider = "broken"
model = "gpt-4o"
```

**Error:** `model_providers.broken: provider type is required`

### Unsupported provider type

```toml
[model_providers.invalid]
type = "unknown-provider"
api_key = "sk-xxx"

[model]
provider = "invalid"
model = "gpt-4o"
```

**Error:** `model_providers.invalid: unsupported provider type "unknown-provider"`

## Best practices

1. **Naming:** use descriptive provider names
   - Good: `openai-work`, `kimi-main`, `ollama-local`
   - Avoid: `p1`, `test`, `xxx`

2. **Credentials:** prefer environment variables for keys
   ```toml
   [model_providers.secure]
   type = "openai"
   token_env = "OPENAI_API_KEY"
   ```

3. **Comments:** annotate each provider
   ```toml
   [model_providers.work]
   type = "openai"
   api_key = "sk-xxx"
   # Work account, quota: 10M tokens/month
   ```

4. **Backups:** save the config before switching
   ```bash
   cp /root/agent.toml /root/agent.toml.bak
   ```

## Planned work

- Runtime switching without a restart
- Per-provider usage and cost tracking
