// Package schema is the single source of truth for MCP tool names and JSON
// Schema 2020-12 input contracts.
package schema

import (
	"encoding/json"
	"fmt"
	"slices"
)

const Draft202012 = "https://json-schema.org/draft/2020-12/schema"

type Annotations struct {
	ReadOnly    bool `json:"readOnly"`
	Idempotent  bool `json:"idempotent"`
	Destructive bool `json:"destructive"`
	Heavy       bool `json:"heavy"`
}

type Tool struct {
	Name         string         `json:"name"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Annotations  Annotations    `json:"annotations"`
}

type Registry struct {
	SchemaVersion string `json:"$schema"`
	Protocol      string `json:"protocol"`
	Tools         []Tool `json:"tools"`
}

type spec struct {
	name        string
	description string
	actions     []string
	annotations Annotations
}

var specs = []spec{
	{"zcr521_status", "读取服务、Root、地址、任务与运行时间状态。", []string{"get"}, ro()},
	{"zcr521_capabilities", "读取或重新探测设备能力与降级原因。", []string{"get", "probe"}, ro()},
	{"zcr521_config", "读取、验证、原子更新、导出或恢复服务配置。", []string{"get", "validate", "update", "export", "reset"}, rw(false)},
	{"zcr521_fs_info", "列目录、读取元数据、磁盘与挂载信息。", []string{"stat", "list", "disk", "mounts"}, ro()},
	{"zcr521_fs_read", "流式读取文本、二进制、行、尾部或资源链接。", []string{"text", "binary", "lines", "tail", "resource"}, ro()},
	{"zcr521_fs_write", "创建、追加、截断、补丁写入或更新时间戳。", []string{"create", "append", "truncate", "patch", "touch"}, rw(true)},
	{"zcr521_fs_manage", "目录、复制、移动、删除、权限、所有者与链接管理。", []string{"mkdir", "copy", "move", "remove", "chmod", "chown", "symlink", "hardlink", "selinux"}, rw(true)},
	{"zcr521_fs_search", "按名称或内容搜索，查找大文件与重复文件。", []string{"name", "content", "large", "duplicates"}, roHeavy()},
	{"zcr521_fs_hash", "计算或校验文件哈希。", []string{"calculate", "verify"}, roHeavy()},
	{"zcr521_archive", "创建、解压、列出或校验 ZIP/TAR/GZIP/XZ/7z。", []string{"create", "extract", "list", "test"}, heavy(true)},
	{"zcr521_download", "可重试、续传、批量下载及状态/取消。", []string{"start", "batch", "status", "cancel"}, heavy(true)},
	{"zcr521_transfer_upload", "创建、查询、分块完成或取消上传。", []string{"create", "chunk", "status", "complete", "cancel"}, heavy(true)},
	{"zcr521_transfer_export", "将文件、目录或任务产物导出到临时下载地址。", []string{"file", "directory", "task"}, heavy(false)},
	{"zcr521_shell", "以 root、shell 或指定 UID 执行命令。", []string{"exec"}, rw(true)},
	{"zcr521_script", "验证或执行多行脚本。", []string{"validate", "run"}, rw(true)},
	{"zcr521_task", "读取、列出、更新、取消任务或读取日志。", []string{"get", "list", "update", "cancel", "logs"}, rw(false)},
	{"zcr521_app_list", "列出普通、系统或指定用户应用。", []string{"list"}, ro()},
	{"zcr521_app_info", "读取应用、组件与权限详情。", []string{"get", "components", "permissions"}, ro()},
	{"zcr521_app_install", "通过 Package Manager session 安装 APK/Split/APKS/XAPK。", []string{"apk", "split", "apks", "xapk", "session"}, heavy(true)},
	{"zcr521_app_manage", "启动、停止、清理、启停、卸载及多用户管理应用。", []string{"launch", "stop", "clear_cache", "clear_data", "enable", "disable", "uninstall", "user"}, rw(true)},
	{"zcr521_app_permission", "列出、授予、撤销权限或管理 AppOps。", []string{"list", "grant", "revoke", "appops"}, rw(true)},
	{"zcr521_app_export", "导出 APK、Split 或应用包。", []string{"apk", "splits", "bundle"}, heavy(false)},
	{"zcr521_root_info", "检测 Root 框架、能力与运行时自检。", []string{"detect", "capabilities", "self_test"}, ro()},
	{"zcr521_root_module", "管理 Magisk、KernelSU 或 APatch 模块生命周期。", []string{"list", "info", "install", "update", "remove", "enable", "disable", "action", "backup", "restore", "logs"}, heavy(true)},
	{"zcr521_systemless", "应用、移除、列出或校验 Systemless 覆盖。", []string{"apply", "remove", "list", "verify"}, heavy(true)},
	{"zcr521_process", "列出或检查进程，发送信号、调整优先级、查看 fd。", []string{"list", "info", "signal", "kill", "renice", "fds"}, rw(true)},
	{"zcr521_service", "分型管理 Binder、应用 Service 与 init 服务。", []string{"binder_list", "binder_call", "app_start", "app_stop", "init_status", "init_start", "init_stop", "init_restart"}, rw(true)},
	{"zcr521_property", "读取、列出、设置或恢复 Android 属性。", []string{"get", "list", "set", "reset"}, rw(true)},
	{"zcr521_setting", "读取、列出、写入或删除 Android Settings。", []string{"get", "list", "put", "delete"}, rw(true)},
	{"zcr521_display", "读取或调整亮度、尺寸、密度与旋转。", []string{"get", "brightness", "size", "density", "rotation"}, rw(true)},
	{"zcr521_audio", "读取或调整音量、静音与音频路由。", []string{"get", "volume", "mute", "route"}, rw(true)},
	{"zcr521_connectivity", "读取或调整 Wi-Fi、移动数据、飞行模式、蓝牙与 NFC。", []string{"get", "wifi", "mobile_data", "airplane_mode", "bluetooth", "nfc"}, rw(true)},
	{"zcr521_locale_time", "读取或调整语言区域、时区与时间格式。", []string{"get", "locale", "timezone", "time_format"}, rw(true)},
	{"zcr521_input_method", "列出、读取、选择、启用或禁用输入法。", []string{"list", "get", "set", "enable", "disable"}, rw(true)},
	{"zcr521_app_policy", "管理待机桶、后台与电池优化策略。", []string{"get", "standby_bucket", "background", "battery_optimization"}, rw(true)},
	{"zcr521_default_app", "读取、设置或清除默认应用。", []string{"get", "set", "clear"}, rw(true)},
	{"zcr521_notification", "读取或调整通知与监听器权限。", []string{"get", "allow", "deny", "listener"}, rw(true)},
	{"zcr521_accessibility", "列出、读取、启用或禁用无障碍服务。", []string{"list", "get", "enable", "disable"}, rw(true)},
	{"zcr521_developer", "读取或调整 ADB、常亮、动画及模拟位置设置。", []string{"get", "adb", "stay_awake", "animation", "mock_location"}, rw(true)},
	{"zcr521_device_info", "读取设备、电池、存储与传感器信息。", []string{"get", "battery", "storage", "sensors"}, ro()},
	{"zcr521_power", "重启到系统/Recovery/Bootloader/Fastbootd 或关机。", []string{"reboot", "recovery", "bootloader", "fastbootd", "shutdown", "soft_reboot"}, rw(true)},
	{"zcr521_screen", "截图、录屏、前台 Activity、唤醒或休眠。", []string{"screenshot", "record", "foreground", "wake", "sleep"}, rw(true)},
	{"zcr521_input", "点击、滑动、输入文本、按键与状态栏操作。", []string{"tap", "swipe", "text", "keyevent", "statusbar"}, rw(true)},
	{"zcr521_network", "接口、路由、DNS、Ping、HTTP、端口、连接、Wi-Fi 与代理。", []string{"interfaces", "routes", "dns", "ping", "resolve", "http", "ports", "connections", "wifi", "proxy", "connectivity"}, rw(false)},
	{"zcr521_log", "读取 Logcat、内核、dmesg、模块/MCP 日志或实时流。", []string{"logcat", "kernel", "dmesg", "module", "mcp", "stream", "clear"}, rw(false)},
	{"zcr521_diagnostics", "执行自检或生成完整诊断报告。", []string{"self_test", "collect"}, heavy(false)},
	{"zcr521_schedule", "管理一次、周期、Cron 及事件触发任务。", []string{"list", "create", "update", "remove", "enable", "disable", "run"}, rw(true)},
	{"zcr521_backup", "创建、列出、验证、恢复或删除备份。", []string{"create", "list", "verify", "restore", "remove"}, heavy(true)},
}

var toolTitles = map[string]string{
	"zcr521_status":          "服务状态",
	"zcr521_capabilities":    "设备能力",
	"zcr521_config":          "服务配置",
	"zcr521_fs_info":         "文件信息",
	"zcr521_fs_read":         "读取文件",
	"zcr521_fs_write":        "写入文件",
	"zcr521_fs_manage":       "管理文件",
	"zcr521_fs_search":       "搜索文件",
	"zcr521_fs_hash":         "文件哈希",
	"zcr521_archive":         "压缩归档",
	"zcr521_download":        "文件下载",
	"zcr521_transfer_upload": "分块上传",
	"zcr521_transfer_export": "导出文件",
	"zcr521_shell":           "执行命令",
	"zcr521_script":          "执行脚本",
	"zcr521_task":            "任务管理",
	"zcr521_app_list":        "应用列表",
	"zcr521_app_info":        "应用信息",
	"zcr521_app_install":     "安装应用",
	"zcr521_app_manage":      "管理应用",
	"zcr521_app_permission":  "应用权限",
	"zcr521_app_export":      "导出应用",
	"zcr521_root_info":       "Root 信息",
	"zcr521_root_module":     "Root 模块",
	"zcr521_systemless":      "Systemless 覆盖",
	"zcr521_process":         "进程管理",
	"zcr521_service":         "系统服务",
	"zcr521_property":        "系统属性",
	"zcr521_setting":         "系统设置",
	"zcr521_display":         "显示设置",
	"zcr521_audio":           "音频设置",
	"zcr521_connectivity":    "连接设置",
	"zcr521_locale_time":     "语言与时间",
	"zcr521_input_method":    "输入法管理",
	"zcr521_app_policy":      "应用策略",
	"zcr521_default_app":     "默认应用",
	"zcr521_notification":    "通知管理",
	"zcr521_accessibility":   "无障碍服务",
	"zcr521_developer":       "开发者设置",
	"zcr521_device_info":     "设备信息",
	"zcr521_power":           "电源控制",
	"zcr521_screen":          "屏幕工具",
	"zcr521_input":           "输入控制",
	"zcr521_network":         "网络工具",
	"zcr521_log":             "日志读取",
	"zcr521_diagnostics":     "系统诊断",
	"zcr521_schedule":        "定时任务",
	"zcr521_backup":          "备份恢复",
}

// toolParameters is the public wire contract. It deliberately lists every
// accepted canonical parameter and compatibility alias so tools.json can be
// consumed by strict JSON Schema clients without falling back to an
// undocumented free-form object.
var toolParameters = map[string][]string{
	"zcr521_status":          {},
	"zcr521_capabilities":    {},
	"zcr521_config":          {"config", "destination"},
	"zcr521_fs_info":         {"path"},
	"zcr521_fs_read":         {"path", "encoding", "offset", "length", "limit", "startLine", "line", "lineCount", "count", "bytes"},
	"zcr521_fs_write":        {"path", "content", "data", "encoding", "mode", "parents", "size", "offset", "createParents"},
	"zcr521_fs_manage":       {"path", "source", "src", "destination", "dest", "target", "paths", "parents", "mode", "overwrite", "recursive", "confirmDangerous", "uid", "gid", "context"},
	"zcr521_fs_search":       {"path", "root", "content", "query", "text", "limit", "maxResults", "name", "pattern", "extension", "ext", "minSize", "maxSize", "maxFiles", "modifiedAfter", "modifiedBefore"},
	"zcr521_fs_hash":         {"path", "algorithm", "expected", "digest", "hash"},
	"zcr521_archive":         {"format", "type", "destination", "output", "path", "sources", "source", "inputs", "archive", "overwrite", "background"},
	"zcr521_download":        {"url", "path", "destination", "output", "overwrite", "resume", "retries", "sha256", "maxBytes", "headers", "timeout", "timeoutMs", "items", "taskId", "background"},
	"zcr521_transfer_upload": {"path", "destination", "overwrite", "content", "data", "offset", "size", "expectedSize", "sha256"},
	"zcr521_transfer_export": {"taskId", "artifactIndex", "index", "path", "destination", "output", "background"},
	"zcr521_shell":           {"command", "cmd", "cwd", "workDir", "env", "stdin", "timeout", "timeoutMs", "identity", "user", "uid", "gid", "background"},
	"zcr521_script":          {"script", "content", "path", "cwd", "workDir", "env", "stdin", "timeout", "timeoutMs", "identity", "user", "uid", "gid", "background"},
	"zcr521_task":            {"taskId", "id", "progress", "message", "limit"},
	"zcr521_app_list":        {"user", "query", "system"},
	"zcr521_app_info":        {"package", "packageName", "component", "user"},
	"zcr521_app_install":     {"path", "apk", "paths", "apks", "archive", "package", "packageName", "replace", "downgrade", "grantRuntimePermissions", "user", "operation", "sessionId", "name", "splitName", "background"},
	"zcr521_app_manage":      {"package", "packageName", "component", "user", "keepData", "enabled", "operation", "mode"},
	"zcr521_app_permission":  {"package", "packageName", "permission", "mode", "user"},
	"zcr521_app_export":      {"package", "packageName", "destination", "output", "user", "background"},
	"zcr521_root_info":       {},
	"zcr521_root_module":     {"id", "moduleId", "path", "zip", "destination", "output", "source", "archive", "format", "overwrite", "background"},
	"zcr521_systemless":      {"source", "target", "path", "lower", "upper", "work", "lazy", "background"},
	"zcr521_process":         {"query", "name", "package", "pid", "signal", "priority"},
	"zcr521_service":         {"kind", "type", "name", "service", "code", "transaction", "arguments", "args", "package", "packageName", "component", "user"},
	"zcr521_property":        {"key", "name", "value"},
	"zcr521_setting":         {"namespace", "table", "key", "value", "user"},
	"zcr521_display":         {"value", "milliseconds", "auto", "mode", "scale", "reset"},
	"zcr521_audio":           {"stream", "level", "value", "muted", "mode"},
	"zcr521_connectivity":    {"enabled", "host", "hostname", "port", "mode"},
	"zcr521_locale_time":     {"timezone", "locale", "value", "time"},
	"zcr521_input_method":    {"id", "component", "user"},
	"zcr521_app_policy":      {"package", "packageName", "user", "allowed", "mode", "value"},
	"zcr521_default_app":     {"package", "packageName", "component", "role", "user"},
	"zcr521_notification":    {"package", "packageName", "component", "allowed", "user"},
	"zcr521_accessibility":   {"id", "component", "user"},
	"zcr521_developer":       {"name", "key", "value", "enabled", "scale"},
	"zcr521_device_info":     {"path"},
	"zcr521_power":           {"delayMs", "confirmDangerous"},
	"zcr521_screen":          {"path", "output", "duration", "seconds", "bitRate", "background"},
	"zcr521_input":           {"x", "y", "x1", "y1", "x2", "y2", "durationMs", "text", "value", "key", "code"},
	"zcr521_network":         {"host", "name", "port", "count", "timeout", "timeoutMs", "url", "method", "body", "headers", "maxResponseBytes", "enabled", "mode"},
	"zcr521_log":             {"follow", "lines", "timeout", "timeoutMs", "path", "target", "background"},
	"zcr521_diagnostics":     {"path", "output", "background"},
	"zcr521_schedule":        {"id", "scheduleId", "name", "type", "tool", "targetTool", "targetArgs", "command", "enabled", "retryCount", "retries", "retryDelaySeconds", "at", "everySeconds", "intervalSeconds", "time", "weekday", "cron", "expression"},
	"zcr521_backup":          {"type", "sources", "paths", "path", "destination", "output", "sha256", "backup", "confirmDangerous", "overwrite", "background"},
}

func All(protocol string) Registry {
	tools := make([]Tool, 0, len(specs))
	for _, item := range specs {
		tools = append(tools, Tool{
			Name:         item.name,
			Title:        toolTitles[item.name],
			Description:  item.description,
			InputSchema:  actionSchema(item.name, item.actions),
			OutputSchema: resultSchema(),
			Annotations:  item.annotations,
		})
	}
	slices.SortFunc(tools, func(a, b Tool) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return Registry{SchemaVersion: Draft202012, Protocol: protocol, Tools: tools}
}

func Find(protocol, name string) (Tool, bool) {
	for _, tool := range All(protocol).Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func Marshal(protocol string) ([]byte, error) {
	return json.MarshalIndent(All(protocol), "", "  ")
}

func ValidateInvocation(name string, args map[string]any) error {
	var found *spec
	for i := range specs {
		if specs[i].name == name {
			found = &specs[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("unknown tool %q", name)
	}
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return fmt.Errorf("tool %s requires string action", name)
	}
	if !slices.Contains(found.actions, action) {
		return fmt.Errorf("tool %s does not support action %q", name, action)
	}
	return nil
}

func actionSchema(name string, actions []string) map[string]any {
	oneOf := make([]any, 0, len(actions))
	enums := make([]any, 0, len(actions))
	for _, action := range actions {
		enums = append(enums, action)
		oneOf = append(oneOf, map[string]any{
			"title": action,
			"properties": map[string]any{
				"action": map[string]any{"const": action},
			},
			"required": []string{"action"},
		})
	}
	catalog := parameterCatalog()
	properties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        enums,
			"description": "要执行的明确操作。",
		},
	}
	names, ok := toolParameters[name]
	if !ok {
		panic("missing parameter contract for " + name)
	}
	for _, parameter := range names {
		definition, exists := catalog[parameter]
		if !exists {
			panic("missing JSON Schema definition for parameter " + parameter)
		}
		properties[parameter] = definition
	}
	return map[string]any{
		"$schema":              Draft202012,
		"$id":                  "https://zcr521.local/schema/tools/" + name + ".json",
		"type":                 "object",
		"properties":           properties,
		"required":             []string{"action"},
		"oneOf":                oneOf,
		"additionalProperties": false,
	}
}

func parameterCatalog() map[string]any {
	stringValue := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integerValue := func(description string, minimum int64) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "description": description}
	}
	booleanValue := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	stringArray := func(description string) map[string]any {
		return map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": description,
		}
	}
	stringMap := func(description string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          description,
		}
	}
	pathValue := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	stringOrArray := func(description string) map[string]any {
		return map[string]any{
			"oneOf": []any{
				map[string]any{"type": "string", "minLength": 1},
				map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "minLength": 1}},
			},
			"description": description,
		}
	}
	return map[string]any{
		"algorithm":               map[string]any{"type": "string", "enum": []string{"md5", "sha1", "sha256"}, "description": "哈希算法。"},
		"allowed":                 booleanValue("是否允许该策略或权限。"),
		"apk":                     pathValue("APK 文件路径兼容别名。"),
		"apks":                    stringArray("Split APK 路径数组兼容别名。"),
		"archive":                 pathValue("归档文件路径兼容别名。"),
		"artifactIndex":           integerValue("任务产物的零基索引。", 0),
		"args":                    stringArray("命令、Binder 或服务参数数组。"),
		"arguments":               stringArray("Binder transaction 参数数组。"),
		"at":                      map[string]any{"type": "string", "format": "date-time", "description": "一次性任务的未来 RFC3339 时间。"},
		"auto":                    booleanValue("是否恢复系统自动模式。"),
		"background":              booleanValue("是否转为持久后台任务执行。"),
		"backup":                  pathValue("备份归档路径兼容别名。"),
		"bitRate":                 integerValue("录屏码率，单位 bit/s。", 1),
		"body":                    stringValue("HTTP 请求体。"),
		"bytes":                   integerValue("尾部读取的最大字节数。", 0),
		"cmd":                     stringValue("Shell 命令兼容别名。"),
		"code":                    map[string]any{"oneOf": []any{map[string]any{"type": "integer", "minimum": 0}, map[string]any{"type": "string", "minLength": 1}}, "description": "Binder transaction 或按键代码。"},
		"command":                 stringValue("要执行的 Shell 命令。"),
		"component":               stringValue("Android 组件名。"),
		"config":                  configInputSchema(),
		"confirmDangerous":        booleanValue("确认高风险破坏性操作的显式开关。"),
		"content":                 stringValue("文本或 Base64 内容。"),
		"context":                 stringValue("SELinux context。"),
		"count":                   integerValue("数量、行数或 Ping 次数。", 0),
		"createParents":           booleanValue("写入前是否创建父目录。"),
		"cron":                    stringValue("五字段 Cron 表达式。"),
		"cwd":                     pathValue("子进程工作目录；相对路径基于默认工作目录。"),
		"data":                    stringValue("content 的兼容别名。"),
		"delayMs":                 integerValue("执行电源操作前的延迟毫秒数。", 0),
		"dest":                    pathValue("destination 的兼容别名。"),
		"destination":             pathValue("目标路径；相对路径基于默认工作目录。"),
		"digest":                  stringValue("expected 的兼容摘要字段。"),
		"downgrade":               booleanValue("是否允许版本降级安装。"),
		"duration":                integerValue("持续时间秒数。", 0),
		"durationMs":              integerValue("持续时间毫秒数。", 0),
		"enabled":                 booleanValue("是否启用目标功能。"),
		"encoding":                map[string]any{"type": "string", "enum": []string{"utf-8", "text", "base64"}, "description": "内容编码。"},
		"env":                     stringMap("附加环境变量。"),
		"everySeconds":            integerValue("间隔任务周期秒数。", 1),
		"expected":                stringValue("期望的哈希摘要。"),
		"expectedSize":            map[string]any{"type": "integer", "minimum": -1, "description": "期望文件字节数；-1 表示不校验。"},
		"expression":              stringValue("cron 的兼容别名。"),
		"ext":                     stringValue("extension 的兼容别名。"),
		"extension":               stringValue("文件扩展名过滤器。"),
		"follow":                  booleanValue("是否持续流式读取日志。"),
		"format":                  map[string]any{"type": "string", "enum": []string{"zip", "tar", "tar.gz", "tgz", "gzip", "gz", "xz", "tar.xz", "txz", "7z"}, "description": "归档格式。"},
		"gid":                     map[string]any{"type": "integer", "minimum": -1, "description": "目标 GID；-1 表示保持不变。"},
		"grantRuntimePermissions": booleanValue("安装后是否授予清单运行时权限。"),
		"hash":                    stringValue("expected 的兼容摘要字段。"),
		"headers":                 stringMap("HTTP 请求头。"),
		"host":                    stringValue("主机名或 IP 地址。"),
		"hostname":                stringValue("主机名。"),
		"id":                      stringValue("任务、模块、计划或组件 ID。"),
		"identity":                map[string]any{"type": "string", "enum": []string{"root", "shell", "current", "uid"}, "description": "Shell 执行身份。"},
		"index":                   integerValue("零基索引。", 0),
		"inputs":                  stringArray("sources 的兼容别名。"),
		"intervalSeconds":         integerValue("everySeconds 的兼容别名。", 1),
		"items":                   map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": map[string]any{"type": "object", "additionalProperties": true}, "description": "批量下载参数对象。"},
		"key":                     stringValue("属性、设置或按键名称。"),
		"keepData":                booleanValue("卸载时是否保留应用数据。"),
		"kind":                    stringValue("服务或操作类别。"),
		"lazy":                    booleanValue("卸载 mount 时是否使用 lazy 模式。"),
		"length":                  integerValue("最大读取字节数。", 0),
		"level":                   integerValue("音量级别。", 0),
		"limit":                   integerValue("返回或读取上限。", 0),
		"line":                    integerValue("startLine 的兼容别名。", 1),
		"lineCount":               integerValue("读取行数。", 0),
		"lines":                   integerValue("日志行数。", 0),
		"locale":                  stringValue("BCP-47 语言区域标识。"),
		"lower":                   pathValue("OverlayFS lowerdir。"),
		"maxBytes":                integerValue("最大传输字节数；0 表示仅受磁盘和配置限制。", 0),
		"maxFiles":                integerValue("扫描文件数上限。", 1),
		"maxResponseBytes":        map[string]any{"type": "integer", "minimum": 1, "maximum": 67108864, "description": "HTTP 响应预览上限。"},
		"maxResults":              integerValue("最大返回结果数。", 1),
		"maxSize":                 map[string]any{"type": "integer", "minimum": -1, "description": "最大文件字节数；-1 表示不限制。"},
		"message":                 stringValue("任务进度消息。"),
		"method":                  stringValue("HTTP 方法。"),
		"milliseconds":            integerValue("毫秒值。", 0),
		"minSize":                 integerValue("最小文件字节数。", 0),
		"mode":                    stringValue("文件模式或 Android 子操作模式。"),
		"modifiedAfter":           map[string]any{"type": "string", "format": "date-time", "description": "修改时间下界。"},
		"modifiedBefore":          map[string]any{"type": "string", "format": "date-time", "description": "修改时间上界。"},
		"moduleId":                stringValue("Root 模块 ID。"),
		"muted":                   booleanValue("是否静音。"),
		"name":                    stringValue("名称、服务名或查询字段。"),
		"namespace":               map[string]any{"type": "string", "enum": []string{"system", "secure", "global"}, "description": "Android Settings 命名空间。"},
		"offset":                  integerValue("字节偏移。", 0),
		"operation":               stringValue("Package Manager session 子操作。"),
		"output":                  pathValue("destination/path 的兼容别名。"),
		"overwrite":               booleanValue("是否覆盖已存在目标。"),
		"package":                 stringValue("Android 包名。"),
		"packageName":             stringValue("package 的兼容别名。"),
		"parents":                 booleanValue("是否递归创建父目录。"),
		"path":                    pathValue("文件或目录路径；相对路径基于默认工作目录。"),
		"paths":                   stringArray("文件或 APK 路径数组。"),
		"pattern":                 stringValue("名称匹配模式。"),
		"permission":              stringValue("Android 权限名。"),
		"pid":                     integerValue("进程 ID。", 1),
		"port":                    map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "TCP 端口。"},
		"priority":                map[string]any{"type": "integer", "minimum": -20, "maximum": 19, "description": "进程 nice 优先级。"},
		"progress":                map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "任务进度 0..1。"},
		"query":                   stringValue("搜索查询。"),
		"recursive":               booleanValue("是否递归处理目录。"),
		"replace":                 booleanValue("是否替换已安装应用。"),
		"reset":                   booleanValue("是否恢复系统默认值。"),
		"resume":                  booleanValue("是否断点续传。"),
		"retries":                 map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "description": "重试次数。"},
		"retryCount":              map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "description": "计划任务重试次数。"},
		"retryDelaySeconds":       map[string]any{"type": "integer", "minimum": 1, "maximum": 86400, "description": "计划任务重试间隔秒数。"},
		"role":                    stringValue("Android Role 名称。"),
		"root":                    pathValue("搜索根目录。"),
		"scale":                   stringValue("动画缩放比例。"),
		"scheduleId":              stringValue("定时任务 ID。"),
		"script":                  stringValue("Shell 脚本文本。"),
		"seconds":                 integerValue("秒数。", 0),
		"service":                 stringValue("Android Binder 或 init 服务名。"),
		"sessionId":               integerValue("Package Manager session ID。", 0),
		"sha256":                  map[string]any{"type": "string", "pattern": "^[A-Fa-f0-9]{64}$", "description": "期望或实际 SHA-256。"},
		"signal":                  map[string]any{"oneOf": []any{map[string]any{"type": "integer", "minimum": 1, "maximum": 64}, map[string]any{"type": "string", "minLength": 1}}, "description": "POSIX 信号编号或名称。"},
		"size":                    map[string]any{"type": "integer", "minimum": -1, "description": "文件字节数。"},
		"source":                  stringOrArray("源路径或源路径数组。"),
		"sources":                 stringArray("源路径数组。"),
		"splitName":               stringValue("Package Manager split 名。"),
		"src":                     pathValue("source 的兼容别名。"),
		"startLine":               integerValue("首行，1 基。", 1),
		"stdin":                   stringValue("发送给子进程标准输入的内容。"),
		"stream":                  integerValue("Android 音频流编号。", 0),
		"system":                  booleanValue("是否仅包含系统应用。"),
		"table":                   map[string]any{"type": "string", "enum": []string{"system", "secure", "global"}, "description": "namespace 的兼容别名。"},
		"target":                  pathValue("链接、挂载或 Systemless 目标路径。"),
		"targetArgs":              map[string]any{"type": "object", "additionalProperties": true, "description": "定时任务目标工具参数。"},
		"targetTool":              stringValue("定时任务目标工具名。"),
		"taskId":                  map[string]any{"type": "string", "pattern": "^(?:[A-Fa-f0-9]{24}|[A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-4[A-Fa-f0-9]{3}-[89ABab][A-Fa-f0-9]{3}-[A-Fa-f0-9]{12})$", "description": "持久任务 ID。"},
		"text":                    stringValue("文本、搜索内容或输入内容。"),
		"time":                    stringValue("HH:MM 时间或系统时间值。"),
		"timeout":                 map[string]any{"oneOf": []any{map[string]any{"type": "integer", "minimum": 1}, map[string]any{"type": "string", "pattern": "^[0-9]+(?:ms|s|m|h)$"}}, "description": "超时秒数或 Go 风格时长。"},
		"timeoutMs":               integerValue("超时毫秒数。", 1),
		"timezone":                stringValue("IANA 时区。"),
		"tool":                    stringValue("定时任务目标工具名。"),
		"transaction":             integerValue("Binder transaction code。", 0),
		"type":                    stringValue("归档、服务、备份或定时任务类型。"),
		"uid":                     map[string]any{"type": "integer", "minimum": -1, "description": "目标 UID；-1 表示保持不变。"},
		"upper":                   pathValue("OverlayFS upperdir。"),
		"url":                     map[string]any{"type": "string", "format": "uri", "pattern": "^https?://", "description": "HTTP 或 HTTPS URL。"},
		"user":                    map[string]any{"oneOf": []any{map[string]any{"type": "integer", "minimum": 0}, map[string]any{"type": "string", "minLength": 1}}, "description": "Android 用户 ID 或 Shell 身份兼容字段。"},
		"value":                   map[string]any{"type": []string{"string", "integer", "boolean"}, "description": "设置值；具体类型由 action 决定。"},
		"weekday":                 map[string]any{"type": "integer", "minimum": 0, "maximum": 6, "description": "星期，0 表示周日。"},
		"work":                    pathValue("OverlayFS workdir。"),
		"workDir":                 pathValue("cwd 的兼容别名。"),
		"x":                       integerValue("屏幕 X 坐标。", 0),
		"x1":                      integerValue("滑动起点 X 坐标。", 0),
		"x2":                      integerValue("滑动终点 X 坐标。", 0),
		"y":                       integerValue("屏幕 Y 坐标。", 0),
		"y1":                      integerValue("滑动起点 Y 坐标。", 0),
		"y2":                      integerValue("滑动终点 Y 坐标。", 0),
		"zip":                     pathValue("Root 模块 ZIP 路径。"),
	}
}

func configInputSchema() map[string]any {
	boolean := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	integer := func(minimum, maximum int64, description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum, "description": description}
	}
	path := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	return map[string]any{
		"type":        "object",
		"description": "完整配置对象；validate/update 必须提交当前 schemaVersion 的整份配置。",
		"properties": map[string]any{
			"schemaVersion": map[string]any{"const": 1},
			"network": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"port":           integer(1, 65535, "监听端口。"),
					"listenLoopback": boolean("监听回环地址。"),
					"listenLan":      boolean("监听直接连接的局域网地址。"),
					"legacySse":      boolean("启用 2024-11-05 SSE 兼容端点。"),
					"allowedOrigins": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required":             []string{"port", "listenLoopback", "listenLan", "legacySse", "allowedOrigins"},
				"additionalProperties": false,
			},
			"paths": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"stateDir":     path("内部状态目录。"),
					"workDir":      path("共享用户工作目录。"),
					"downloadsDir": path("下载目录。"),
					"uploadsDir":   path("上传目录。"),
					"artifactsDir": path("产物目录。"),
					"tempDir":      path("临时目录。"),
				},
				"required":             []string{"stateDir", "workDir", "downloadsDir", "uploadsDir", "artifactsDir", "tempDir"},
				"additionalProperties": false,
			},
			"limits": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"maxConnections":        integer(1, 4096, "最大连接数。"),
					"maxRequestBytes":       integer(65536, 1<<34, "最大请求体字节数。"),
					"totalTasks":            integer(1, 128, "总任务并发。"),
					"heavyTasks":            integer(1, 128, "重任务并发。"),
					"shellTimeoutSeconds":   integer(1, 86400, "Shell 默认超时秒数。"),
					"transferChunkBytes":    integer(65536, 67108864, "MCP 传输块字节数。"),
					"transferMaxBytes":      integer(0, 1<<60, "传输总大小上限；0 表示仅受磁盘限制。"),
					"resultPreviewBytes":    integer(1024, 67108864, "结果预览字节数。"),
					"artifactTtlSeconds":    integer(1, 604800, "临时产物有效秒数。"),
					"shutdownGraceSeconds":  integer(1, 300, "优雅退出秒数。"),
					"uploadIdleTtlSeconds":  integer(60, 2592000, "上传会话空闲有效秒数。"),
					"downloadRetryAttempts": integer(0, 10, "下载重试次数。"),
				},
				"required": []string{
					"maxConnections", "maxRequestBytes", "totalTasks", "heavyTasks",
					"shellTimeoutSeconds", "transferChunkBytes", "transferMaxBytes",
					"resultPreviewBytes", "artifactTtlSeconds", "shutdownGraceSeconds",
					"uploadIdleTtlSeconds", "downloadRetryAttempts",
				},
				"additionalProperties": false,
			},
			"security": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"anonymous":       map[string]any{"const": true, "description": "本版本固定匿名连接。"},
					"onLinkOnly":      map[string]any{"const": true, "description": "仅允许回环和直接连接的局域网来源。"},
					"validateHost":    map[string]any{"const": true},
					"validateOrigin":  map[string]any{"const": true},
					"allowCors":       map[string]any{"const": false},
					"dropFrontendUid": map[string]any{"const": 2000},
				},
				"required":             []string{"anonymous", "onLinkOnly", "validateHost", "validateOrigin", "allowCors", "dropFrontendUid"},
				"additionalProperties": false,
			},
			"capabilities": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "boolean"},
			},
		},
		"required":             []string{"schemaVersion", "network", "paths", "limits", "security", "capabilities"},
		"additionalProperties": false,
	}
}

func resultSchema() map[string]any {
	properties := map[string]any{
		"success":        map[string]any{"type": "boolean"},
		"code":           map[string]any{"type": "string"},
		"message":        map[string]any{"type": "string"},
		"data":           map[string]any{},
		"error":          map[string]any{"type": []string{"object", "null"}},
		"stdout":         map[string]any{"type": "string"},
		"stderr":         map[string]any{"type": "string"},
		"exitCode":       map[string]any{"type": "integer"},
		"durationMs":     map[string]any{"type": "integer", "minimum": 0},
		"taskId":         map[string]any{"type": "string"},
		"rebootRequired": map[string]any{"type": "boolean"},
		"artifacts":      map[string]any{"type": "array"},
		"strategy":       map[string]any{"type": "string"},
	}
	required := []string{
		"success", "code", "message", "data", "error", "stdout", "stderr",
		"exitCode", "durationMs", "taskId", "rebootRequired", "artifacts", "strategy",
	}
	return map[string]any{
		"$schema":              Draft202012,
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func ro() Annotations {
	return Annotations{ReadOnly: true, Idempotent: true}
}

func roHeavy() Annotations {
	return Annotations{ReadOnly: true, Idempotent: true, Heavy: true}
}

func rw(destructive bool) Annotations {
	return Annotations{Destructive: destructive}
}

func heavy(destructive bool) Annotations {
	return Annotations{Destructive: destructive, Heavy: true}
}
