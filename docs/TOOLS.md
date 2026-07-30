# 工具索引

完整机器可读定义以 [`schemas/tools.json`](../schemas/tools.json) 为准。

机器工具名固定使用小写 `zcr521_` 前缀和下划线，符合 MCP SDK 的工具名字符约束；客户端展示使用独立的简短中文 `title`，不再把机器标识当作显示名称。

| 分组 | 工具 |
|---|---|
| 服务 | `zcr521_status`、`zcr521_capabilities`、`zcr521_config` |
| 文件 | `zcr521_fs_info`、`zcr521_fs_read`、`zcr521_fs_write`、`zcr521_fs_manage`、`zcr521_fs_search`、`zcr521_fs_hash`、`zcr521_archive` |
| 传输 | `zcr521_download`、`zcr521_transfer_upload`、`zcr521_transfer_export` |
| Shell/任务 | `zcr521_shell`、`zcr521_script`、`zcr521_task` |
| 应用 | `zcr521_app_list`、`zcr521_app_info`、`zcr521_app_install`、`zcr521_app_manage`、`zcr521_app_permission`、`zcr521_app_export` |
| Root | `zcr521_root_info`、`zcr521_root_module`、`zcr521_systemless` |
| 进程/服务 | `zcr521_process`、`zcr521_service` |
| 系统设置 | `zcr521_property`、`zcr521_setting`、`zcr521_display`、`zcr521_audio`、`zcr521_connectivity`、`zcr521_locale_time`、`zcr521_input_method`、`zcr521_app_policy`、`zcr521_default_app`、`zcr521_notification`、`zcr521_accessibility`、`zcr521_developer` |
| 设备控制 | `zcr521_device_info`、`zcr521_power`、`zcr521_screen`、`zcr521_input` |
| 网络 | `zcr521_network` |
| 诊断 | `zcr521_log`、`zcr521_diagnostics` |
| 自动化 | `zcr521_schedule`、`zcr521_backup` |

## 执行规则

- 相对路径从 `/storage/emulated/0/zcr521AI` 解析；绝对路径保持绝对，不施加工作区沙箱。
- 安装时工作区根目录为空；各功能只在实际调用需要时创建对应子目录。
- 修改 Android 设置后，适配器会再次读取并核对；不一致返回失败。
- Android 命令按 API/帮助输出选择 `cmd`、`pm`、`am`、`settings`、`svc`、`dumpsys`、`input` 等策略。
- 缺少命令或 ROM 不支持时返回 `COMMAND_UNAVAILABLE` 或 `UNSUPPORTED`，其他工具继续可用。
- Shell 使用独立进程组；超时先 TERM，宽限后 KILL。输出写任务日志，响应只返回有界预览。
- 归档解压拒绝绝对路径、`..` 穿越和逃离目标目录的符号链接。
- Systemless 不执行 `setenforce 0`，不默认 remount 真实只读分区。
