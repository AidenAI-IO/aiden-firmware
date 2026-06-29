#!/bin/bash
# 监控 android-tools-aiden 构建进度
LOG=/tmp/build3.log

echo "=== android-tools-aiden 构建监控 ==="
echo "日志文件: $LOG"
echo ""

while true; do
  clear
  echo "=== 构建监控（每 30 秒刷新）==="
  echo "时间: $(date '+%Y-%m-%d %H:%M:%S')"
  echo ""

  # 检查构建是否还在运行
  if ! pgrep -f "build_image.sh" > /dev/null; then
    echo "⚠️  构建进程已停止"
    exit_code=$(tail -1 /tmp/claude-1000/-home-cani1free-aiden-hardware-demo/*/tasks/b0jodyuzs.output 2>/dev/null | grep -oE "[0-9]+$")
    echo "退出码: ${exit_code:-unknown}"
    break
  fi

  # 日志大小
  echo "日志大小: $(ls -lh $LOG 2>/dev/null | awk '{print $5}')"

  # 关键依赖状态
  echo ""
  echo "关键依赖："
  for pkg in host-go host-protobuf host-boringssl; do
    if grep -q ">>> $pkg.*Installing" $LOG 2>/dev/null; then
      echo "  ✓ $pkg"
    elif grep -q ">>> $pkg" $LOG 2>/dev/null; then
      echo "  ⏳ $pkg (构建中)"
    else
      echo "  ⬜ $pkg (等待)"
    fi
  done

  # android-tools-aiden 状态
  echo ""
  if grep -q "Built target adb" $LOG 2>/dev/null; then
    echo "🎉 android-tools-aiden 构建成功！"
    grep "Built target adb" $LOG | tail -1
    break
  elif grep -q "Building CXX object vendor/CMakeFiles" $LOG 2>/dev/null; then
    echo "⚙️  android-tools-aiden 正在编译..."
    adb_progress=$(grep -oE "\[ *[0-9]+%\]" $LOG 2>/dev/null | tail -1)
    echo "   进度: ${adb_progress:-未知}"
  elif grep -q ">>> android-tools-aiden" $LOG 2>/dev/null; then
    echo "📦 android-tools-aiden 已开始（配置/提取阶段）"
  else
    echo "⏳ 等待 android-tools-aiden（编译依赖中）"
  fi

  # 错误检查
  errors=$(grep -iE "error:|Error [0-9]" $LOG 2>/dev/null | grep -iv "warning:" | tail -3)
  if [ -n "$errors" ]; then
    echo ""
    echo "⚠️  发现错误："
    echo "$errors" | sed 's/^/   /'
    break
  fi

  # 当前正在编译的包
  echo ""
  echo "当前活动："
  grep -E ">>> " $LOG 2>/dev/null | tail -1 | sed 's/^/  /'

  sleep 30
done

echo ""
echo "=== 最终状态 ==="
if grep -q "Built target adb" $LOG 2>/dev/null; then
  echo "✓ android-tools-aiden 构建成功"
  echo ""
  echo "完整的 android-tools-aiden 相关输出："
  grep -iE "android-tools-aiden|Built target adb|Installing.*adb" $LOG | tail -20
else
  echo "构建未成功完成"
  echo ""
  echo "最后的错误（如果有）："
  grep -iE "error:|Error [0-9]" $LOG 2>/dev/null | grep -iv "warning:" | tail -10
fi
