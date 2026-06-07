#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY="$SCRIPT_DIR/overlay"
PICO_SDK="$SCRIPT_DIR/pico-sdk"
DEST_OVERLAY="$PICO_SDK/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-buildroot-aiden"

require_rknnmrt_version() {
    local runtime="$1"
    local version major minor

    version="$(strings "$runtime" | sed -n 's/^librknnmrt version: \([0-9][0-9.]*\).*/\1/p' | head -n 1)"
    if [ -z "$version" ]; then
        echo "  ✗ Error: cannot read RKNN runtime version from $runtime" >&2
        exit 1
    fi
    major="${version%%.*}"
    minor="${version#*.}"
    minor="${minor%%.*}"
    if [ "$major" -lt 2 ] || { [ "$major" -eq 2 ] && [ "$minor" -lt 3 ]; }; then
        echo "  ✗ Error: RKNN runtime $version is too old for silero_vad_rv1106.rknn; use librknnmrt >= 2.3.x" >&2
        exit 1
    fi
}

echo "=== Aiden Hardware Demo - Image Builder ==="
echo ""

# Step 1: 编译应用程序. _build.sh requires a verified Go in PATH and disables
# automatic Go toolchain downloads.
echo "[1/6] Building applications..."
cd "$SCRIPT_DIR"
./_build.sh

# Step 2: 准备 overlay 目录
echo "[2/6] Preparing overlay directories..."
mkdir -p "$OVERLAY/oem/usr/bin" "$OVERLAY/oem/usr/lib" "$OVERLAY/oem/etc"
cp -a "$SCRIPT_DIR/build/bin"/. "$OVERLAY/oem/usr/bin/"
echo "  ✓ Binaries copied to overlay/oem/usr/bin"

BENCHMARK_SRC="$SCRIPT_DIR/benchmark"
BENCHMARK_DEST="$OVERLAY/userdata/agent/benchmark"
BENCHMARK_RSYNC_EXCLUDES=(--exclude '__pycache__/' --exclude '*.pyc' --exclude '.DS_Store' --exclude '._*')
if [ ! -d "$BENCHMARK_SRC/runner" ] || [ ! -d "$BENCHMARK_SRC/suites" ]; then
    echo "  ✗ Error: benchmark runner or suites missing under $BENCHMARK_SRC" >&2
    exit 1
fi
mkdir -p "$BENCHMARK_DEST/runner" "$BENCHMARK_DEST/suites"
rsync -a --delete "${BENCHMARK_RSYNC_EXCLUDES[@]}" "$BENCHMARK_SRC/runner/" "$BENCHMARK_DEST/runner/"
rsync -a --delete "${BENCHMARK_RSYNC_EXCLUDES[@]}" "$BENCHMARK_SRC/suites/" "$BENCHMARK_DEST/suites/"
if [ -f "$BENCHMARK_SRC/pyproject.toml" ]; then
    cp "$BENCHMARK_SRC/pyproject.toml" "$BENCHMARK_DEST/pyproject.toml"
else
    rm -f "$BENCHMARK_DEST/pyproject.toml"
fi
echo "  ✓ Benchmark runner and suites staged to overlay/userdata/agent/benchmark"

AGENT_TOOLS_DEST="$OVERLAY/userdata/agent_tools"
mkdir -p "$AGENT_TOOLS_DEST"
cp "$SCRIPT_DIR/scripts/generate_agent_files_report.py" "$AGENT_TOOLS_DEST/"
cp "$SCRIPT_DIR/scripts/agent_files_template.html" "$AGENT_TOOLS_DEST/"
cp "$SCRIPT_DIR/scripts/view_agent_files.sh" "$AGENT_TOOLS_DEST/"
chmod +x "$AGENT_TOOLS_DEST/generate_agent_files_report.py" "$AGENT_TOOLS_DEST/view_agent_files.sh"
echo "  ✓ Agent files report tools staged to overlay/userdata/agent_tools"

RKNNMRT_OVERLAY="$OVERLAY/oem/usr/lib/librknnmrt.so"
RKNNMRT_SOURCE="$PICO_SDK/media/iva/iva/librockiva/rockiva-rv1106-Linux/lib/librknnmrt.so"
if [ -f "$RKNNMRT_OVERLAY" ]; then
    echo "  ✓ RKNN runtime already present in overlay/oem/usr/lib"
else
    if [ ! -f "$RKNNMRT_SOURCE" ]; then
        echo "  ✗ Error: RKNN runtime not found: $RKNNMRT_SOURCE"
        exit 1
    fi
    cp "$RKNNMRT_SOURCE" "$RKNNMRT_OVERLAY"
    echo "  ✓ RKNN runtime copied to overlay/oem/usr/lib"
fi
require_rknnmrt_version "$RKNNMRT_OVERLAY"

KEY_SOURCE="${OTA_PUBLIC_KEY_PATH:-}"
if [ -n "$KEY_SOURCE" ]; then
    if [ ! -f "$KEY_SOURCE" ]; then
        echo "  ✗ Error: OTA_PUBLIC_KEY_PATH does not exist: $KEY_SOURCE"
        exit 1
    fi
elif [ -f "$SCRIPT_DIR/keys/ota_pubkey.pem" ]; then
    if grep -Eiq 'dev|test|placeholder' "$SCRIPT_DIR/keys/ota_pubkey.pem"; then
        echo "  ✗ Error: keys/ota_pubkey.pem is marked dev/test/placeholder; refusing production image"
        exit 1
    fi
    KEY_SOURCE="$SCRIPT_DIR/keys/ota_pubkey.pem"
else
    echo "  ✗ Error: set OTA_PUBLIC_KEY_PATH to a production Ed25519 public key or commit keys/ota_pubkey.pem"
    exit 1
fi

"$SCRIPT_DIR/scripts/validate_ota_pubkey.sh" "$KEY_SOURCE"
cp "$KEY_SOURCE" "$OVERLAY/oem/etc/ota_pubkey.pem"
echo "  ✓ OTA public key copied to overlay/oem/etc/ota_pubkey.pem"

# Step 3: 同步 etc 与内置 skills 到 buildroot overlay（rootfs）
echo "[3/6] Syncing overlay (etc + bundled skills) to buildroot overlay..."
if [ ! -d "$DEST_OVERLAY" ]; then
    echo "  ✗ Error: destination directory not found at $DEST_OVERLAY"
    exit 1
fi

# 只复制 etc 目录到 buildroot overlay
if [ -d "$OVERLAY/etc" ]; then
    mkdir -p "$DEST_OVERLAY/etc"
    rsync -a --delete "$OVERLAY/etc/" "$DEST_OVERLAY/etc/"
    echo "  ✓ etc directory synced"
fi

# Bundled agent skills: src/agent/config/skills/ 是单一源，固件里要落到
# /usr/share/aiden/skills/ 才能被 agent 的 resolveBundledSkillsDir() 找到。
SKILLS_SRC="$SCRIPT_DIR/src/agent/config/skills"
SKILLS_DEST="$DEST_OVERLAY/usr/share/aiden/skills"
if [ -d "$SKILLS_SRC" ]; then
    mkdir -p "$SKILLS_DEST"
    rsync -a --delete "$SKILLS_SRC/" "$SKILLS_DEST/"
    skill_count=$(find "$SKILLS_DEST" -mindepth 2 -maxdepth 2 -type f -name SKILL.md | wc -l | tr -d ' ')
    if [ "$skill_count" -lt 1 ]; then
        echo "  ✗ Error: no SKILL.md staged in $SKILLS_DEST" >&2
        exit 1
    fi
    echo "  ✓ bundled skills synced ($skill_count skill(s))"
else
    echo "  ⚠ Warning: $SKILLS_SRC not found; skipping bundled skills" >&2
fi

# Bundled phone-bridge app mapping: src/agent/internal/agent/app_mapping.json 是
# Go embed 的单一源，固件里同步落到 /usr/share/aiden/app_mapping.json，方便运维
# 不重新 build 二进制就能更新映射（agent 启动时优先读 configDir，再读这里，最后
# 回退到二进制内嵌副本）。
APP_MAPPING_SRC="$SCRIPT_DIR/src/agent/internal/agent/app_mapping.json"
APP_MAPPING_DEST="$DEST_OVERLAY/usr/share/aiden/app_mapping.json"
if [ -f "$APP_MAPPING_SRC" ]; then
    mkdir -p "$(dirname "$APP_MAPPING_DEST")"
    cp "$APP_MAPPING_SRC" "$APP_MAPPING_DEST"
    echo "  ✓ phone-bridge app mapping synced"
else
    echo "  ⚠ Warning: $APP_MAPPING_SRC not found; skipping app mapping" >&2
fi

# Step 4: 运行 pico-sdk 构建，overlay 注入后只打包一次 firmware，避免 A/B 大镜像重复生成。
echo "[4/6] Running pico-sdk build stages..."
cd "$PICO_SDK"
./build.sh sysdrv "$@"
./build.sh media "$@"
./build.sh app "$@"

# Step 5: 获取输出路径并复制 oem/userdata 内容
echo "[5/6] Injecting oem and userdata content..."

# Source board config to get paths
if [ -f "$PICO_SDK/.BoardConfig.mk" ]; then
    source "$PICO_SDK/.BoardConfig.mk"
fi

# 设置默认值
: ${RK_CHIP:=rv1106}
: ${RK_LIBC_TPYE:=glibc}
SDK_ROOT_DIR="$PICO_SDK"
RK_PROJECT_OUTPUT="${SDK_ROOT_DIR}/output/out"
RK_PROJECT_OUTPUT_IMAGE="${SDK_ROOT_DIR}/output/image"
RK_PROJECT_PACKAGE_OEM_DIR="${RK_PROJECT_OUTPUT}/oem"
RK_PROJECT_PACKAGE_USERDATA_DIR="${RK_PROJECT_OUTPUT}/userdata"

# 复制 oem 内容
if [ -d "$OVERLAY/oem" ]; then
    echo "  → Copying oem content..."
    mkdir -p "$RK_PROJECT_PACKAGE_OEM_DIR"
    rsync -a "$OVERLAY/oem/" "$RK_PROJECT_PACKAGE_OEM_DIR/"
    echo "  ✓ OEM content copied"
fi

# VAD models live in /oem/usr/model so OTA updates can replace them with the
# oem partition. Remove stale copies from reused userdata package directories.
rm -rf "$RK_PROJECT_PACKAGE_USERDATA_DIR/agent/model"

# 复制 userdata 内容
if [ -d "$OVERLAY/userdata" ] && [ "$(ls -A "$OVERLAY/userdata" 2>/dev/null)" ]; then
    echo "  → Copying userdata content..."
    mkdir -p "$RK_PROJECT_PACKAGE_USERDATA_DIR"
    rsync -a "$OVERLAY/userdata/" "$RK_PROJECT_PACKAGE_USERDATA_DIR/"
    echo "  ✓ USERDATA content copied"
fi

# Step 6: 重新打包 oem 和 userdata 镜像
echo "[6/6] Rebuilding oem.img and userdata.img..."
cd "$PICO_SDK/project"

# 重新打包 oem.img
if [ -d "$RK_PROJECT_PACKAGE_OEM_DIR" ] && [ "$(ls -A "$RK_PROJECT_PACKAGE_OEM_DIR")" ]; then
    echo "  → Rebuilding oem.img..."
    firmware_log="$(mktemp)"
    ./build.sh firmware "$@" > "$firmware_log" 2>&1
    grep -E "(oem|userdata|update)" "$firmware_log" || true
    rm -f "$firmware_log"
    echo "  ✓ Images rebuilt"
fi

echo ""
echo "=== Build Complete ==="
echo "Images location: $RK_PROJECT_OUTPUT_IMAGE"
echo ""
ls -lh "$RK_PROJECT_OUTPUT_IMAGE"/*.img 2>/dev/null | awk '{print "  " $9 " (" $5 ")"}'
echo ""

missing=0
for img in misc.img boot_a.img boot_b.img oem.img rootfs.img userdata.img update.img; do
    if [ ! -s "$RK_PROJECT_OUTPUT_IMAGE/$img" ]; then
        echo "  ✗ Missing expected image: $RK_PROJECT_OUTPUT_IMAGE/$img" >&2
        missing=1
    fi
done
if [ "$missing" -ne 0 ]; then
    exit 1
fi
echo "  ✓ Expected A/B images verified"

cd "$SCRIPT_DIR"
