// Package frontend exposes the network-facing HTTP surface of ZCR521.
//
// The package deliberately depends on interfaces instead of the privileged
// implementation. This keeps parsing and network policy in the unprivileged
// frontend process while the broker remains the only component that performs
// device operations.
package frontend

import (
	"context"
	"net/http"
	"time"
)

// Result is the JSON-compatible result returned by the privileged broker.
// It mirrors the stable public result envelope without importing a concrete
// broker implementation.
type Result struct {
	Success        bool   `json:"success"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	Data           any    `json:"data"`
	Error          any    `json:"error"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	ExitCode       int    `json:"exitCode"`
	DurationMS     int64  `json:"durationMs"`
	TaskID         string `json:"taskId"`
	RebootRequired bool   `json:"rebootRequired"`
	Artifacts      []any  `json:"artifacts"`
	Strategy       string `json:"strategy"`
}

// BrokerClient is the complete privileged boundary used by the frontend.
// Implementations must return the operation's real result; the frontend never
// turns a transport success into an operation success.
type BrokerClient interface {
	Call(ctx context.Context, tool string, args map[string]any) (Result, error)
	Status(ctx context.Context) (map[string]any, error)
}

// MCPHandlers are injected by the daemon composition root.
//
// SDKStreamable and SDKCurrent are both built with the official
// modelcontextprotocol/go-sdk. SDKStreamable retains sessions for older
// initialize-based clients; SDKCurrent is stateless as required by 2026-07-28.
// Compatibility is optional and is used only for tasks/* methods that the
// daemon explicitly elects to extend. LegacySSE and LegacyMessages implement
// the 2024-11-05 transport.
type MCPHandlers struct {
	SDKStreamable  http.Handler
	SDKCurrent     http.Handler
	Compatibility  http.Handler
	LegacySSE      http.Handler
	LegacyMessages http.Handler
}

// Options configures the public HTTP surface.
type Options struct {
	Broker           BrokerClient
	MCP              MCPHandlers
	Version          string
	ProtocolCurrent  string
	ProtocolPrevious string
	ProtocolLegacy   string
	ListenAddr       string
	Port             int
	WorkDir          string
	TransferDir      string
	EnableLegacySSE  bool
	AllowedOrigins   []string

	MaxRequestBytes    int64
	MaxUploadBytes     int64
	TransferChunkBytes int64
	MaxConcurrent      int
	UploadTTL          time.Duration
	ArtifactTTL        time.Duration
}
