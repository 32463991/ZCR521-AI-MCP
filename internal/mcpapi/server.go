// Package mcpapi binds the official MCP Go SDK to the privileged broker.
package mcpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/model"
	"github.com/zcr521/android-ai-mcp/internal/schema"
)

type Caller interface {
	Call(context.Context, string, map[string]any) model.Result
}

type Publisher func(path, name, sha256 string, ttl time.Duration) (model.Artifact, error)

type API struct {
	server    *mcp.Server
	caller    Caller
	publishMu sync.RWMutex
	publisher Publisher
}

func New(caller Caller) (*API, error) {
	if caller == nil {
		return nil, fmt.Errorf("MCP caller is required")
	}
	// The SDK exposes prompts/list even when the prompt store is empty. Declare
	// that empty, stable collection so current-protocol discovery matches the
	// methods the server actually honors. The 48-tool catalog is static for a
	// running process, therefore listChanged must remain false.
	capabilities := &mcp.ServerCapabilities{
		Prompts: &mcp.PromptCapabilities{ListChanged: false},
		Tools:   &mcp.ToolCapabilities{ListChanged: false},
	}
	// SEP-2663 currently defines no extension-specific settings. An empty
	// object is the complete capability declaration.
	capabilities.AddExtension("io.modelcontextprotocol/tasks", nil)
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "zcr521-android-mcp",
		Title:       buildinfo.Name,
		Description: "Android Root 文件、Shell、应用、系统、网络、诊断与自动化工具。",
		Version:     buildinfo.Version,
	}, &mcp.ServerOptions{
		Instructions: "这是完整 Root 能力服务。破坏性操作必须先确认目标；大文件应使用 ResourceLink 或分块传输。",
		Capabilities: capabilities,
	})
	api := &API{server: server, caller: caller}
	for _, definition := range schema.All(buildinfo.ProtocolCurrent).Tools {
		api.register(definition)
	}
	return api, nil
}

func (a *API) Server() *mcp.Server {
	return a.server
}

func (a *API) SetPublisher(publisher Publisher) {
	a.publishMu.Lock()
	a.publisher = publisher
	a.publishMu.Unlock()
}

func (a *API) register(definition schema.Tool) {
	name := definition.Name
	description := definition.Description
	readOnly := definition.Annotations.ReadOnly
	idempotent := definition.Annotations.Idempotent
	destructive := definition.Annotations.Destructive
	openWorld := name == "zcr521_download" || name == "zcr521_network"
	a.server.AddTool(&mcp.Tool{
		Name:         name,
		Title:        definition.Title,
		Description:  description,
		InputSchema:  definition.InputSchema,
		OutputSchema: definition.OutputSchema,
		Annotations: &mcp.ToolAnnotations{
			Title:           definition.Title,
			ReadOnlyHint:    readOnly,
			IdempotentHint:  idempotent,
			DestructiveHint: boolPointer(destructive),
			OpenWorldHint:   boolPointer(openWorld),
		},
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if request != nil && request.Params != nil && len(request.Params.Arguments) > 0 {
			decoderErr := json.Unmarshal(request.Params.Arguments, &args)
			if decoderErr != nil {
				result := model.Failure("INVALID_ARGUMENT", "工具参数必须是 JSON 对象", "DecodeError", decoderErr.Error())
				return toolResult(result), nil
			}
		}
		result := a.caller.Call(ctx, name, args)
		a.publishArtifacts(&result)
		return toolResult(result), nil
	})
}

func (a *API) publishArtifacts(result *model.Result) {
	a.publishMu.RLock()
	publisher := a.publisher
	a.publishMu.RUnlock()
	if publisher == nil {
		return
	}
	for index := range result.Artifacts {
		artifact := &result.Artifacts[index]
		if artifact.URI != "" || artifact.Path == "" {
			continue
		}
		published, err := publisher(artifact.Path, artifact.Name, artifact.SHA256, time.Hour)
		if err != nil {
			continue
		}
		artifact.URI = published.URI
		if artifact.Size == 0 {
			artifact.Size = published.Size
		}
		if artifact.ExpiresAt.IsZero() {
			artifact.ExpiresAt = published.ExpiresAt
		}
	}
}

func toolResult(result model.Result) *mcp.CallToolResult {
	raw, err := json.Marshal(result)
	if err != nil {
		fallback := model.Failure("RESULT_ENCODING_FAILED", "工具结果编码失败", "EncodingError", err.Error())
		raw, _ = json.Marshal(fallback)
		result = fallback
	}
	content := []mcp.Content{&mcp.TextContent{Text: string(raw)}}
	for _, artifact := range result.Artifacts {
		if artifact.URI == "" {
			continue
		}
		name := artifact.Name
		if name == "" {
			name = filepath.Base(artifact.Path)
		}
		size := artifact.Size
		mediaType := artifact.MediaType
		if mediaType == "" {
			mediaType = mime.TypeByExtension(filepath.Ext(name))
		}
		content = append(content, &mcp.ResourceLink{
			URI:         artifact.URI,
			Name:        name,
			Title:       name,
			Description: "ZCR521 临时流式下载；过期后需重新导出。",
			MIMEType:    mediaType,
			Size:        &size,
		})
	}
	return &mcp.CallToolResult{
		Content:           content,
		StructuredContent: result,
		IsError:           !result.Success,
	}
}

func boolPointer(value bool) *bool {
	return &value
}
