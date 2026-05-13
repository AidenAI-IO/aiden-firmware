#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY="$SCRIPT_DIR/overlay"
PICO_SDK="$SCRIPT_DIR/pico-sdk"
DEST_OVERLAY="$PICO_SDK/project/cfg/BoardConfig_IPC/overlay/overlay-luckfox-buildroot-aiden"

echo "=== Aiden Hardware Demo - Image Builder ==="
echo ""

# Step 1: 编译应用程序
echo "[1/6] Building applications..."
cd "$SCRIPT_DIR"
./_build.sh

# Step 2: 准备 overlay 目录
echo "[2/6] Preparing overlay directories..."
mkdir -p "$OVERLAY/oem/usr/bin"
cp -a "$SCRIPT_DIR/build/bin"/. "$OVERLAY/oem/usr/bin/"
echo "  ✓ Binaries copied to overlay/oem/usr/bin"

# Step 3: 同步 etc 等目录到 buildroot overlay（排除 oem 和 userdata）
echo "[3/6] Syncing overlay (etc) to buildroot overlay..."
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

# Step 4: 运行完整的 pico-sdk 构建
echo "[4/6] Running pico-sdk build all..."
cd "$PICO_SDK"
./build.sh all "$@"

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

# 复制 userdata 内容
if [ -d "$OVERLAY/userdata" ] && [ "$(ls -A $OVERLAY/userdata 2>/dev/null)" ]; then
    echo "  → Copying userdata content..."
    mkdir -p "$RK_PROJECT_PACKAGE_USERDATA_DIR"
    rsync -a "$OVERLAY/userdata/" "$RK_PROJECT_PACKAGE_USERDATA_DIR/"
    echo "  ✓ USERDATA content copied"
fi

# Step 6: 重新打包 oem 和 userdata 镜像
echo "[6/6] Rebuilding oem.img and userdata.img..."
cd "$PICO_SDK/project"

# 重新打包 oem.img
if [ -d "$RK_PROJECT_PACKAGE_OEM_DIR" ] && [ "$(ls -A $RK_PROJECT_PACKAGE_OEM_DIR)" ]; then
    echo "  → Rebuilding oem.img..."
    ./build.sh firmware 2>&1 | grep -E "(oem|userdata|update)" || true
    echo "  ✓ Images rebuilt"
fi

echo ""
echo "=== Build Complete ==="
echo "Images location: $RK_PROJECT_OUTPUT_IMAGE"
echo ""
ls -lh "$RK_PROJECT_OUTPUT_IMAGE"/*.img 2>/dev/null | awk '{print "  " $9 " (" $5 ")"}'
echo ""

cd "$SCRIPT_DIR"
