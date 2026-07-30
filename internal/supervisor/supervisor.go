// Package supervisor keeps the root broker and unprivileged HTTP frontend
// alive as a crash-isolated pair.
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/atomicfile"
	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
)

type Options struct {
	Executable    string
	StateDir      string
	FrontendUID   int
	FrontendGID   int
	StableAfter   time.Duration
	FailureWindow time.Duration
	MaxFailures   int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	Logger        *slog.Logger
}

type VersionState struct {
	ActiveVersion    string    `json:"activeVersion"`
	ActiveBinary     string    `json:"activeBinary"`
	CandidateVersion string    `json:"candidateVersion"`
	CandidateBinary  string    `json:"candidateBinary"`
	PreviousVersion  string    `json:"previousVersion"`
	PreviousBinary   string    `json:"previousBinary"`
	CandidateStarted time.Time `json:"candidateStarted"`
	GoodSince        time.Time `json:"goodSince"`
	RollbackAt       time.Time `json:"rollbackAt"`
	RollbackReason   string    `json:"rollbackReason"`
}

var errNoPendingCandidate = errors.New("没有待验证且可回滚的候选版本")

func DefaultOptions() Options {
	return Options{
		StateDir:      buildinfo.DefaultStateDir,
		FrontendUID:   2000,
		FrontendGID:   2000,
		StableAfter:   5 * time.Minute,
		FailureWindow: 10 * time.Minute,
		MaxFailures:   5,
		InitialDelay:  time.Second,
		MaxDelay:      time.Minute,
	}
}

func Run(ctx context.Context, options Options) error {
	if options.Executable == "" {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve supervisor executable: %w", err)
		}
		options.Executable = executable
	}
	applyDefaults(&options)
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	runDir := filepath.Join(options.StateDir, "run")
	logDir := filepath.Join(options.StateDir, "logs")
	// The shell frontend needs traverse-only access to reach run/, while all
	// sensitive children (config, tasks, logs, versions) retain their own 0700
	// or 0600 permissions.
	_ = os.Chown(options.StateDir, 0, options.FrontendGID)
	_ = os.Chmod(options.StateDir, 0o710)
	if err := os.MkdirAll(runDir, 0o710); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	_ = os.Chown(runDir, 0, options.FrontendGID)
	_ = os.Chmod(runDir, 0o710)

	var failures []time.Time
	delay := options.InitialDelay
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		started := time.Now()
		reason, err := runPair(ctx, options, runDir, logDir, logger)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = errors.New(reason)
		}
		now := time.Now()
		failures = append(failures, now)
		cutoff := now.Add(-options.FailureWindow)
		firstRecent := 0
		for firstRecent < len(failures) && failures[firstRecent].Before(cutoff) {
			firstRecent++
		}
		failures = failures[firstRecent:]
		logger.Error("子进程异常退出",
			"reason", reason,
			"error", err,
			"failuresInWindow", len(failures),
			"uptime", time.Since(started).String(),
		)
		rollbackErr := rollbackPendingCandidate(options, reason+": "+err.Error())
		if rollbackErr != nil && !errors.Is(rollbackErr, errNoPendingCandidate) {
			logger.Error("候选版本自动回滚失败", "error", rollbackErr)
		}
		if len(failures) >= options.MaxFailures {
			writeStopReason(options.StateDir, reason, err, len(failures), rollbackErr)
			return fmt.Errorf("10 分钟内连续失败 %d 次，停止重启: %w", len(failures), err)
		}
		waitDelay := delay
		if time.Since(started) >= options.StableAfter {
			failures = nil
			delay = options.InitialDelay
			waitDelay = options.InitialDelay
		} else {
			delay = min(delay*2, options.MaxDelay)
		}
		timer := time.NewTimer(waitDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func runPair(ctx context.Context, options Options, runDir, logDir string, logger *slog.Logger) (string, error) {
	socket := filepath.Join(runDir, buildinfo.DefaultSocketName)
	pidFile := filepath.Join(runDir, "frontend.pid")
	_ = os.Remove(pidFile)

	brokerLog, err := openLog(filepath.Join(logDir, "broker.log"))
	if err != nil {
		return "open broker log", err
	}
	defer brokerLog.Close()
	frontendLog, err := openLog(filepath.Join(logDir, "frontend.log"))
	if err != nil {
		return "open frontend log", err
	}
	defer frontendLog.Close()

	pairCtx, cancelPair := context.WithCancel(ctx)
	defer cancelPair()
	broker := exec.Command(options.Executable,
		"broker",
		"--state-dir", options.StateDir,
		"--socket", socket,
		"--frontend-pid-file", pidFile,
	)
	broker.Stdout = brokerLog
	broker.Stderr = brokerLog
	configureChild(broker, 0, 0, false)
	if err := broker.Start(); err != nil {
		return "start broker", err
	}
	logger.Info("Root broker 已启动", "pid", broker.Process.Pid)

	if err := waitForSocket(pairCtx, socket, broker, 10*time.Second); err != nil {
		stopProcess(broker.Process, 5*time.Second)
		_ = broker.Wait()
		return "broker socket not ready", err
	}
	frontendConfig := filepath.Join(runDir, "frontend-config.json")
	configData, err := os.ReadFile(filepath.Join(options.StateDir, "config.json"))
	if err != nil {
		stopProcess(broker.Process, 5*time.Second)
		_ = broker.Wait()
		return "read frontend config", err
	}
	if err := atomicfile.Write(frontendConfig, configData, 0o640); err != nil {
		stopProcess(broker.Process, 5*time.Second)
		_ = broker.Wait()
		return "write frontend config", err
	}
	_ = os.Chown(frontendConfig, 0, options.FrontendGID)
	transferDir := filepath.Join(runDir, "transfer")
	if err := os.MkdirAll(transferDir, 0o700); err != nil {
		stopProcess(broker.Process, 5*time.Second)
		_ = broker.Wait()
		return "create frontend transfer directory", err
	}
	_ = os.Chown(transferDir, options.FrontendUID, options.FrontendGID)
	_ = os.Chmod(transferDir, 0o700)

	frontend := exec.Command(options.Executable,
		"serve",
		"--state-dir", options.StateDir,
		"--config", frontendConfig,
		"--socket", socket,
	)
	frontend.Stdout = frontendLog
	frontend.Stderr = frontendLog
	configureChild(frontend, options.FrontendUID, options.FrontendGID, true)
	degraded := false
	if err := frontend.Start(); err != nil {
		// Some ROMs disallow setuid from the module domain. Keep the service
		// available, but record and expose the security degradation.
		degraded = true
		frontend = exec.Command(options.Executable,
			"serve",
			"--state-dir", options.StateDir,
			"--config", frontendConfig,
			"--socket", socket,
			"--security-degraded", "frontend_setuid_failed",
		)
		frontend.Stdout = frontendLog
		frontend.Stderr = frontendLog
		configureChild(frontend, 0, 0, false)
		if fallbackErr := frontend.Start(); fallbackErr != nil {
			stopProcess(broker.Process, 5*time.Second)
			_ = broker.Wait()
			return "start frontend", fmt.Errorf("uid 2000: %v; root fallback: %w", err, fallbackErr)
		}
	}
	if err := atomicfile.Write(pidFile, []byte(strconv.Itoa(frontend.Process.Pid)+"\n"), 0o640); err != nil {
		stopProcess(frontend.Process, 5*time.Second)
		stopProcess(broker.Process, 5*time.Second)
		_, _ = frontend.Process.Wait()
		_, _ = broker.Process.Wait()
		return "write frontend pid", err
	}
	_ = os.Chown(pidFile, 0, options.FrontendGID)
	logger.Info("MCP frontend 已启动", "pid", frontend.Process.Pid, "securityDegraded", degraded)

	type exit struct {
		name string
		err  error
	}
	exits := make(chan exit, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		exits <- exit{"broker", broker.Wait()}
	}()
	go func() {
		defer waitGroup.Done()
		exits <- exit{"frontend", frontend.Wait()}
	}()

	stableTimer := time.NewTimer(options.StableAfter)
	defer stableTimer.Stop()
	stableMarked := false
	for {
		select {
		case <-ctx.Done():
			cancelPair()
			stopProcess(frontend.Process, 8*time.Second)
			stopProcess(broker.Process, 8*time.Second)
			waitGroup.Wait()
			_ = os.Remove(pidFile)
			return "supervisor stopped", nil
		case ended := <-exits:
			cancelPair()
			if ended.name == "broker" {
				stopProcess(frontend.Process, 8*time.Second)
			} else {
				stopProcess(broker.Process, 8*time.Second)
			}
			waitGroup.Wait()
			_ = os.Remove(pidFile)
			return ended.name + " exited", ended.err
		case <-stableTimer.C:
			if !stableMarked {
				if err := markStable(options); err != nil {
					logger.Error("无法标记稳定版本", "error", err)
				} else {
					logger.Info("候选版本已稳定运行并标记为可用", "stableAfter", options.StableAfter.String())
				}
				stableMarked = true
			}
		case <-pairCtx.Done():
			return "process pair cancelled", pairCtx.Err()
		}
	}
}

func applyDefaults(options *Options) {
	defaults := DefaultOptions()
	if options.StateDir == "" {
		options.StateDir = defaults.StateDir
	}
	if options.FrontendUID == 0 {
		options.FrontendUID = defaults.FrontendUID
	}
	if options.FrontendGID == 0 {
		options.FrontendGID = defaults.FrontendGID
	}
	if options.StableAfter <= 0 {
		options.StableAfter = defaults.StableAfter
	}
	if options.FailureWindow <= 0 {
		options.FailureWindow = defaults.FailureWindow
	}
	if options.MaxFailures <= 0 {
		options.MaxFailures = defaults.MaxFailures
	}
	if options.InitialDelay <= 0 {
		options.InitialDelay = defaults.InitialDelay
	}
	if options.MaxDelay <= 0 {
		options.MaxDelay = defaults.MaxDelay
	}
}

func waitForSocket(ctx context.Context, path string, broker *exec.Cmd, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("timed out waiting for broker socket")
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return nil
			}
			if broker.ProcessState != nil && broker.ProcessState.Exited() {
				return errors.New("broker exited before socket became ready")
			}
		}
	}
}

func openLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func stopProcess(process *os.Process, grace time.Duration) {
	if process == nil {
		return
	}
	_ = signalProcessGroup(process, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			if !processAlive(process) {
				close(done)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = signalProcessGroup(process, syscall.SIGKILL)
	}
}

func markStable(options Options) error {
	path := filepath.Join(options.StateDir, "versions", "state.json")
	state, _ := readVersionState(path)
	state.ActiveVersion = buildinfo.Version
	state.ActiveBinary = options.Executable
	state.GoodSince = time.Now().UTC()
	state.CandidateVersion = ""
	state.CandidateBinary = ""
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

func rollback(options Options, reason string) error {
	path := filepath.Join(options.StateDir, "versions", "state.json")
	state, err := readVersionState(path)
	if err != nil {
		return err
	}
	if state.PreviousBinary == "" || state.PreviousBinary == options.Executable {
		return errors.New("没有可回滚的升级前二进制")
	}
	info, err := os.Stat(state.PreviousBinary)
	if err != nil {
		return fmt.Errorf("previous binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("升级前二进制无效")
	}
	versionRoot := filepath.Join(options.StateDir, "versions")
	relative, err := filepath.Rel(versionRoot, state.PreviousBinary)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return errors.New("拒绝状态目录之外的回滚二进制")
	}
	if err := restorePreviousBinary(state.PreviousBinary, options.Executable); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	state.RollbackAt = time.Now().UTC()
	state.RollbackReason = reason
	state.ActiveVersion = state.PreviousVersion
	state.ActiveBinary = options.Executable
	state.CandidateVersion = ""
	state.CandidateBinary = ""
	state.GoodSince = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return replaceCurrentProcess(options.Executable, os.Args)
}

func rollbackPendingCandidate(options Options, reason string) error {
	path := filepath.Join(options.StateDir, "versions", "state.json")
	state, err := readVersionState(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errNoPendingCandidate
		}
		return err
	}
	if state.CandidateVersion == "" || state.PreviousBinary == "" || !state.GoodSince.IsZero() {
		return errNoPendingCandidate
	}
	return rollback(options, reason)
}

func restorePreviousBinary(previous, active string) error {
	source, err := os.Open(previous)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.CreateTemp(filepath.Dir(active), ".zcr521d-rollback-*")
	if err != nil {
		return err
	}
	temp := destination.Name()
	keep := false
	defer func() {
		_ = destination.Close()
		if !keep {
			_ = os.Remove(temp)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	if err := destination.Chmod(0o755); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, active); err != nil {
		return err
	}
	keep = true
	return nil
}

func readVersionState(path string) (VersionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VersionState{}, err
	}
	var state VersionState
	if err := json.Unmarshal(data, &state); err != nil {
		return VersionState{}, err
	}
	return state, nil
}

func writeStopReason(stateDir, reason string, runErr error, failures int, rollbackErr error) {
	rollbackMessage := ""
	if rollbackErr != nil {
		rollbackMessage = rollbackErr.Error()
	}
	data, _ := json.MarshalIndent(map[string]any{
		"time":          time.Now().UTC(),
		"reason":        reason,
		"error":         runErr.Error(),
		"failures":      failures,
		"rollbackError": rollbackMessage,
	}, "", "  ")
	data = append(data, '\n')
	_ = atomicfile.Write(filepath.Join(stateDir, "supervisor-stopped.json"), data, 0o600)
	_ = atomicfile.Write(filepath.Join(stateDir, "crash-loop.blocked"), data, 0o600)
}

func copyStream(destination io.Writer, source io.Reader) {
	_, _ = io.Copy(destination, source)
}
