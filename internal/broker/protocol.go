// Package broker implements the root-only Unix socket boundary between the
// unprivileged HTTP frontend and Android operations.
package broker

import (
	"context"
	"encoding/json"

	"github.com/zcr521/android-ai-mcp/internal/model"
)

const WireVersion = 1

type Request struct {
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	TimeoutMS int64           `json:"timeoutMs"`
}

type Response struct {
	Version   int          `json:"version"`
	ID        string       `json:"id"`
	Result    model.Result `json:"result"`
	Data      any          `json:"data"`
	ErrorCode string       `json:"errorCode,omitempty"`
	Error     string       `json:"error"`
}

type Handler interface {
	Call(context.Context, string, map[string]any) model.Result
	Status(context.Context) (map[string]any, error)
	TaskGet(context.Context, string) (any, error)
	TaskList(context.Context) (any, error)
	TaskUpdate(context.Context, string, float64, string) (any, error)
	TaskCancel(context.Context, string) (any, error)
}

type Peer struct {
	PID      int    `json:"pid"`
	UID      uint32 `json:"uid"`
	GID      uint32 `json:"gid"`
	Verified bool   `json:"verified"`
	Strategy string `json:"strategy"`
}
