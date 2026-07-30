// Package config owns validation, corruption recovery and atomic updates.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/atomicfile"
	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion int             `json:"schemaVersion"`
	Network       Network         `json:"network"`
	Paths         Paths           `json:"paths"`
	Limits        Limits          `json:"limits"`
	Security      Security        `json:"security"`
	Capabilities  map[string]bool `json:"capabilities"`
}

type Network struct {
	Port           int      `json:"port"`
	ListenLoopback bool     `json:"listenLoopback"`
	ListenLAN      bool     `json:"listenLan"`
	LegacySSE      bool     `json:"legacySse"`
	AllowedOrigins []string `json:"allowedOrigins"`
}

type Paths struct {
	StateDir     string `json:"stateDir"`
	WorkDir      string `json:"workDir"`
	DownloadsDir string `json:"downloadsDir"`
	UploadsDir   string `json:"uploadsDir"`
	ArtifactsDir string `json:"artifactsDir"`
	TempDir      string `json:"tempDir"`
}

type Limits struct {
	MaxConnections        int   `json:"maxConnections"`
	MaxRequestBytes       int64 `json:"maxRequestBytes"`
	TotalTasks            int   `json:"totalTasks"`
	HeavyTasks            int   `json:"heavyTasks"`
	ShellTimeoutSeconds   int   `json:"shellTimeoutSeconds"`
	TransferChunkBytes    int64 `json:"transferChunkBytes"`
	TransferMaxBytes      int64 `json:"transferMaxBytes"`
	ResultPreviewBytes    int64 `json:"resultPreviewBytes"`
	ArtifactTTLSeconds    int   `json:"artifactTtlSeconds"`
	ShutdownGraceSeconds  int   `json:"shutdownGraceSeconds"`
	UploadIdleTTLSeconds  int   `json:"uploadIdleTtlSeconds"`
	DownloadRetryAttempts int   `json:"downloadRetryAttempts"`
}

type Security struct {
	Anonymous       bool `json:"anonymous"`
	OnLinkOnly      bool `json:"onLinkOnly"`
	ValidateHost    bool `json:"validateHost"`
	ValidateOrigin  bool `json:"validateOrigin"`
	AllowCORS       bool `json:"allowCors"`
	DropFrontendUID int  `json:"dropFrontendUid"`
}

type LoadReport struct {
	Created       bool   `json:"created"`
	Recovered     bool   `json:"recovered"`
	CorruptBackup string `json:"corruptBackup"`
	Warning       string `json:"warning"`
}

func Default() Config {
	work := buildinfo.DefaultWorkDir
	state := buildinfo.DefaultStateDir
	return Config{
		SchemaVersion: SchemaVersion,
		Network: Network{
			Port:           buildinfo.DefaultPort,
			ListenLoopback: true,
			ListenLAN:      true,
			LegacySSE:      true,
			AllowedOrigins: []string{},
		},
		Paths: Paths{
			StateDir:     state,
			WorkDir:      work,
			DownloadsDir: filepath.Join(work, "downloads"),
			UploadsDir:   filepath.Join(work, "uploads"),
			ArtifactsDir: filepath.Join(work, "output"),
			TempDir:      filepath.Join(state, "tmp"),
		},
		Limits: Limits{
			MaxConnections:        32,
			MaxRequestBytes:       8 << 20,
			TotalTasks:            8,
			HeavyTasks:            2,
			ShellTimeoutSeconds:   120,
			TransferChunkBytes:    4 << 20,
			TransferMaxBytes:      0,
			ResultPreviewBytes:    1 << 20,
			ArtifactTTLSeconds:    3600,
			ShutdownGraceSeconds:  15,
			UploadIdleTTLSeconds:  86400,
			DownloadRetryAttempts: 4,
		},
		Security: Security{
			Anonymous:       true,
			OnLinkOnly:      true,
			ValidateHost:    true,
			ValidateOrigin:  true,
			AllowCORS:       false,
			DropFrontendUID: 2000,
		},
		Capabilities: map[string]bool{},
	}
}

func (c Config) Validate() error {
	var problems []string
	if c.SchemaVersion != SchemaVersion {
		problems = append(problems, fmt.Sprintf("schemaVersion 必须为 %d", SchemaVersion))
	}
	if c.Network.Port < 1 || c.Network.Port > 65535 {
		problems = append(problems, "network.port 必须在 1..65535")
	}
	if !c.Network.ListenLoopback && !c.Network.ListenLAN {
		problems = append(problems, "至少启用一个监听范围")
	}
	for _, origin := range c.Network.AllowedOrigins {
		if origin == "*" {
			problems = append(problems, "不允许通配 Origin")
		}
	}
	requiredPaths := map[string]string{
		"paths.stateDir": c.Paths.StateDir,
		"paths.workDir":  c.Paths.WorkDir,
	}
	for name, value := range requiredPaths {
		if strings.TrimSpace(value) == "" || !portableAbs(value) {
			problems = append(problems, name+" 必须是绝对路径")
		}
	}
	if c.Limits.TotalTasks < 1 || c.Limits.TotalTasks > 128 {
		problems = append(problems, "limits.totalTasks 必须在 1..128")
	}
	if c.Limits.HeavyTasks < 1 || c.Limits.HeavyTasks > c.Limits.TotalTasks {
		problems = append(problems, "limits.heavyTasks 必须在 1..totalTasks")
	}
	if c.Limits.ShellTimeoutSeconds < 1 || c.Limits.ShellTimeoutSeconds > 86400 {
		problems = append(problems, "limits.shellTimeoutSeconds 必须在 1..86400")
	}
	if c.Limits.TransferChunkBytes < 64<<10 || c.Limits.TransferChunkBytes > 64<<20 {
		problems = append(problems, "limits.transferChunkBytes 必须在 64 KiB..64 MiB")
	}
	if c.Limits.MaxRequestBytes < c.Limits.TransferChunkBytes {
		problems = append(problems, "limits.maxRequestBytes 不得小于传输块")
	}
	if c.Security.AllowCORS {
		problems = append(problems, "本版本禁止开放 CORS")
	}
	if !c.Security.Anonymous {
		problems = append(problems, "本版本固定为匿名连接，不接受会造成认证已启用假象的配置")
	}
	if !c.Security.OnLinkOnly || !c.Security.ValidateHost || !c.Security.ValidateOrigin {
		problems = append(problems, "onLinkOnly、validateHost、validateOrigin 必须保持启用")
	}
	if c.Security.DropFrontendUID != 2000 {
		problems = append(problems, "security.dropFrontendUid 必须为 Android shell UID 2000")
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func portableAbs(path string) bool {
	return filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/")
}

func Load(path string) (Config, LoadReport, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		cfg.Paths.StateDir = filepath.Dir(path)
		derivePaths(&cfg)
		if err := Save(path, cfg); err != nil {
			return Config{}, LoadReport{}, err
		}
		return cfg, LoadReport{Created: true}, nil
	}
	if err != nil {
		return Config{}, LoadReport{}, fmt.Errorf("读取配置: %w", err)
	}
	cfg := Default()
	cfg.Paths.StateDir = filepath.Dir(path)
	cfg.Paths.DownloadsDir = ""
	cfg.Paths.UploadsDir = ""
	cfg.Paths.ArtifactsDir = ""
	cfg.Paths.TempDir = ""
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Validate() != nil {
		reason := err
		if reason == nil {
			reason = cfg.Validate()
		}
		backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		if renameErr := os.Rename(path, backup); renameErr != nil {
			return Config{}, LoadReport{}, fmt.Errorf("配置损坏且备份失败 (%v): %w", reason, renameErr)
		}
		cfg = Default()
		cfg.Paths.StateDir = filepath.Dir(path)
		derivePaths(&cfg)
		if saveErr := Save(path, cfg); saveErr != nil {
			return Config{}, LoadReport{}, fmt.Errorf("配置恢复保存失败: %w", saveErr)
		}
		return cfg, LoadReport{
			Recovered:     true,
			CorruptBackup: backup,
			Warning:       "配置损坏，已备份并恢复默认值: " + reason.Error(),
		}, nil
	}
	derivePaths(&cfg)
	return cfg, LoadReport{}, nil
}

func Save(path string, cfg Config) error {
	derivePaths(&cfg)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码配置: %w", err)
	}
	data = append(data, '\n')
	return atomicfile.Write(path, data, 0o600)
}

// Normalize returns a copy with every derived path and collection populated.
// Callers that keep a successfully saved Config in memory must use the same
// normalized representation that is written to disk.
func Normalize(cfg Config) Config {
	derivePaths(&cfg)
	return cfg
}

func EnsureDirectories(cfg Config) error {
	if err := EnsureStateDirectories(cfg); err != nil {
		return err
	}
	return EnsureWorkDirectories(cfg)
}

func EnsureStateDirectories(cfg Config) error {
	return ensureDirectories("内部状态", []string{
		cfg.Paths.StateDir,
		cfg.Paths.TempDir,
		filepath.Join(cfg.Paths.StateDir, "run"),
		filepath.Join(cfg.Paths.StateDir, "tasks"),
		filepath.Join(cfg.Paths.StateDir, "logs"),
		filepath.Join(cfg.Paths.StateDir, "versions"),
	})
}

func EnsureWorkDirectories(cfg Config) error {
	return ensureDirectories("用户工作", []string{
		cfg.Paths.WorkDir,
	})
}

func ensureDirectories(scope string, dirs []string) error {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建%s目录 %s: %w", scope, dir, err)
		}
	}
	return nil
}

func ListenAddresses(cfg Config) []string {
	var result []string
	port := fmt.Sprintf("%d", cfg.Network.Port)
	if cfg.Network.ListenLoopback {
		result = append(result, net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("::1", port))
	}
	if cfg.Network.ListenLAN {
		result = append(result, net.JoinHostPort("0.0.0.0", port), net.JoinHostPort("::", port))
	}
	return result
}

func derivePaths(cfg *Config) {
	if cfg.Paths.DownloadsDir == "" {
		cfg.Paths.DownloadsDir = filepath.Join(cfg.Paths.WorkDir, "downloads")
	}
	if cfg.Paths.UploadsDir == "" {
		cfg.Paths.UploadsDir = filepath.Join(cfg.Paths.WorkDir, "uploads")
	}
	if cfg.Paths.ArtifactsDir == "" {
		cfg.Paths.ArtifactsDir = filepath.Join(cfg.Paths.WorkDir, "output")
	}
	if cfg.Paths.TempDir == "" {
		cfg.Paths.TempDir = filepath.Join(cfg.Paths.StateDir, "tmp")
	}
	if cfg.Capabilities == nil {
		cfg.Capabilities = map[string]bool{}
	}
}
