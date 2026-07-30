// Package service composes configuration, durable tasks and privileged
// operations behind the broker.Handler interface.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/config"
	"github.com/zcr521/android-ai-mcp/internal/model"
	"github.com/zcr521/android-ai-mcp/internal/ops"
	"github.com/zcr521/android-ai-mcp/internal/schema"
	"github.com/zcr521/android-ai-mcp/internal/tasks"
)

type Service struct {
	startedAt  time.Time
	configPath string
	mu         sync.RWMutex
	config     config.Config
	operations *ops.Manager
	tasks      *tasks.Manager
	probeOnce  sync.Once
	probe      map[string]any
}

func New(configPath string, cfg config.Config) (*Service, error) {
	operations := ops.New(ops.Config{
		WorkDir:      cfg.Paths.WorkDir,
		StateDir:     cfg.Paths.StateDir,
		ShellTimeout: time.Duration(cfg.Limits.ShellTimeoutSeconds) * time.Second,
	})
	service := &Service{
		startedAt:  time.Now().UTC(),
		configPath: configPath,
		config:     cfg,
		operations: operations,
	}
	manager, err := tasks.New(
		filepath.Join(cfg.Paths.StateDir, "tasks"),
		cfg.Limits.TotalTasks,
		cfg.Limits.HeavyTasks,
		service.runTask,
	)
	if err != nil {
		return nil, err
	}
	service.tasks = manager
	manager.ResumeInterrupted()
	return service, nil
}

func (s *Service) Close() {
	if s.tasks != nil {
		s.tasks.Close()
	}
}

func (s *Service) Call(ctx context.Context, tool string, args map[string]any) model.Result {
	started := time.Now()
	if err := schema.ValidateInvocation(tool, args); err != nil {
		result := model.Failure("INVALID_ARGUMENT", "工具参数未通过 Schema 校验", "ValidationError", err.Error())
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	var result model.Result
	switch tool {
	case "zcr521_status":
		status, err := s.Status(ctx)
		if err != nil {
			result = model.Failure("STATUS_FAILED", "读取状态失败", "StatusError", err.Error())
		} else {
			result = model.Success("OK", "状态读取成功", status)
			result.Strategy = "broker"
		}
	case "zcr521_config":
		result = s.configCall(args)
	case "zcr521_task":
		result = s.taskCall(ctx, args)
	default:
		background, _ := args["background"].(bool)
		if background {
			cleanArgs := cloneMap(args)
			delete(cleanArgs, "background")
			task, err := s.tasks.Submit(tool, cleanArgs, isHeavy(tool), true)
			if err != nil {
				result = model.Failure("TASK_SUBMIT_FAILED", "后台任务提交失败", "TaskError", err.Error())
			} else {
				result = model.Success("TASK_ACCEPTED", "任务已进入后台队列", map[string]any{
					"taskId": task.ID,
					"status": task.Status,
				})
				result.TaskID = task.ID
				result.Strategy = "durable_task"
			}
		} else {
			result = convertOps(s.operations.Execute(ctx, ops.Request{Tool: tool, Args: args}))
		}
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

func (s *Service) Status(ctx context.Context) (map[string]any, error) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()
	taskList := s.tasks.List()
	active := 0
	for _, task := range taskList {
		if !task.Terminal() {
			active++
		}
	}
	s.probeOnce.Do(func() {
		probeContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s.probe = s.collectProbe(probeContext)
	})
	probe := cloneMap(s.probe)
	return map[string]any{
		"name":              buildinfo.Name,
		"version":           buildinfo.Version,
		"commit":            buildinfo.Commit,
		"buildTime":         buildinfo.BuildTime,
		"protocolCurrent":   buildinfo.ProtocolCurrent,
		"protocolPrevious":  buildinfo.ProtocolPrevious,
		"protocolLegacySse": buildinfo.ProtocolLegacySSE,
		"moduleId":          buildinfo.ModuleID,
		"uptimeSeconds":     int64(time.Since(s.startedAt).Seconds()),
		"effectiveUid":      effectiveUID(),
		"root":              effectiveUID() == 0,
		"security": map[string]any{
			"anonymous":      cfg.Security.Anonymous,
			"lanEnabled":     cfg.Network.ListenLAN,
			"onLinkOnly":     cfg.Security.OnLinkOnly,
			"warning":        "同网段设备可获得完整 Root；切勿将端口映射到公网。",
			"frontendTarget": cfg.Security.DropFrontendUID,
		},
		"addresses": map[string]any{
			"port":      cfg.Network.Port,
			"loopback":  cfg.Network.ListenLoopback,
			"lan":       cfg.Network.ListenLAN,
			"mcp":       "/mcp",
			"legacySse": cfg.Network.LegacySSE,
		},
		"paths": map[string]any{
			"stateDir": cfg.Paths.StateDir,
			"workDir":  cfg.Paths.WorkDir,
		},
		"tasks": map[string]any{
			"active": active,
			"total":  len(taskList),
			"limit":  cfg.Limits.TotalTasks,
			"heavy":  cfg.Limits.HeavyTasks,
		},
		"androidVersion": probe["androidVersion"],
		"apiLevel":       probe["apiLevel"],
		"abiList":        probe["abiList"],
		"rootFramework":  probe["rootFramework"],
		"capabilities":   probe["capabilities"],
	}, nil
}

func (s *Service) collectProbe(ctx context.Context) map[string]any {
	capability := s.operations.Execute(ctx, ops.Request{
		Tool: "zcr521_capabilities",
		Args: map[string]any{"action": "get"},
	})
	device := s.operations.Execute(ctx, ops.Request{
		Tool: "zcr521_device_info",
		Args: map[string]any{"action": "get"},
	})
	result := map[string]any{
		"androidVersion": "",
		"apiLevel":       "",
		"abiList":        "",
		"rootFramework":  map[string]any{"name": "unknown"},
		"capabilities": map[string]any{
			"success": capability.Success,
			"data":    capability.Data,
			"error":   capability.Error,
		},
	}
	if data, ok := capability.Data.(map[string]any); ok {
		if framework, exists := data["rootFramework"]; exists {
			result["rootFramework"] = framework
		}
	}
	if data, ok := device.Data.(map[string]any); ok {
		if properties, ok := data["properties"].(map[string]string); ok {
			result["androidVersion"] = properties["androidVersion"]
			result["apiLevel"] = properties["apiLevel"]
			result["abiList"] = properties["abiList"]
		} else if properties, ok := data["properties"].(map[string]any); ok {
			result["androidVersion"] = properties["androidVersion"]
			result["apiLevel"] = properties["apiLevel"]
			result["abiList"] = properties["abiList"]
		}
		if framework, exists := data["rootFramework"]; exists {
			result["rootFramework"] = framework
		}
	}
	return result
}

func (s *Service) TaskGet(_ context.Context, id string) (any, error) {
	if id == "" {
		return nil, errors.New("taskId is required")
	}
	task, ok := s.tasks.Get(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	return task, nil
}

func (s *Service) TaskList(context.Context) (any, error) {
	return s.tasks.List(), nil
}

func (s *Service) TaskUpdate(_ context.Context, id string, progress float64, message string) (any, error) {
	return s.tasks.Update(id, progress, message)
}

func (s *Service) TaskCancel(_ context.Context, id string) (any, error) {
	return s.tasks.Cancel(id)
}

func (s *Service) runTask(ctx context.Context, task tasks.Task, reporter tasks.Reporter) model.Result {
	var args map[string]any
	if err := json.Unmarshal(task.Arguments, &args); err != nil {
		return model.Failure("INVALID_ARGUMENT", "无法恢复任务参数", "DecodeError", err.Error())
	}
	_ = reporter.Progress(0.01, "任务开始")
	result := convertOps(s.operations.Execute(ctx, ops.Request{Tool: task.Tool, Args: args}))
	if result.Stdout != "" {
		_ = reporter.Log("stdout", result.Stdout)
	}
	if result.Stderr != "" {
		_ = reporter.Log("stderr", result.Stderr)
	}
	if result.Success {
		_ = reporter.Progress(1, "任务完成")
	}
	result.TaskID = task.ID
	return result
}

func (s *Service) taskCall(ctx context.Context, args map[string]any) model.Result {
	action, _ := args["action"].(string)
	id, _ := args["taskId"].(string)
	var (
		data any
		err  error
	)
	switch action {
	case "get":
		data, err = s.TaskGet(ctx, id)
	case "list":
		data, err = s.TaskList(ctx)
	case "cancel":
		data, err = s.TaskCancel(ctx, id)
	case "update":
		progress, _ := number(args["progress"])
		message, _ := args["message"].(string)
		data, err = s.TaskUpdate(ctx, id, progress, message)
	case "logs":
		if id == "" {
			err = errors.New("taskId is required")
		} else {
			cfg := s.Config()
			path := filepath.Join(cfg.Paths.StateDir, "tasks", id+".jsonl")
			limit := cfg.Limits.ResultPreviewBytes
			if requested, ok := number(args["limit"]); ok && requested > 0 && int64(requested) < limit {
				limit = int64(requested)
			}
			var (
				raw       []byte
				start     int64
				totalSize int64
			)
			raw, start, totalSize, err = readFileTail(path, limit)
			if err == nil {
				data = map[string]any{
					"taskId":     id,
					"log":        string(raw),
					"offset":     start,
					"nextOffset": totalSize,
					"truncated":  start > 0,
				}
			}
		}
	default:
		err = fmt.Errorf("unsupported task action %q", action)
	}
	if err != nil {
		return model.Failure("TASK_FAILED", "任务操作失败", "TaskError", err.Error())
	}
	result := model.Success("OK", "任务操作成功", data)
	result.Strategy = "durable_task"
	return result
}

func readFileTail(path string, limit int64) ([]byte, int64, int64, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit))
	return raw, start, info.Size(), err
}

func (s *Service) configCall(args map[string]any) model.Result {
	action, _ := args["action"].(string)
	switch action {
	case "get":
		return model.Success("OK", "配置读取成功", s.Config())
	case "validate":
		candidate, err := decodeConfig(args)
		if err != nil {
			return model.Failure("INVALID_CONFIG", "配置解码失败", "ConfigError", err.Error())
		}
		if err := candidate.Validate(); err != nil {
			return model.Failure("INVALID_CONFIG", "配置校验失败", "ConfigError", err.Error())
		}
		return model.Success("OK", "配置有效", candidate)
	case "update":
		candidate, err := decodeConfig(args)
		if err != nil {
			return model.Failure("INVALID_CONFIG", "配置解码失败", "ConfigError", err.Error())
		}
		current := s.Config()
		if filepath.Clean(candidate.Paths.StateDir) != filepath.Clean(current.Paths.StateDir) {
			return model.Failure("IMMUTABLE_CONFIG", "内部状态目录不能在线迁移", "ConfigError", "paths.stateDir is immutable")
		}
		if err := config.Save(s.configPath, candidate); err != nil {
			return model.Failure("CONFIG_WRITE_FAILED", "配置原子更新失败", "ConfigError", err.Error())
		}
		s.mu.Lock()
		s.config = candidate
		s.mu.Unlock()
		result := model.Success("OK", "配置已更新；网络与并发项在服务重启后完全生效", candidate)
		result.RebootRequired = true
		result.Strategy = "atomic_json"
		return result
	case "reset":
		current := s.Config()
		candidate := config.Default()
		candidate.Paths.StateDir = current.Paths.StateDir
		candidate.Paths.WorkDir = current.Paths.WorkDir
		candidate.Paths.DownloadsDir = ""
		candidate.Paths.UploadsDir = ""
		candidate.Paths.ArtifactsDir = ""
		candidate.Paths.TempDir = ""
		candidate = config.Normalize(candidate)
		if err := config.Save(s.configPath, candidate); err != nil {
			return model.Failure("CONFIG_WRITE_FAILED", "恢复默认配置失败", "ConfigError", err.Error())
		}
		s.mu.Lock()
		s.config = candidate
		s.mu.Unlock()
		result := model.Success("OK", "配置已恢复默认值", candidate)
		result.RebootRequired = true
		result.Strategy = "atomic_json"
		return result
	case "export":
		cfg := s.Config()
		destination, _ := args["destination"].(string)
		if destination == "" {
			destination = filepath.Join(cfg.Paths.WorkDir, "output", "zcr521-config.json")
		} else if !filepath.IsAbs(destination) {
			destination = filepath.Join(cfg.Paths.WorkDir, destination)
		}
		if err := config.Save(destination, cfg); err != nil {
			return model.Failure("CONFIG_EXPORT_FAILED", "配置导出失败", "FilesystemError", err.Error())
		}
		result := model.Success("OK", "配置已导出", map[string]any{"path": destination})
		result.Strategy = "json_export"
		return result
	default:
		return model.Failure("INVALID_ARGUMENT", "不支持的配置操作", "ValidationError", action)
	}
}

func (s *Service) Config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func decodeConfig(args map[string]any) (config.Config, error) {
	value, ok := args["config"]
	if !ok {
		return config.Config{}, errors.New("config object is required")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return config.Config{}, err
	}
	var candidate config.Config
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return config.Config{}, err
	}
	candidate = config.Normalize(candidate)
	return candidate, candidate.Validate()
}

func convertOps(source ops.Result) model.Result {
	result := model.Result{
		Success:        source.Success,
		Code:           source.Code,
		Message:        source.Message,
		Data:           source.Data,
		Stdout:         source.Stdout,
		Stderr:         source.Stderr,
		ExitCode:       source.ExitCode,
		DurationMS:     source.DurationMs,
		TaskID:         source.TaskID,
		RebootRequired: source.RebootRequired,
		Artifacts:      []model.Artifact{},
		Strategy:       source.Strategy,
	}
	if source.Error != "" {
		result.Error = &model.Error{
			Type:    source.Code,
			Details: source.Error,
			Fields:  map[string]any{},
		}
	}
	for _, path := range source.Artifacts {
		result.Artifacts = append(result.Artifacts, model.Artifact{Name: filepath.Base(path), Path: path})
	}
	return result
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}

func isHeavy(tool string) bool {
	definition, ok := schema.Find(buildinfo.ProtocolCurrent, tool)
	return ok && definition.Annotations.Heavy
}
