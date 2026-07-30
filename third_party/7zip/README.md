# 7-Zip 26.01 Android 构建

本目录不提交来源不明的预编译程序，也不以空文件冒充 `7zz`。构建流程从
7-Zip 官方 GitHub Release 获取固定版本源码，先校验发布资产 SHA-256，再使用
Android NDK r29 为三个 ABI 编译真实 Android ELF。

## 固定上游

- 项目：7-Zip，Copyright (C) 1999-2026 Igor Pavlov
- 版本/标签：`26.01`
- 发布资产：`7z2601-src.tar.xz`
- 官方地址：<https://github.com/ip7z/7zip/releases/download/26.01/7z2601-src.tar.xz>
- SHA-256：`b2389e0e930b2f9a348cf0fe7d9870a46482a8ec044ee0bdf42e2136db31c3d6`

固定值同时保存在 `SOURCE.lock`。下载脚本在解压前强制比对哈希，失配立即失败。

## 构建

主机需要 POSIX shell、GNU make、tar、curl/wget，以及已安装的 Android NDK r29：

```sh
export ANDROID_NDK_HOME=/absolute/path/to/android-ndk-r29
./third_party/7zip/build-android.sh
```

默认最低 API 为 26，与项目 Android 8.0 下限一致。可通过
`ANDROID_API_LEVEL` 提高，但不得低于 26。输出位于：

```text
third_party/7zip/out/
├── arm64-v8a/7zz
├── armeabi-v7a/7zz
├── x86_64/7zz
├── LICENSE-7ZIP.txt
└── SHA256SUMS
```

脚本使用 NDK 的目标包装 Clang、静态链接 C++ 运行库、禁用上游可选汇编路径，
并通过 Clang response file 避免 Windows 命令行长度上限，
同时按 Bionic 规则把 pthread 符号解析到 libc（不链接 Android 不提供的
`libpthread.so`），并为所有 `PT_LOAD` 加入 Android 15/16 所需的 16 KiB
最大/公共页对齐，
并用 `llvm-readelf` 验证 ELF 架构、Android linker 和
`libc++_shared.so` 依赖。Android 二进制不能在普通构建主机上直接运行，最终仍须
在相应 ABI 的 Android API 26+ 设备或模拟器执行压缩、解压、路径穿越与大文件测试。

可用环境变量：

- `SEVENZIP_CACHE_DIR`：下载和解压缓存目录。
- `SEVENZIP_SOURCE_DIR`：已校验源码目录。
- `SEVENZIP_BUILD_DIR`：中间产物目录。
- `SEVENZIP_OUT_DIR`：最终输出目录。
- `ANDROID_API_LEVEL`：目标 API，默认 `26`。
- `JOBS`：GNU make 并发数。
- `NDK_HOST_TAG`：需要手动选择 NDK host prebuilt 时使用。
- `SEVENZIP_ABIS`：空格分隔的重编 ABI；默认构建全部三种 ABI。

## 许可证

7-Zip 26.01 并非单一许可证：多数源码为 GNU LGPL v2.1 或更高版本；RAR
解码相关源码同时受 unRAR 限制；LZFSE 与 Zstandard 解码部分为 BSD
3-Clause；XXH64 为 BSD 2-Clause；另有明确标注为 public domain 的文件。

构建脚本从已校验的源码复制官方 `DOC/License.txt` 到输出目录。发布 `7zz`
二进制时必须把该文件一并放入最终交付物，不得删除版权、BSD 声明或 unRAR
限制说明。官方原文也保存在本目录的 `LICENSE-7ZIP.txt`。
