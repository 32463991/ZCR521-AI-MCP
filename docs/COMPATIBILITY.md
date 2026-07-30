# 兼容性矩阵

本表严格区分“构建/主机测试”与“设备实测”。当前构建环境没有连接 Android 设备或模拟器，因此不能把安装、开机、SELinux、厂商 ROM 或 Root 框架行为标为已实测。

## Android API

| API | Android | 当前证据 | 结论 |
|---:|---:|---|---|
| 26 | 8.0 | NDK API 26 链接基线、解析器夹具 | 理论兼容，设备未测试 |
| 27 | 8.1 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 28 | 9 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 29 | 10 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 30 | 11 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 31 | 12 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 32 | 12L | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 33 | 13 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 34 | 14 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 35 | 15 | 命令适配分支/夹具 | 理论兼容，设备未测试 |
| 36 | 16 | 命令适配分支/夹具 | 理论兼容，设备未测试 |

## ABI 与页面

| ABI | 框架用途 | 构建证据 | 设备证据 |
|---|---|---|---|
| arm64-v8a | Magisk、KernelSU、APatch | NDK r29、PIE、16 KiB LOAD 闸门 | 未测试 |
| armeabi-v7a | Magisk、KernelSU | NDK r29、PIE、16 KiB LOAD 闸门 | 未测试 |
| x86_64 | Magisk、KernelSU、模拟器 | NDK r29、PIE、16 KiB LOAD 闸门 | 未测试 |

同一个 16 KiB 对齐 ELF 可在 4 KiB 内核上运行；最终仍需分别在 4 KiB/16 KiB 真机或模拟器验证启动与 7z 操作。

## Root 框架

| 框架 | 状态 |
|---|---|
| Magisk | 模块布局与脚本静态模拟通过；真机未测试 |
| KernelSU / Next | 模块布局与脚本静态模拟通过；真机未测试 |
| APatch ARM64 | APM 布局静态模拟通过；真机未测试 |
| APatch 其他 ABI | 暂不支持，符合 APatch 官方 ARM64 限制 |

## Systemless

- Magisk：生成覆盖模块并可即时 bind mount；理论兼容未测试。
- APatch：OverlayFS 模块布局；理论兼容未测试。
- KernelSU：存在 metamodule 时生成覆盖；没有 metamodule 时，只对已存在目标提供可重放 bind mount，新增目录返回 `UNSUPPORTED`。
- 厂商 ROM、init 服务名、命令参数和 SELinux 域均以启动探测为准；单项失败不关闭基础 MCP、Root Shell 和文件能力。
