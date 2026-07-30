package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
)

func TestStopProcessTerminatesLiveChild(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestSupervisorHelperProcess", "--")
	command.Env = append(os.Environ(), "GO_WANT_SUPERVISOR_HELPER=1")
	configureChild(command, 0, 0, false)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	stopProcess(command.Process, 2*time.Second)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("stopProcess left the child alive")
	}
}

func TestSupervisorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SUPERVISOR_HELPER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestDefaultPolicyMatchesReleaseContract(t *testing.T) {
	options := DefaultOptions()
	if options.InitialDelay != time.Second ||
		options.MaxDelay != time.Minute ||
		options.FailureWindow != 10*time.Minute ||
		options.MaxFailures != 5 ||
		options.StableAfter != 5*time.Minute {
		t.Fatalf("unexpected defaults: %#v", options)
	}
}

func TestCrashLoopStopsAfterFiveFailuresAndPersistsReason(t *testing.T) {
	stateDir := t.TempDir()
	options := DefaultOptions()
	options.StateDir = stateDir
	options.Executable = filepath.Join(stateDir, "missing-zcr521d")
	options.StableAfter = time.Hour
	options.FailureWindow = time.Minute
	options.MaxFailures = 5
	options.InitialDelay = time.Millisecond
	options.MaxDelay = 2 * time.Millisecond
	options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	err := Run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "连续失败 5 次") {
		t.Fatalf("Run error = %v", err)
	}
	for _, name := range []string{"supervisor-stopped.json", "crash-loop.blocked"} {
		path := filepath.Join(stateDir, name)
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		var report struct {
			Failures int    `json:"failures"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal(raw, &report) != nil ||
			report.Failures != 5 ||
			report.Reason != "start broker" {
			t.Fatalf("%s = %s", name, raw)
		}
	}
}

func TestMarkStablePromotesCandidateAtomically(t *testing.T) {
	stateDir := t.TempDir()
	versionDir := filepath.Join(stateDir, "versions")
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(versionDir, "state.json")
	initial := VersionState{
		CandidateVersion: "0.01",
		CandidateBinary:  filepath.Join(versionDir, "candidate"),
		PreviousVersion:  "0.9.0",
		PreviousBinary:   filepath.Join(versionDir, "previous"),
		CandidateStarted: time.Now().UTC().Add(-5 * time.Minute),
	}
	writeVersionStateForTest(t, path, initial)
	executable := filepath.Join(versionDir, "active")
	if err := markStable(Options{StateDir: stateDir, Executable: executable}); err != nil {
		t.Fatal(err)
	}
	got, err := readVersionState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveVersion != buildinfo.Version ||
		got.ActiveBinary != executable ||
		got.CandidateVersion != "" ||
		got.CandidateBinary != "" ||
		got.GoodSince.IsZero() {
		t.Fatalf("stable state = %#v", got)
	}
}

func TestRollbackRejectsBinaryOutsideVersionDirectory(t *testing.T) {
	stateDir := t.TempDir()
	versionDir := filepath.Join(stateDir, "versions")
	if err := os.MkdirAll(versionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(stateDir, "outside-binary")
	if err := os.WriteFile(outside, []byte("previous"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeVersionStateForTest(t, filepath.Join(versionDir, "state.json"), VersionState{
		CandidateVersion: "0.01",
		PreviousVersion:  "0.9.0",
		PreviousBinary:   outside,
	})
	err := rollback(Options{
		StateDir:   stateDir,
		Executable: filepath.Join(versionDir, "active"),
	}, "test")
	if err == nil || !strings.Contains(err.Error(), "状态目录之外") {
		t.Fatalf("rollback error = %v", err)
	}
}

func TestRestorePreviousBinaryUsesCompleteCopy(t *testing.T) {
	root := t.TempDir()
	previous := filepath.Join(root, "previous")
	active := filepath.Join(root, "active")
	content := []byte("complete previous binary")
	if err := os.WriteFile(previous, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restorePreviousBinary(previous, active); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("active content = %q", got)
	}
}

func TestRollbackPendingCandidateWithoutStateIsNoop(t *testing.T) {
	err := rollbackPendingCandidate(Options{StateDir: t.TempDir()}, "test")
	if !errors.Is(err, errNoPendingCandidate) {
		t.Fatalf("error = %v", err)
	}
}

func writeVersionStateForTest(t *testing.T, path string, state VersionState) {
	t.Helper()
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
