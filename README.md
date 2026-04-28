# Aiden DEMO

## 相关硬件

[Luckfox Pico Zero](https://wiki.luckfox.com/Luckfox-Pico-Zero)

[TC358743XBG](https://toshiba.semicon-storage.com/eu/semiconductor/product/interface-bridge-ics-for-mobile-peripheral-devices/hdmir-interface-bridge-ics/detail.TC358743XBG.html)

[CH375B](https://easyelecmodule.com/ch375b-u-disk-read-write-module-development-guide/)

## Build

Standard out-of-source CMake build:

```bash
cmake -S . -B build
cmake --build build
```

For the Luckfox cross-compilation environment, use:

```bash
./build.sh
```

All build artifacts are placed in `build/`:
- `build/lib/` - Static libraries
- `build/bin/` - Executables
- `build/CMakeFiles/` - CMake metadata and intermediate files

## Scripts

- `start-dev.sh`: 启动 Docker 编译环境
- `build.sh`: 使用交叉编译工具链构建项目
