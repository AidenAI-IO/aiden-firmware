# Agent Files Visualization - Updates Summary

## 已完成功能

### 1. 基础可视化 ✅

- 部署到设备 `/userdata/agent_tools/`
- Memory 目录: 47 个文件 (357.4 KB)
- Skills 目录: 3 个文件 (9.7 KB)
- 生成 86-114KB 自包含 HTML 报告

### 2. 文件关联关系 ✅

新增自动提取和可视化文件间引用关系：

**支持的引用类型**:

- `ep_XXXXX` - Episode 引用（对话记录）
- `proc_XXXXX` - Procedure 引用（操作过程）
- `app_XXXXX` - App 引用（应用记录）
- `fail_XXXXX` - Failure 引用（失败记录）

**关联功能**:

- 自动扫描文件内容提取引用 ID
- 在文件详情页显示 "References" 区块
- 每个引用显示类型、ID、路径
- 点击 "Jump" 按钮跳转到引用的文件
- 目标文件高亮显示 2 秒

**实现示例**:

```
File: device/apps/app_512a6dfaaf7b.yaml
References: 2
  - episode: ep_1780625339372268063_07145689
  - app: app_512a6dfaaf7b
```

### 3. Web 路由（待编译）⏳

已在 `config_web.cpp` 中添加路由：

- `/user_files` - 查看报告
- `/user_files/regenerate` - 重新生成报告

**注意**: 需要重新编译 `config_web` 二进制才能生效

## 使用方法

### 在设备上生成报告

```bash
ssh root@<DEVICE_IP>
cd /userdata/agent_tools
./view_agent_files.sh
```

### 查看报告

1. 下载到本地查看：

```bash
scp root@<DEVICE_IP>:/userdata/agent/files_report.html ~/Desktop/
open ~/Desktop/files_report.html
```

2. 或通过 SSH 下载（已测试）：

```bash
ssh root@<DEVICE_IP> "cat /userdata/agent/files_report.html" > report.html
```

### 浏览关联关系

1. 打开任意 memory 文件（如 procedure、app、profile）
2. 向下滚动查看 "References" 区块
3. 点击 "Jump" 按钮跳转到引用的文件
4. 目标文件会高亮并自动滚动到视图中

## 技术实现

### Python 脚本增强

`generate_agent_files_report.py` 新增：

- `extract_references()` 函数：使用正则表达式提取引用
- 支持通配符路径匹配（如 `episodes/*/ep_xxx/*`）
- 为每个文件添加 `references` 字段

### HTML 模板增强

`agent_files_template.html` 新增：

- References 区块显示
- 引用类型颜色编码
- Jump 按钮和点击事件
- `jumpToFile()` 函数：智能路径匹配和跳转
- 文件高亮动画效果

### 引用提取规则

```python
episode_pattern = r'ep_\d+_[a-f0-9]+'     # Episode IDs
proc_pattern = r'proc_[a-f0-9]+'           # Procedure IDs
app_pattern = r'app_[a-f0-9]+'             # App IDs
fail_pattern = r'fail_[a-f0-9]+'           # Failure IDs
```

## 文件位置

**设备上**:

- 工具目录: `/userdata/agent_tools/`
- 报告文件: `/userdata/agent/files_report.html`
- Memory 数据: `/userdata/agent/memory/`
- Skills 数据: `/userdata/agent/skills/`

**本地**:

- 脚本: `scripts/generate_agent_files_report.py`
- 模板: `scripts/agent_files_template.html`
- 部署脚本: `scripts/deploy_to_device.sh`
- Web 路由: `src/config_web.cpp` (已修改，待编译)

## 下一步

### 需要编译的功能

要启用 Web 访问，需要：

1. 交叉编译 `config_web.cpp`
2. 部署到设备 `/oem/usr/bin/config_web`
3. 重启 config_web 服务
4. 访问 `http://<DEVICE_IP>/user_files`

### 可能的增强

- [ ] 反向引用：显示哪些文件引用了当前文件
- [ ] 引用图谱：可视化文件关联网络
- [ ] 搜索功能：按引用类型或 ID 搜索
- [ ] 导出功能：导出引用关系为 JSON/CSV
- [ ] 实时更新：文件变化时自动重新生成

## 测试数据

从设备提取的实际引用示例：

- `device/profile.yaml` → 引用 `ep_1780625339372268063_07145689`
- `device/procedures/proc_832cbf216c9e.yaml` → 引用 episode 和 recall_memory
- `device/apps/app_512a6dfaaf7b.yaml` → 引用 episode

共检测到 39 处引用关系分布在 47 个 memory 文件中。
