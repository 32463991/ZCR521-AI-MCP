package buildinfo

const (
	Name               = "ZCR521 AI MCP"
	ModuleID           = "zcr521.android.mcp"
	DefaultVersion     = "0.01"
	ProtocolCurrent    = "2026-07-28"
	ProtocolPrevious   = "2025-11-25"
	ProtocolLegacySSE  = "2024-11-05"
	DefaultPort        = 5322
	DefaultStateDir    = "/data/adb/zcr521-mcp"
	DefaultWorkDir     = "/storage/emulated/0/zcr521AI"
	DefaultModuleDir   = "/data/adb/modules/zcr521.android.mcp"
	DefaultSocketName  = "broker.sock"
	DefaultStableAfter = 300
)

var (
	Version          = DefaultVersion
	Commit           = "unknown"
	BuildTime        = "unknown"
	ModulePropSHA256 = ""
)
