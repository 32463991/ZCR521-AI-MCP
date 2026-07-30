package frontend

import (
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OfficialMCPOptions controls both official Streamable HTTP handlers.
type OfficialMCPOptions struct {
	MaxRequestBodyBytes int64
	SessionTimeout      time.Duration
	Compatibility       http.Handler
}

// NewOfficialMCPHandlers constructs every standard MCP transport from the
// pinned official Go SDK. Two Streamable handlers are necessary: protocol
// 2026-07-28 is sessionless, while older protocol versions retain the
// initialize/session lifecycle.
func NewOfficialMCPHandlers(
	getServer func(*http.Request) *mcp.Server,
	options OfficialMCPOptions,
) MCPHandlers {
	if options.SessionTimeout <= 0 {
		options.SessionTimeout = 30 * time.Minute
	}
	legacy := mcp.NewSSEHandler(getServer, &mcp.SSEOptions{})
	return MCPHandlers{
		SDKStreamable: mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
			MaxRequestBodyBytes: options.MaxRequestBodyBytes,
			SessionTimeout:      options.SessionTimeout,
		}),
		SDKCurrent: mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
			Stateless:                    true,
			MaxRequestBodyBytes:          options.MaxRequestBodyBytes,
			PropagateRequestCancellation: true,
		}),
		Compatibility:  options.Compatibility,
		LegacySSE:      legacy,
		LegacyMessages: legacy,
	}
}
