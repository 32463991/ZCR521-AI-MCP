# 安全模型

## 默认风险

本项目按需求默认启用匿名本机+局域网监听，并授予 MCP 工具完整 Root 能力。该选择的直接后果是：同一直接连接网段内，能访问 5322 端口的设备可以读写文件、执行 Root Shell、安装/卸载应用、改变系统设置甚至重启或关机。

网络限制用于缩小暴露面，不构成身份认证。不要把端口转发到公网，不要在访客 Wi‑Fi、酒店网络或不可信热点使用 LAN 模式。

## 权限边界

- supervisor：Root，只有进程管理、退避、日志和版本回滚职责。
- broker：Root，唯一执行特权操作的进程。
- frontend：优先 UID 2000、无 Linux capabilities；只负责 HTTP/MCP 解析和传输。
- broker socket：使用 `SO_PEERCRED` 核对 UID/GID，并把 peer PID 与 supervisor 写入的 frontend 子进程 PID 精确比对。

若 ROM 阻止降权，supervisor 会以 Root 重启 frontend；服务保持可用，但 `/status.securityDegraded=true`，状态页显示原因。

## 网络边界

- 回环始终允许。
- LAN 只允许接口路由表中“直接连接”的 on-link 地址。
- 非本机 Host、跨源 Origin、CORS 预检、过大请求和超并发请求被拒绝。
- 状态页没有外部脚本、字体、图片或统计请求，并设置 CSP/Frame/Referrer/Permissions 等安全头。

## 文件与命令

绝对路径能力是刻意设计，不是遗漏。调用方必须承担 Root 绝对路径修改风险。实现仍保证：

- 原子写配置和任务快照。
- 归档穿越检查。
- 下载/上传使用随机 ID、范围校验、SHA-256 与 TTL。
- 进程终止针对独立进程组。
- 卸载脚本在发信号前检查 PID 与 `/proc/<pid>/cmdline`。
- 不自动禁用 SELinux、不默认 remount 系统分区。

## 建议加固

当前 0.01 为满足锁定默认值，不启用 Token。生产环境至少采取一项：

1. 关闭 LAN，只使用 `adb forward`。
2. 在可信 VPN/私有 USB 网络中使用。
3. 用设备防火墙把 5322 限制到固定管理主机。
4. 后续启用认证配置后使用高熵 Token。

发现漏洞时不要附带真实设备隐私或可直接利用的公网地址。
