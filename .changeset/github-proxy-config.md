---
"aiden-firmware": minor
---

feat(ota): add GitHub proxy configuration for accelerated downloads

Added support for configuring GitHub proxy services to improve OTA download reliability in regions with poor GitHub connectivity.

**Features:**
- New `github_proxy_url` configuration field in both OTA config (`/userdata/ota/config.json`) and agent config (`agent.toml`)
- Automatic URL transformation for all GitHub domains (github.com, api.github.com, raw.githubusercontent.com, objects.githubusercontent.com)
- Support for popular proxy services (gh-proxy.com, ghfast.top) and custom proxies
- Non-GitHub URLs remain unaffected

**Configuration:**

OTA config:
```json
{
  "github_proxy_url": "https://gh-proxy.com/"
}
```

Agent config:
```toml
[ota]
github_proxy_url = "https://gh-proxy.com/"
```

**Documentation:**
- Added `docs/08-ota/ota-github-proxy.md` with usage examples and troubleshooting guide
