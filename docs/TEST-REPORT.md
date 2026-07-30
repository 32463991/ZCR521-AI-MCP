# 测试报告

版本：`0.01`
报告日期：2026-07-30
可复现时间戳：`SOURCE_DATE_EPOCH=1785340800`

## 结论

全部可在当前主机执行的单元、集成、竞态、静态、安全、定向 Fuzz、协议和构建闸门均通过。官方 MCP 合规套件中适用于本项目固定 48 工具契约的检查通过；要求 `greet`、`slow_compute`、`failing_job` 等套件专用夹具的检查明确记为“不适用”，没有向产品加入假工具来换取通过率。

当前环境没有连接 Android 设备或模拟器。安装、重启自启动、UID 2000 实际降权、Root 框架、SELinux Enforcing、4 KiB/16 KiB 设备运行、厂商 ROM 与真实 7z 执行均为“设备未测试”，不能由主机构建结果替代。

## 测试环境

| 项目 | 值 |
|---|---|
| 主机 | Windows NT 10.0.19045，amd64 |
| PowerShell | 5.1.19041.6456 |
| Go | 1.26.5 |
| 竞态测试 C 编译器 | MinGW-w64 GCC 16.1.0 |
| Android NDK | r29 / 29.0.14206865 |
| Android 链接基线 | API 26 |
| MCP Go SDK | v1.7.0-pre.3，vendored，提交 827f90b |
| MCP Conformance | `@modelcontextprotocol/conformance@0.2.0-alpha.10` |
| 7-Zip | 26.01，源码包 SHA-256 `b2389e0e930b2f9a348cf0fe7d9870a46482a8ec044ee0bdf42e2136db31c3d6` |

## 主机与安全测试

| 闸门 | 命令或证据 | 结果 |
|---|---|---|
| 依赖完整性 | `go mod verify` | 通过 |
| 全包单元/集成 | `go test ./...`，16 个项目包 | 通过 |
| 静态分析 | `go vet ./...` | 通过 |
| 数据竞态 | `go test -race ./...` | 通过 |
| 工具/动作完整性 | `TestAllPublicToolsAreDispatched`、`TestEverySchemaActionReachesAnOperation` | 48 个工具及全部公开 action 均有真实分派 |
| 端到端 MCP | `TestEndToEndMCPBrokerFrontend` | 新协议、文件读写、普通后台任务、Tasks 扩展、传输和安全拒绝通过 |
| Tasks | `internal/mcpapi` 测试 | `-32021`、`-32602`、路由头、移除方法、持久句柄、标准终态映射通过 |
| Supervisor | `internal/supervisor` 测试 | 1–60 秒策略默认值、五次熔断、停止原因、稳定标记、回滚路径边界和完整复制通过 |
| 配置恢复 | `internal/config` 测试 | 初始生成、损坏隔离恢复、新字段迁移、危险 CORS 拒绝通过 |
| 持久任务 | `internal/tasks` 测试 | 原子持久化、重启恢复、超时终态通过 |
| 传输 | `internal/frontend` 与 `internal/ops` 测试 | Content-Range、断点恢复、回滚、SHA-256、Range 下载、过期清理通过 |
| 归档 | ZIP、TAR、GZIP、XZ、tar.xz 测试 | 往返、`../`、绝对路径、TAR 越界链接和符号链接组件防护通过 |
| APKS/XAPK | 固定 `toc.pb` 与清单夹具 | ABI、密度、语言、SDK、OBB 包目录边界通过 |
| Schema | `internal/schema` 测试与 `schemas/tools.json` | 48 个唯一工具；JSON Schema 2020-12；`action` enum/`oneOf`；结果结构完整 |
| 状态页 | 单元测试 + 浏览器 QA | 1280×720、390×844 无横向溢出；安全警告可见；控制台 0 错误/警告 |
| 模块脚本 | Shell 语法与 `verify_module.py` | LF、入口、权限、三 ABI、安装选择和卸载边界通过 |

## 最终二进制运行时验收

本闸门直接启动编译后的 `zcr521d supervisor`，不使用进程内测试替代真实
frontend → Unix Socket → broker → operation 链路。

| 检查 | 结果 |
|---|---|
| `/health`、`/status` 与 MCP 连接 | 通过 |
| 当前协议协商 | `2026-07-28` |
| 工具发现 | 48/48 |
| 工具实际调用 | 48/48 均返回完整统一结果结构 |
| 主机可执行功能 | 17 项真实成功 |
| Android 专属功能 | 31 项正确返回结构化 `UNSUPPORTED`，没有冒充成功 |
| 并发 | 24 worker、512 次 MCP 状态调用全部成功 |
| 后台任务取消 | 长时间 Shell 任务进入 `cancelled` |
| frontend 异常退出 | supervisor 在退避后重建 broker/frontend，恢复 HTTP 200 |
| 恢复后进程数 | supervisor/broker/frontend 严格收敛为 1/1/1 |
| 10 秒空闲采样 | 总 RSS 37.85 MiB，进程数保持 3 |

该验收发现并修复了 Windows 主机上使用 signal 0 探测子进程时的误判：
旧逻辑会让 supervisor 在 frontend 异常退出后等待仍存活的 broker。修复后
Windows 使用进程退出码判断存活并可靠终止子进程；Android/Linux 继续使用
PID signal 0 与独立进程组。

Windows 当前令牌没有创建符号链接的权限，因此“已有符号链接父目录”的真实文件系统用例在本机跳过；同一拒绝逻辑的纯函数、TAR 越界链接、路径穿越和 Fuzz 均通过。该用例仍需在 Android/Linux 设备补测。

## 定向 Fuzz

以下 Fuzz 均以固定安全/有效种子启动，并限制单输入大小：

| 目标 | 时长 | 执行数 | 结果 |
|---|---:|---:|---|
| `FuzzSafeArchivePaths` | 3 秒 | 19,289 | 通过 |
| `FuzzParseBundletoolTOC` | 3 秒 | 8,265 | 通过 |
| `FuzzParseXAPKManifest` | 3 秒 | 9,747 | 通过 |

## MCP 官方合规套件

套件来源：[modelcontextprotocol/conformance](https://github.com/modelcontextprotocol/conformance)。

| 场景 | 结果 | 说明 |
|---|---|---|
| `server-stateless` / 2026-07-28 | 24 项通过；4 项不可测试 | 4 项仅因缺少套件诊断夹具 `test_missing_capability`、`test_streaming_elicitation`、`test_logging_tool` |
| `tools-list` | 2/2 通过 | 固定 48 工具可列举 |
| `dns-rebinding-protection` | 2/2 通过 | Host/DNS rebinding 防护 |
| `http-header-validation` | 13/13 通过 | 当前协议路由头 |
| `server-sse-multiple-streams` | 2/2 通过 | 当前与旧版分别通过 |
| `server-initialize` / 2025-11-25 | 2/2 通过 | 旧初始化/会话 |
| `ping` / 2025-11-25 | 1/1 通过 | 旧协议 ping |
| `server-sse-polling` / 2025-11-25 | 0 失败、2 个 SHOULD 警告 | 未发送 priming event 与 `retry:`；不影响 MUST 合规 |
| Tasks capability | 2 个通用项通过；2 项夹具不可用 | 扩展声明与未协商 `-32021` 通过；其余要求 `slow_compute` |
| Tasks dispatch/envelope | 3 个通用项通过；5 项夹具不可用 | `tasks/result/list` 移除及未知 ID `-32602` 通过；其余要求套件专用工具 |
| JSON Schema 2020-12 场景 | 不适用 | 场景硬编码要求 `json_schema_2020_12_tool`；本项目由提交的 48 工具 Schema 与内部测试验证 |

## Android ELF 与模块构建

`zcr521d` 和 `7zz` 均由 NDK r29 为下列 ABI 真实构建，不使用占位文件：

| ABI | ELF machine | PIE | PT_LOAD 最大页对齐 | API 基线 |
|---|---:|---|---:|---:|
| arm64-v8a | 183 | 通过 | 16,384 | 26 |
| armeabi-v7a | 40 | 通过 | 16,384 | 26 |
| x86_64 | 62 | 通过 | 16,384 | 26 |

模块 ZIP 的路径、LF、Unix mode、`META-INF` 安装入口、生命周期脚本、三 ABI 二进制和许可证由 `scripts/verify_module.py` 检查。构建完成后以相同 epoch 连续打包两次并比较 SHA-256；最终哈希写入 `dist/SHA256SUMS`。

## 浏览器 QA

- 桌面 1280×720：六张状态卡为三列；背景 `rgb(241,243,246)`；卡片为半透明白色、细边框和 `blur(16px)`。
- 窄屏 390×844：卡片改单列，文案换行，无横向溢出，Root LAN 警告位于首屏。
- 页面无紫色渐变，无持续动画；`prefers-reduced-motion` 禁用一次性入场动画。
- 状态页控制台 0 条 error/warn。
- 浏览器后端的多个测试标签始终报告 `document.hidden=false`，无法动态触发 visibility；隐藏时清定时器和中止请求由 `TestAssetsMeetMotionAndIdleRequirements` 静态/单元验证。

## 必须补充的设备证据

- API 26–36，4 KiB 与 16 KiB 页面设备启动。
- Magisk、KernelSU/Next、APatch ARM64 的安装、升级、Action 信息、卸载和开机启动。
- frontend 真实 UID 2000、broker UID 0、`SO_PEERCRED`、SELinux Enforcing。
- 截图/录屏、输入、应用安装/权限、网络、设置、属性、重启和 Systemless 修改。
- 三 ABI `7zz` 在 Android 上的创建、解压、校验、恶意归档和大文件测试。
- 厂商 ROM、多用户、存储未解锁、磁盘不足、联网/充电事件、空闲 CPU/RSS。
