# Config Web OTA Integration Guide

## Overview

This document describes how the OTA GitHub proxy configuration is integrated into the config_web UI.

## Architecture

### Backend (Go Agent)

1. **Config Metadata** (`src/agent/internal/agent/config_meta.go`)
   - Defines the `ota` section with two fields:
     - `github_proxy_url`: Select widget with predefined options
     - `github_proxy_url_custom`: Text input for custom URLs (conditionally visible)

2. **Config Storage** (`agent.toml`)
   ```toml
   [ota]
   github_proxy_url = "https://gh-proxy.com/"
   ```

3. **API Endpoints**
   - `GET /api/config-meta` - Returns field metadata with enum options
   - `GET /api/config` - Returns current config values
   - `POST /api/config` - Saves updated config

### Frontend (HTML/JavaScript)

The config_web UI dynamically renders form fields based on metadata:

1. **Metadata-Driven Rendering**
   - Fetch metadata from `/api/config-meta`
   - For each section, render fields according to their `Widget` type
   - Select widgets use the `Enum` options from metadata
   - Conditional visibility based on `VisibleWhen` rules

2. **OTA Section UI**
   ```html
   <div class="section-card" data-section="ota">
     <h3>OTA Configuration</h3>
     
     <div class="field">
       <label>GitHub Proxy</label>
       <select id="ota-github_proxy_url">
         <option value="">Direct connection (no proxy)</option>
         <option value="https://gh-proxy.com/">gh-proxy.com</option>
         <option value="https://ghfast.top/">ghfast.top</option>
         <option value="custom">Custom proxy URL</option>
       </select>
       <div class="field-hint">
         Use a proxy to accelerate GitHub downloads in regions with poor connectivity
       </div>
     </div>
     
     <div class="field" data-visible-when="ota.github_proxy_url == 'custom'">
       <label>Custom Proxy URL</label>
       <input type="text" 
              id="ota-github_proxy_url_custom"
              placeholder="https://your-proxy.example.com/" />
       <div class="field-hint">
         Enter the full URL of your GitHub proxy service
       </div>
     </div>
   </div>
   ```

3. **Value Transformation**
   When saving:
   - If `github_proxy_url == "custom"`, use the value from `github_proxy_url_custom`
   - Otherwise, use the selected proxy URL directly
   
   When loading:
   - If the value matches a predefined option, select it
   - Otherwise, set `github_proxy_url = "custom"` and populate `github_proxy_url_custom`

## Implementation Flow

### 1. User Opens Config Web
1. Browser requests `/api/config-meta`
2. config_web calls `agent config-meta --format=json`
3. Agent returns metadata including OTA section with enum options
4. UI renders OTA section with dropdown

### 2. User Selects Proxy
1. User selects "gh-proxy.com" from dropdown
2. JavaScript updates form state
3. User clicks "Save"
4. POST `/api/config` with `{"ota": {"github_proxy_url": "https://gh-proxy.com/"}}`
5. config_web writes to `agent.toml`

### 3. User Selects Custom Proxy
1. User selects "Custom proxy URL"
2. Conditional field appears (via `visibleWhen` rule)
3. User enters custom URL: `https://my-proxy.example.com/`
4. User clicks "Save"
5. Transform logic converts to: `{"ota": {"github_proxy_url": "https://my-proxy.example.com/"}}`

### 4. OTA Uses Configuration
1. OTA updater loads `agent.toml` or `/userdata/ota/config.json`
2. Reads `github_proxy_url` value
3. Applies proxy to all GitHub URLs during download

## Benefits of Metadata-Driven Approach

1. **No Hardcoded URLs in Frontend**
   - Proxy options defined in Go code
   - Easy to add/remove options without touching HTML

2. **Type Safety**
   - Metadata schema enforced in Go
   - Frontend validation based on metadata

3. **Single Source of Truth**
   - Go code defines both config structure and UI metadata
   - No drift between backend validation and frontend rendering

4. **Easy Testing**
   - Test metadata generation: `agent config-meta --format=json`
   - Test config roundtrip: `agent config --config agent.toml`

## Example Metadata Output

```json
{
  "sections": [
    {
      "name": "ota",
      "fields": [
        {
          "key": "github_proxy_url",
          "widget": "select",
          "enum": [
            {
              "value": "",
              "label": "Direct connection (no proxy)"
            },
            {
              "value": "https://gh-proxy.com/",
              "label": "gh-proxy.com"
            },
            {
              "value": "https://ghfast.top/",
              "label": "ghfast.top"
            },
            {
              "value": "custom",
              "label": "Custom proxy URL"
            }
          ],
          "default": ""
        },
        {
          "key": "github_proxy_url_custom",
          "widget": "text",
          "visibleWhen": {
            "all": [
              {
                "field": "ota.github_proxy_url",
                "op": "eq",
                "value": "custom"
              }
            ]
          }
        }
      ]
    }
  ]
}
```

## Future Enhancements

1. **Proxy Health Check**
   - Add "Test Connection" button
   - Verify proxy is reachable before saving

2. **Proxy Speed Test**
   - Compare direct vs proxy download speeds
   - Recommend fastest option

3. **Multiple Proxy Regions**
   - Add region-specific proxy recommendations
   - Auto-select based on device location
