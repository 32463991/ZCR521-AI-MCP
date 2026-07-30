package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zcr521/android-ai-mcp/internal/atomicfile"
	"github.com/zcr521/android-ai-mcp/internal/broker"
	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/config"
	"github.com/zcr521/android-ai-mcp/internal/frontend"
	"github.com/zcr521/android-ai-mcp/internal/mcpapi"
	"github.com/zcr521/android-ai-mcp/internal/model"
	"github.com/zcr521/android-ai-mcp/internal/ops"
	"github.com/zcr521/android-ai-mcp/internal/schema"
	"github.com/zcr521/android-ai-mcp/internal/service"
	"github.com/zcr521/android-ai-mcp/internal/supervisor"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if os.Args[1] == "supervisor" || os.Args[1] == "broker" {
		if err := enforceModulePropIntegrity(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "zcr521d:", err)
			os.Exit(1)
		}
	}
	var err error
	switch os.Args[1] {
	case "supervisor":
		err = runSupervisor(os.Args[2:])
	case "broker":
		err = runBroker(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "status":
		err = runStatus(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "config":
		err = runConfig(os.Args[2:])
	case "schema":
		err = runSchema(os.Args[2:])
	case "version", "--version", "-version":
		printVersion()
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "zcr521d:", err)
		os.Exit(1)
	}
}

func runSupervisor(arguments []string) error {
	flags := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	stateDir := flags.String("state-dir", envOr("ZCR521_STATE_DIR", buildinfo.DefaultStateDir), "内部状态目录")
	stableAfter := flags.Duration("stable-after", 5*time.Minute, "候选版本稳定标记时间")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	configPath := filepath.Join(*stateDir, "config.json")
	cfg, report, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if report.Warning != "" {
		_, _ = fmt.Fprintln(os.Stderr, report.Warning)
	}
	if err := config.EnsureStateDirectories(cfg); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	options := supervisor.DefaultOptions()
	options.StateDir = *stateDir
	options.StableAfter = *stableAfter
	options.Logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	go ensureWorkDirectories(ctx, cfg, options.Logger)
	go maintainMCPAddressFile(ctx, *stateDir, cfg.Network.Port, cfg.Network.ListenLAN, options.Logger)
	return supervisor.Run(ctx, options)
}

func runBroker(arguments []string) error {
	flags := flag.NewFlagSet("broker", flag.ContinueOnError)
	stateDir := flags.String("state-dir", envOr("ZCR521_STATE_DIR", buildinfo.DefaultStateDir), "内部状态目录")
	socket := flags.String("socket", "", "Unix socket")
	pidFile := flags.String("frontend-pid-file", "", "frontend PID 文件")
	allowHost := flags.Bool("allow-unverified-host", runtime.GOOS != "android" && runtime.GOOS != "linux", "仅供非 Linux 主机测试")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if runtime.GOOS == "android" && effectiveUID() != 0 {
		return errors.New("broker 必须以 uid=0 运行")
	}
	if *socket == "" {
		*socket = filepath.Join(*stateDir, "run", buildinfo.DefaultSocketName)
	}
	if *pidFile == "" {
		*pidFile = filepath.Join(*stateDir, "run", "frontend.pid")
	}
	configPath := filepath.Join(*stateDir, "config.json")
	cfg, report, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if report.Warning != "" {
		_, _ = fmt.Fprintln(os.Stderr, report.Warning)
	}
	if err := config.EnsureStateDirectories(cfg); err != nil {
		return err
	}
	if err := config.EnsureWorkDirectories(cfg); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "共享存储尚未就绪，基础 MCP 服务继续启动:", err)
	}
	backend, err := service.New(configPath, cfg)
	if err != nil {
		return err
	}
	defer backend.Close()
	server, err := broker.NewServer(broker.ServerOptions{
		SocketPath:             *socket,
		FrontendPIDFile:        *pidFile,
		AllowedUIDs:            []uint32{0, 2000},
		SocketGID:              2000,
		MaxRequestBytes:        cfg.Limits.MaxRequestBytes,
		MaxConcurrent:          cfg.Limits.TotalTasks * 2,
		AllowUnverifiedForHost: *allowHost,
	}, backend)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go ensureWorkDirectories(ctx, cfg, slog.Default())
	return server.ListenAndServe(ctx)
}

func ensureWorkDirectories(ctx context.Context, cfg config.Config, logger *slog.Logger) {
	delay := time.Second
	reportedUnavailable := false
	for {
		err := config.EnsureWorkDirectories(cfg)
		if err == nil {
			if reportedUnavailable {
				logger.Info("共享存储已就绪，用户工作目录创建完成", "path", cfg.Paths.WorkDir)
			}
			return
		}
		if !reportedUnavailable {
			logger.Warn("共享存储尚未就绪；将后台重试且不阻塞 MCP 启动", "error", err)
			reportedUnavailable = true
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func runServe(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	stateDir := flags.String("state-dir", envOr("ZCR521_STATE_DIR", buildinfo.DefaultStateDir), "内部状态目录")
	configPath := flags.String("config", "", "frontend 配置副本")
	socket := flags.String("socket", "", "broker Unix socket")
	degraded := flags.String("security-degraded", "", "安全降级原因")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *configPath == "" {
		*configPath = filepath.Join(*stateDir, "config.json")
	}
	if *socket == "" {
		*socket = filepath.Join(*stateDir, "run", buildinfo.DefaultSocketName)
	}
	cfg, report, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if report.Warning != "" {
		_, _ = fmt.Fprintln(os.Stderr, report.Warning)
	}
	rootClient := broker.Client{
		SocketPath:      *socket,
		DialTimeout:     5 * time.Second,
		MaxResponseSize: max(cfg.Limits.MaxRequestBytes, 16<<20),
	}
	api, err := mcpapi.New(rootClient)
	if err != nil {
		return err
	}
	taskExtension := mcpapi.TasksHandler{
		Client:       rootClient,
		Caller:       rootClient,
		MaxBodyBytes: cfg.Limits.MaxRequestBytes,
		PollInterval: 500 * time.Millisecond,
	}
	mcpHandlers := frontend.NewOfficialMCPHandlers(
		func(*http.Request) *mcp.Server {
			return api.Server()
		},
		frontend.OfficialMCPOptions{
			MaxRequestBodyBytes: cfg.Limits.MaxRequestBytes,
			SessionTimeout:      30 * time.Minute,
			Compatibility:       taskExtension,
		},
	)
	listenAddress := "127.0.0.1"
	if cfg.Network.ListenLAN {
		listenAddress = "0.0.0.0"
	}
	uploadMax := cfg.Limits.TransferMaxBytes
	if uploadMax <= 0 {
		uploadMax = math.MaxInt64
	}
	publicBroker := frontendBroker{client: rootClient, securityDegraded: *degraded}
	public, err := frontend.New(frontend.Options{
		Broker:             publicBroker,
		MCP:                mcpHandlers,
		Version:            buildinfo.Version,
		ProtocolCurrent:    buildinfo.ProtocolCurrent,
		ProtocolPrevious:   buildinfo.ProtocolPrevious,
		ProtocolLegacy:     buildinfo.ProtocolLegacySSE,
		ListenAddr:         listenAddress,
		Port:               cfg.Network.Port,
		WorkDir:            cfg.Paths.WorkDir,
		TransferDir:        filepath.Join(cfg.Paths.StateDir, "run", "transfer"),
		EnableLegacySSE:    cfg.Network.LegacySSE,
		AllowedOrigins:     cfg.Network.AllowedOrigins,
		MaxRequestBytes:    cfg.Limits.MaxRequestBytes,
		MaxUploadBytes:     uploadMax,
		TransferChunkBytes: cfg.Limits.TransferChunkBytes,
		MaxConcurrent:      cfg.Limits.MaxConnections,
		UploadTTL:          time.Duration(cfg.Limits.UploadIdleTTLSeconds) * time.Second,
		ArtifactTTL:        time.Duration(cfg.Limits.ArtifactTTLSeconds) * time.Second,
	})
	if err != nil {
		return err
	}
	api.SetPublisher(func(path, name, sha256 string, ttl time.Duration) (model.Artifact, error) {
		artifact, err := public.PublishFile(path, name, sha256, ttl)
		if err != nil {
			return model.Artifact{}, err
		}
		artifactHost := "127.0.0.1"
		if cfg.Network.ListenLAN {
			artifactHost = firstOnLinkAddress()
		}
		return model.Artifact{
			Name:      artifact.Name,
			Path:      artifact.Path,
			URI:       "http://" + net.JoinHostPort(artifactHost, strconv.Itoa(cfg.Network.Port)) + artifact.DownloadURL,
			MediaType: mime.TypeByExtension(filepath.Ext(artifact.Name)),
			Size:      artifact.Size,
			SHA256:    artifact.SHA256,
			ExpiresAt: artifact.ExpiresAt,
		}, nil
	})

	httpServer := &http.Server{
		Addr:              public.ListenAddress(),
		Handler:           public,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Limits.ShutdownGraceSeconds)*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	_, _ = fmt.Fprintf(os.Stderr, "ZCR521 MCP frontend 监听 http://%s（匿名 Root LAN=%v）\n", public.ListenAddress(), cfg.Network.ListenLAN)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func runStatus(arguments []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	stateDir := flags.String("state-dir", envOr("ZCR521_STATE_DIR", buildinfo.DefaultStateDir), "内部状态目录")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	cfg, _, err := config.Load(filepath.Join(*stateDir, "config.json"))
	if err != nil {
		return err
	}
	url := "http://127.0.0.1:" + strconv.Itoa(cfg.Network.Port) + "/status"
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("服务未就绪: %w", err)
	}
	defer response.Body.Close()
	var value any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		return err
	}
	return writePrettyJSON(os.Stdout, value)
}

func runDoctor(arguments []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	stateDir := flags.String("state-dir", envOr("ZCR521_STATE_DIR", buildinfo.DefaultStateDir), "内部状态目录")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	configPath := filepath.Join(*stateDir, "config.json")
	cfg, report, err := config.Load(configPath)
	if err != nil {
		return err
	}
	checks := map[string]any{
		"config": map[string]any{
			"valid":     cfg.Validate() == nil,
			"recovered": report.Recovered,
			"warning":   report.Warning,
		},
		"runtime": map[string]any{
			"goos":   runtime.GOOS,
			"goarch": runtime.GOARCH,
			"uid":    effectiveUID(),
			"root":   effectiveUID() == 0,
		},
	}
	manager := ops.New(ops.Config{
		WorkDir:      cfg.Paths.WorkDir,
		StateDir:     cfg.Paths.StateDir,
		ShellTimeout: 30 * time.Second,
	})
	checks["capabilities"] = manager.Execute(context.Background(), ops.Request{
		Tool: "zcr521_capabilities",
		Args: map[string]any{"action": "probe"},
	})
	checks["selfTest"] = manager.Execute(context.Background(), ops.Request{
		Tool: "zcr521_diagnostics",
		Args: map[string]any{"action": "self_test"},
	})
	return writePrettyJSON(os.Stdout, map[string]any{
		"success": true,
		"checks":  checks,
	})
}

func runConfig(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("config 需要 get、validate 或 reset")
	}
	action := arguments[0]
	flags := flag.NewFlagSet("config "+action, flag.ContinueOnError)
	stateDir := flags.String("state-dir", envOr("ZCR521_STATE_DIR", buildinfo.DefaultStateDir), "内部状态目录")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	path := filepath.Join(*stateDir, "config.json")
	switch action {
	case "get":
		cfg, report, err := config.Load(path)
		if err != nil {
			return err
		}
		return writePrettyJSON(os.Stdout, map[string]any{"config": cfg, "loadReport": report})
	case "validate":
		cfg, _, err := config.Load(path)
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		return writePrettyJSON(os.Stdout, map[string]any{"success": true, "message": "配置有效"})
	case "reset":
		current, _, err := config.Load(path)
		if err != nil {
			return err
		}
		cfg := config.Default()
		cfg.Paths.StateDir = current.Paths.StateDir
		cfg.Paths.WorkDir = current.Paths.WorkDir
		cfg.Paths.DownloadsDir = ""
		cfg.Paths.UploadsDir = ""
		cfg.Paths.ArtifactsDir = ""
		cfg.Paths.TempDir = ""
		cfg = config.Normalize(cfg)
		if err := config.Save(path, cfg); err != nil {
			return err
		}
		return writePrettyJSON(os.Stdout, map[string]any{"success": true, "message": "配置已恢复默认值", "config": cfg})
	default:
		return fmt.Errorf("未知 config 操作 %q", action)
	}
}

func runSchema(arguments []string) error {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	output := flags.String("output", "-", "输出文件；- 表示 stdout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	data, err := schema.Marshal(buildinfo.ProtocolCurrent)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *output == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return atomicfile.Write(*output, data, 0o644)
}

func printVersion() {
	_ = writePrettyJSON(os.Stdout, map[string]any{
		"name":      buildinfo.Name,
		"moduleId":  buildinfo.ModuleID,
		"version":   buildinfo.Version,
		"commit":    buildinfo.Commit,
		"buildTime": buildinfo.BuildTime,
		"protocols": []string{buildinfo.ProtocolCurrent, buildinfo.ProtocolPrevious, buildinfo.ProtocolLegacySSE},
	})
}

func usage() {
	_, _ = fmt.Fprintln(os.Stderr, `ZCR521 AI MCP

用法:
  zcr521d supervisor [--state-dir DIR]
  zcr521d broker --state-dir DIR --socket PATH --frontend-pid-file PATH
  zcr521d serve --state-dir DIR --config PATH --socket PATH
  zcr521d status [--state-dir DIR]
  zcr521d doctor [--state-dir DIR]
  zcr521d config get|validate|reset [--state-dir DIR]
  zcr521d schema [--output schemas/tools.json]
  zcr521d version`)
}

func enforceModulePropIntegrity() error {
	if runtime.GOOS != "android" && os.Getenv("ZCR521_REQUIRE_MODULE_PROP_HASH") != "1" {
		return nil
	}
	expected := strings.TrimSpace(buildinfo.ModulePropSHA256)
	if expected == "" {
		return errors.New("缺少内置 module.prop 签名哈希，拒绝运行服务")
	}
	moduleDir := envOr("ZCR521_MODULE_DIR", buildinfo.DefaultModuleDir)
	if err := verifyModulePropIntegrity(moduleDir, expected); err != nil {
		return fmt.Errorf("module.prop 签名哈希校验失败，拒绝运行服务: %w", err)
	}
	return nil
}

func verifyModulePropIntegrity(moduleDir, expected string) error {
	expectedBytes, err := hex.DecodeString(strings.TrimSpace(expected))
	if err != nil || len(expectedBytes) != sha256.Size {
		return errors.New("内置哈希格式无效")
	}
	path := filepath.Join(moduleDir, "module.prop")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("module.prop 必须是普通文件且不能是符号链接")
	}
	if info.Size() > 64<<10 {
		return errors.New("module.prop 超过大小限制")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	actual := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(actual[:], expectedBytes) != 1 {
		return errors.New("文件内容已被修改")
	}
	return nil
}

type frontendBroker struct {
	client           broker.Client
	securityDegraded string
}

func (b frontendBroker) Call(ctx context.Context, tool string, args map[string]any) (frontend.Result, error) {
	result := b.client.Call(ctx, tool, args)
	return frontendResult(result), nil
}

func (b frontendBroker) Status(ctx context.Context) (map[string]any, error) {
	status, err := b.client.Status(ctx)
	if err != nil {
		return nil, err
	}
	if b.securityDegraded == "" {
		status["securityDegraded"] = false
	} else {
		status["securityDegraded"] = true
		status["securityDegradationReason"] = b.securityDegraded
	}
	return status, nil
}

func frontendResult(source model.Result) frontend.Result {
	artifacts := make([]any, 0, len(source.Artifacts))
	for _, artifact := range source.Artifacts {
		artifacts = append(artifacts, artifact)
	}
	return frontend.Result{
		Success:        source.Success,
		Code:           source.Code,
		Message:        source.Message,
		Data:           source.Data,
		Error:          source.Error,
		Stdout:         source.Stdout,
		Stderr:         source.Stderr,
		ExitCode:       source.ExitCode,
		DurationMS:     source.DurationMS,
		TaskID:         source.TaskID,
		RebootRequired: source.RebootRequired,
		Artifacts:      artifacts,
		Strategy:       source.Strategy,
	}
}

func writePrettyJSON(file *os.File, value any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func maintainMCPAddressFile(
	ctx context.Context,
	stateDir string,
	port int,
	listenLAN bool,
	logger *slog.Logger,
) {
	path := filepath.Join(stateDir, "MCP地址.txt")
	lastContent := ""
	lastError := ""
	update := func() {
		address := ""
		if listenLAN {
			if detected, ok := firstLANAddress(); ok {
				address = detected
			}
		}
		value := mcpAddressFileContent(port, listenLAN, address)
		if value == lastContent {
			return
		}
		if err := atomicfile.Write(path, []byte(value), 0o644); err != nil {
			if message := err.Error(); message != lastError {
				logger.Warn("更新 MCP 地址文件失败", "path", path, "error", err)
				lastError = message
			}
			return
		}
		lastContent = value
		lastError = ""
	}
	update()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}

func mcpAddressFileContent(port int, listenLAN bool, address string) string {
	var content strings.Builder
	_, _ = fmt.Fprintf(&content, "本机地址：http://127.0.0.1:%d/mcp\n", port)
	if listenLAN && address != "" {
		_, _ = fmt.Fprintf(&content, "局域网地址：http://%s:%d/mcp\n", address, port)
	}
	return content.String()
}

func firstOnLinkAddress() string {
	if address, ok := firstLANAddress(); ok {
		return address
	}
	return "127.0.0.1"
}

func firstLANAddress() (string, bool) {
	interfaces, err := net.Interfaces()
	if err == nil {
		for preferredOnly := true; ; preferredOnly = false {
			for _, networkInterface := range interfaces {
				if networkInterface.Flags&net.FlagUp == 0 ||
					networkInterface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 ||
					isCellularNetworkInterface(networkInterface.Name) ||
					preferredOnly && !isPreferredLANInterface(networkInterface.Name) {
					continue
				}
				addresses, _ := networkInterface.Addrs()
				for _, address := range addresses {
					var ip net.IP
					switch value := address.(type) {
					case *net.IPNet:
						ip = value.IP
					case *net.IPAddr:
						ip = value.IP
					}
					if ip == nil || ip.IsLoopback() || ip.To4() == nil {
						continue
					}
					if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
						return ip.String(), true
					}
				}
			}
			if !preferredOnly {
				break
			}
		}
	}
	return "", false
}

func isCellularNetworkInterface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{
		"rmnet", "ccmni", "pdp", "wwan", "cellular", "v4-rmnet", "r_rmnet",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func isPreferredLANInterface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{
		"wlan", "wifi", "ap", "swlan", "eth", "rndis", "usb", "bt-pan",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
