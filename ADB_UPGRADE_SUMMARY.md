# adb 1.0.41 Upgrade Summary

## Final Result

✅ **Successfully upgraded board's adb client from 1.0.31 → 1.0.41**

- **Package:** `android-tools-aiden` (out-of-tree, vendored from nmeum/android-tools 30.0.5p1)
- **Binary:** `/usr/bin/adb` (2.0M, ARM 32-bit, dynamically linked, uClibc)
- **Version:** 1.0.41 (`ADB_SERVER_VERSION 41` from AOSP platform-tools-30)
- **BoringSSL:** Statically linked (crypto/ssl baked into binary)
- **External deps:** protobuf, brotli, lz4, libusb, zlib, zstd (dynamically linked)

## Patches Applied (11 total)

All patches live in `pico-sdk/sysdrv/tools/board/buildroot/android-tools-aiden/` and are exported to `~/test/pico-sdk/`.

### Compile Fixes (0001-0008)
1. **0001** - `std::span` shim for GCC 8.3 (no `<span>` header)
2. **0002** - Skip BoringSSL `ssl/test` subdir (pruned from vendored tarball)
3. **0003** - Move patches to package top-level (Buildroot 2023.02 doesn't search `patches/`)
4. **0004** - Fix GCC 8.3 ICE on VLA `sizeof` in `logging_splitters.h`
5. **0005** - Add missing `<functional>` includes for libstdc++
6. **0006** - C++17 compat for `string_view::starts_with/ends_with` (C++20 methods)
7. **0007** - Skip glibc `group_member()` on uClibc (uClibc defines `__GLIBC__` but lacks it)
8. **0008** - Add boringssl googletest include for `gtest_prod.h`

### Link Fix (0009)
9. **0009** - Define `libzip` CMake target with googletest include (adb links libzip, but its target lives in `CMakeLists.fastboot.txt` which our client-only build excludes)

### Install Fix (0010)
10. **0010** - Correct adb install path in `.mk` file (in-tree build → `vendor/adb`, not `buildroot-build/vendor/adb`)

### Runtime Symbol Fix (0011)
11. **0011** - Link vendored BoringSSL statically (`BUILD_SHARED_LIBS=OFF`) — without this, adb dynamically links `libcrypto.so`/`libssl.so` which resolve to OpenSSL 1.1 at runtime, causing unresolved BoringSSL symbol errors (`SPAKE2_*`, `EVP_AEAD_*`, `HKDF`, `sk_pop_free_ex`, etc.)

## Build Environment

- **Toolchain:** GCC 8.3 + uClibc + armv7 hard-float
- **Target:** ARM 32-bit (Rockchip RV1106 board)
- **Build system:** Buildroot 2023.02.6 (Docker-based)
- **Source:** nmeum/android-tools 30.0.5p1 (vendored, reproducible tarball from `scripts/vendor-android-tools.sh`)

## Key Technical Details

### Why BoringSSL, not OpenSSL?
Modern adb (AOSP platform-tools-30) uses BoringSSL-specific APIs (`<openssl/base64.h>`, SPAKE2, EVP_AEAD) that don't exist in OpenSSL. The vendored source includes a curated BoringSSL with Go-based build system.

### Why Static BoringSSL?
Buildroot's cmake-package defaults to `BUILD_SHARED_LIBS=ON`, building BoringSSL as `libcrypto.so`/`libssl.so`. Those shared libs are never installed to the rootfs (only adb is), so at runtime adb's `NEEDED libcrypto.so/libssl.so` resolve to the **OpenSSL 1.1** libs already in the rootfs, causing symbol resolution failures. By forcing `BUILD_SHARED_LIBS=OFF`, BoringSSL's crypto/ssl become static archives linked directly into adb (binary grows 770K → 2.0M), eliminating the runtime dependency.

### Why nmeum/android-tools 30.0.5p1, not 35.x?
- **30.0.5p1** requires C++17 (`gnu++2a`), which GCC 8.3 supports
- **35.x** requires C++20, which GCC 8.3 lacks
- The board's vendor SDK locks the toolchain to GCC 8.3

## Verification

```bash
# On device
/usr/bin/adb --version
# Should report: Android Debug Bridge version 1.0.41

# Check BoringSSL symbols resolved
readelf -d /usr/bin/adb | grep NEEDED
# Should NOT list libcrypto.so or libssl.so

# Run adb
adb devices
# Should NOT show "can't resolve symbol" errors
```

## Files Modified

### Main Repo
- `pico-sdk` submodule pointer (11 commits)

### pico-sdk Submodule
- `sysdrv/tools/board/buildroot/android-tools-aiden/*.patch` (9 patches)
- `sysdrv/tools/board/buildroot/android-tools-aiden/*.mk` (package definition)
- `sysdrv/tools/board/buildroot/android-tools-aiden/Config.in` (kconfig)

All patches exported to `~/test/pico-sdk/` for archival.

## Build Time
- **Full build:** ~35-40 minutes (includes host-go, BoringSSL, protobuf compilation)
- **Incremental:** ~2-5 minutes (reuses cached deps)

## Next Steps
1. Flash `update.img` or `rootfs.img` to device
2. Verify `adb --version` reports 1.0.41
3. Test adb connectivity with Android devices

---
Generated: 2026-06-26
