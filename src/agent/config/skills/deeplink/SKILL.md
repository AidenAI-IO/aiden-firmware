---
name: deeplink
description: Deeplink reference for phone automation. Use open_app with ios_urls/android_packages to trigger in-app actions beyond simple app launch.
metadata:
  preferred_model: primary
  allowed_tools: [open_app]
---

Use the `open_app` tool with explicit `ios_urls` or `android_packages` to trigger specific in-app actions.
Only use these when a plain URL cannot achieve the goal. For opening websites, pass the URL directly as the app name.

On Android, `android_packages` supports both package names (just opens the app) and deeplink URLs containing `://` (opens a specific screen). When a deeplink URL works on both platforms, pass the same URL in both `ios_urls` and `android_packages`.

## WeChat / 微信

| Action | iOS | Android |
|--------|-----|---------|
| Scan QR | `weixin://scanqrcode` | `com.tencent.mm` (then HID to navigate) |
| My QR code | `weixin://dl/qrcode` | - |
| Moments | `weixin://dl/moments` | - |
| Mini Programs | `weixin://dl/miniprogram` | - |
| Favorites | `weixin://dl/favorites` | - |
| Sticker shop | `weixin://dl/stickers` | - |
| Settings | `weixin://dl/settings` | - |
| Chat (known user) | `weixin://dl/chat?{username}` | - |

## Alipay / 支付宝

| Action | iOS | Android (intent) |
|--------|-----|---------|
| Scan QR | `alipay://platformapi/startapp?appId=10000007` | same |
| Payment code (被扫) | `alipay://platformapi/startapp?appId=20000056` | same |
| Transfer | `alipay://platformapi/startapp?appId=20000116` | same |
| Ant Forest | `alipay://platformapi/startapp?appId=60000002` | same |
| Bus code | `alipay://platformapi/startapp?appId=200011235` | same |
| Health code | `alipay://platformapi/startapp?appId=68687141` | same |
| Bill / 账单 | `alipay://platformapi/startapp?appId=20000003` | same |
| Contacts | `alipay://platformapi/startapp?appId=20000014` | same |
| Red packet | `alipay://platformapi/startapp?appId=88886666` | same |

## Taobao / 淘宝

| Action | iOS | Android |
|--------|-----|---------|
| Scan | `taobao://go/scan` | `com.taobao.taobao` |
| Cart | `taobao://go/cart` | same |
| Orders | `taobao://go/myorder` | same |
| Search | `taobao://go/search?q={keyword}` | same |
| Item detail | `taobao://go/detail?id={itemId}` | same |

## Douyin / 抖音

| Action | iOS | Android |
|--------|-----|---------|
| Scan | `snssdk1128://scan` | `com.ss.android.ugc.aweme` |
| Search | `snssdk1128://search?keyword={q}` | same |
| Record video | `snssdk1128://record` | same |
| Profile | `snssdk1128://user/profile/{uid}` | same |
| Live | `snssdk1128://live` | same |

## Meituan / 美团

| Action | iOS | Android |
|--------|-----|---------|
| Scan | `imeituan://www.meituan.com/scan` | `com.sankuai.meituan` |
| Orders | `imeituan://www.meituan.com/order/list` | same |
| Food delivery | `imeituan://www.meituan.com/food` | same |

## Didi / 滴滴

| Action | iOS | Android |
|--------|-----|---------|
| Call taxi | `diditaxi://passenger/call` | `com.sdu.didi.psnger` |

## Xiaohongshu / 小红书

| Action | iOS | Android |
|--------|-----|---------|
| Search | `xhsdiscover://search?keyword={q}` | `com.xingin.xhs` |
| Post detail | `xhsdiscover://item/{noteId}` | same |

## Bilibili / 哔哩哔哩

| Action | iOS | Android |
|--------|-----|---------|
| Search | `bilibili://search?keyword={q}` | `tv.danmaku.bili` |
| Video | `bilibili://video/{bvid}` | same |
| Live room | `bilibili://live/{roomId}` | same |

## JD / 京东

| Action | iOS | Android |
|--------|-----|---------|
| Scan | `openapp.jdmobile://virtual?params={"category":"scan"}` | `com.jingdong.app.mall` |
| Cart | `openapp.jdmobile://virtual?params={"category":"cart"}` | same |
| Orders | `openapp.jdmobile://virtual?params={"category":"orderList"}` | same |
| Search | `openapp.jdmobile://virtual?params={"category":"search","keyword":"{q}"}` | same |

## Eleme / 饿了么

| Action | iOS | Android |
|--------|-----|---------|
| Orders | `eleme://orderlist` | `me.ele` |
| Search | `eleme://search?keyword={q}` | same |

## Maps / 地图导航

| Action | iOS | Android |
|--------|-----|---------|
| Apple Maps directions | `maps://?saddr={origin}&daddr={destination}&dirflg=d` | - |
| Apple Maps search | `maps://?q={query}` | - |
| Google Maps directions | `comgooglemaps://?saddr={origin}&daddr={destination}&directionsmode=driving` | `google.navigation:q={lat},{lng}&mode=d` |
| Google Maps search | `comgooglemaps://?q={query}` | `geo:0,0?q={query}` |
| Amap / 高德 directions | `iosamap://path?dlat={lat}&dlon={lng}&dname={name}&dev=0&t=0` | `amapuri://route/plan?dlat={lat}&dlon={lng}&dname={name}&dev=0&t=0` |
| Amap search | `iosamap://poi?keyword={q}` | `amapuri://poi?keyword={q}` |
| Baidu Maps directions | `baidumap://map/direction?destination={name}&coord_type=gcj02&mode=driving` | `bdapp://map/direction?destination={name}&coord_type=gcj02&mode=driving` |
| Baidu Maps search | `baidumap://map/place/search?query={q}` | `bdapp://map/place/search?query={q}` |

Direction mode flags: Apple Maps `dirflg`: d=driving, w=walking, r=transit. Google: driving/walking/bicycling/transit. Amap `t`: 0=driving, 1=bus, 2=walking. Baidu `mode`: driving/walking/transit/riding.

## Phone / SMS / FaceTime

| Action | iOS | Android |
|--------|-----|---------|
| Call | `tel://{number}` | `tel:{number}` |
| Call with prompt | `telprompt://{number}` | - |
| SMS | `sms://{number}` | `sms:{number}` |
| SMS with body | `sms://{number}&body={text}` | `sms:{number}?body={text}` |
| FaceTime video | `facetime://{contact}` | - |
| FaceTime audio | `facetime-audio://{contact}` | - |

## iOS Settings (prefs:root=)

| Screen | URL |
|--------|-----|
| Wi-Fi | `App-prefs://WIFI` |
| Bluetooth | `App-prefs://Bluetooth` |
| Cellular | `App-prefs://MOBILE_DATA_SETTINGS_ID` |
| Hotspot | `App-prefs://INTERNET_TETHERING` |
| Notifications | `App-prefs://NOTIFICATIONS_ID` |
| Battery | `App-prefs://BATTERY_USAGE` |
| Display | `App-prefs://DISPLAY` |
| Sounds | `App-prefs://Sounds` |
| Privacy | `App-prefs://Privacy` |
| Location | `App-prefs://Privacy&path=LOCATION` |
| Camera permission | `App-prefs://Privacy&path=CAMERA` |
| Microphone permission | `App-prefs://Privacy&path=MICROPHONE` |
| General | `App-prefs://General` |
| Storage | `App-prefs://General&path=STORAGE_MGMT` |
| Software Update | `App-prefs://General&path=SOFTWARE_UPDATE_LINK` |
| Keyboard | `App-prefs://General&path=Keyboard` |
| VPN | `App-prefs://General&path=VPN` |
| Date & Time | `App-prefs://General&path=DATE_AND_TIME` |
| Accessibility | `App-prefs://ACCESSIBILITY` |
| Do Not Disturb | `App-prefs://DO_NOT_DISTURB` |
| Screen Time | `App-prefs://SCREEN_TIME` |
| Siri | `App-prefs://SIRI` |
| Face ID | `App-prefs://PASSCODE` |

## Social / Messaging

| Action | iOS | Android |
|--------|-----|---------|
| Telegram chat | `tg://resolve?domain={username}` | same |
| Telegram send | `tg://resolve?domain={username}&text={msg}` | same |
| WhatsApp chat | `whatsapp://send?phone={number}&text={msg}` | same |
| Instagram profile | `instagram://user?username={name}` | same |
| Twitter/X profile | `twitter://user?screen_name={name}` | same |
| Twitter/X search | `twitter://search?query={q}` | same |

## Notes

- Deeplinks may break across app versions. If a deeplink fails, fall back to HID (open app then navigate visually).
- iOS `App-prefs://` works on iOS 15+. Older versions use `prefs:root=`.
- Android packages can also be launched with extras via `am start` shell command if phone bridge supports it.
- For WeChat, most `weixin://dl/` paths only work if the user is already logged in.
- Alipay `appId` values are stable across versions.
