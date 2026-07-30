# 安装、升级与卸载

## 前置条件

- Android 8.0 / API 26 或更高。
- 已安装 Magisk、KernelSU/KernelSU Next，或 ARM64 设备上的 APatch。
- 安装包必须是正式版本中唯一的资源 `ZCR521-Android-AI-MCP-v0.01-universal.zip`。

模块不依赖 Zygisk。`post-fs-data.sh` 只做常量时间的目录准备，不启动服务；非阻塞启动位于 `service.sh`。

## 框架差异

| 框架 | 支持 ABI | 说明 |
|---|---|---|
| Magisk | arm64-v8a、armeabi-v7a、x86_64 | 标准模块安装与生命周期脚本 |
| KernelSU / Next | arm64-v8a、armeabi-v7a、x86_64 | 标准模块；新增 Systemless 路径依赖已有 metamodule |
| APatch | arm64-v8a | 官方仅支持 ARM64；使用 APM 布局 |

安装器读取 Android ABI，选择 `bin/<ABI>/zcr521d` 和 `7zz`，复制为 `bin/zcr521d`、`bin/7zz`，再删除未选 ABI。未知 ABI 会立即终止安装，不会留下伪成功模块。

## 安装

1. 在 Root 框架管理器中选择 ZIP。
2. 检查安装日志中的 ABI、框架和二进制检查结果。
3. 重启 Android。
4. 浏览 `http://127.0.0.1:5322/health`；返回 `ok: true` 才表示 frontend 可用。
5. 查看 `/status`，确认 `root`、`securityDegraded`、框架和能力探测结果。

默认用户目录：

```text
/storage/emulated/0/zcr521AI/
```

安装只创建这个空的工作区根目录，不预创建任何子目录。下载、上传、截图、备份等目录仅在用户调用对应 MCP 操作时按需创建。

内部状态位于 `/data/adb/zcr521-mcp`，普通应用不可读。

## USB 安全连接

Windows：

```powershell
.\scripts\usb\zcr521-usb-windows.ps1
```

Linux：

```sh
./scripts/usb/zcr521-usb-linux.sh
```

macOS：

```sh
./scripts/usb/zcr521-usb-macos.sh
```

脚本验证 `adb`、设备状态与端口转发，然后把桌面客户端指向 `http://127.0.0.1:5322/mcp`。若客户端拒绝明文 LAN MCP，应始终使用该方式。

## Action 信息

模块管理器的 Action 按钮只显示：

```text
作者:小骨@Xiaogu_zcr521
当前版本: 0.01
```

MCP 地址保存在 `/data/adb/zcr521-mcp/MCP地址.txt`。安装完成时会尝试打开官方 Telegram `@Xiaogu_zcr521`，跳转结果不影响安装。

## 升级与回滚

升级安装保留内部状态与用户目录。supervisor 把新版本视为候选版本：

- 连续稳定运行 5 分钟后标记为可用版本。
- 异常采用 1、2、4、8、16、32、60 秒上限的退避。
- 10 分钟内连续失败 5 次停止重启；存在升级前二进制时执行回滚并记录原因。

可通过 `/status`、模块日志和 `/data/adb/zcr521-mcp/versions/state.json` 查看结果。

## 卸载

卸载脚本读取 PID 文件后还会验证 `/proc/<pid>/cmdline` 指向本模块的 `zcr521d`，验证通过才发送 TERM/KILL。它删除：

- `/data/adb/zcr521-mcp`
- 模块自己的临时 mount 和生成覆盖

它不会删除 `/storage/emulated/0/zcr521AI`。若用户需要彻底清除，请在确认备份后自行删除该目录。
