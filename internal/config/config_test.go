package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesAndRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, report, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Created || cfg.Network.Port != 5322 {
		t.Fatalf("unexpected first load: %#v %#v", cfg, report)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, report, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Recovered || report.CorruptBackup == "" {
		t.Fatalf("expected recovery: %#v", report)
	}
	if _, err := os.Stat(report.CorruptBackup); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectUnsafeCORS(t *testing.T) {
	cfg := Default()
	cfg.Security.AllowCORS = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadMergesNewFieldsWithoutDiscardingExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{
	  "schemaVersion": 1,
	  "network": {"port": 5444, "listenLoopback": true, "listenLan": true, "legacySse": true},
	  "paths": {"stateDir": "` + filepath.ToSlash(dir) + `", "workDir": "/storage/emulated/0/custom-zcr521"}
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, report, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Recovered {
		t.Fatalf("compatible older config was treated as corrupt: %#v", report)
	}
	if cfg.Network.Port != 5444 || cfg.Limits.TotalTasks != 8 || !cfg.Security.OnLinkOnly {
		t.Fatalf("existing and added fields were not merged: %#v", cfg)
	}
	if got := filepath.ToSlash(cfg.Paths.DownloadsDir); got != "/storage/emulated/0/custom-zcr521/downloads" {
		t.Fatalf("downloadsDir = %q", got)
	}
}

func TestEnsureWorkDirectoriesCreatesOnlyWorkspaceRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "zcr521AI")
	cfg := Default()
	cfg.Paths.WorkDir = root
	derivePaths(&cfg)
	if err := EnsureWorkDirectories(cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace contains default children: %v", entries)
	}
}
