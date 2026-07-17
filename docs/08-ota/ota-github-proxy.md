# OTA GitHub Proxy Configuration

## Overview

When downloading OTA updates from GitHub, network connectivity can be unstable in some regions. The OTA system now supports configuring GitHub proxy services to accelerate downloads.

## Configuration

### Option 1: OTA Config File (`/userdata/ota/config.json`)

Add the `github_proxy_url` field to your OTA configuration:

```json
{
  "github_proxy_url": "https://gh-proxy.com/"
}
```

### Option 2: Agent Config File (`agent.toml`)

Add the OTA section to your agent configuration:

```toml
[ota]
github_proxy_url = "https://gh-proxy.com/"
```

## Supported Proxy Services

### Pre-configured Options

1. **gh-proxy.com**
   ```json
   {"github_proxy_url": "https://gh-proxy.com/"}
   ```

2. **ghfast.top**
   ```json
   {"github_proxy_url": "https://ghfast.top/"}
   ```

### Custom Proxy

You can use any GitHub proxy service that follows the standard pattern:

```text
https://your-proxy.example.com/https://github.com/...
```

Example:
```json
{
  "github_proxy_url": "https://my-proxy.example.com/"
}
```

## How It Works

When `github_proxy_url` is configured:

1. The OTA updater detects GitHub URLs (github.com, api.github.com, raw.githubusercontent.com, objects.githubusercontent.com)
2. It prepends the proxy URL to the original GitHub URL
3. Downloads are fetched through the proxy service

Example transformation:
```
Original: https://github.com/AidenAI-IO/aiden-firmware/releases/download/v1.0.0/update.tar.gz
With proxy: https://gh-proxy.com/https://github.com/AidenAI-IO/aiden-firmware/releases/download/v1.0.0/update.tar.gz
```

Non-GitHub URLs are not affected by this setting.

## Disabling Proxy

To disable the proxy, remove the `github_proxy_url` field or set it to an empty string:

```json
{
  "github_proxy_url": ""
}
```

## Troubleshooting

If downloads fail with a proxy configured:

1. Verify the proxy URL is accessible from your device
2. Check that the proxy URL ends with a `/` (it will be added automatically if missing)
3. Try a different proxy service
4. Temporarily disable the proxy to verify it's not a GitHub connectivity issue

## Security Note

Proxy services can see the URLs being accessed. Only use trusted proxy services, especially for production deployments.
