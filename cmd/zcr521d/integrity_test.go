package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyModulePropIntegrity(t *testing.T) {
	moduleDir := t.TempDir()
	path := filepath.Join(moduleDir, "module.prop")
	original := []byte("id=zcr521.android.mcp\nname=ZCR521 AI MCP\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(original)
	expected := hex.EncodeToString(sum[:])
	if err := verifyModulePropIntegrity(moduleDir, expected); err != nil {
		t.Fatalf("valid module.prop rejected: %v", err)
	}
	if err := os.WriteFile(path, append(original, []byte("author=changed\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyModulePropIntegrity(moduleDir, expected); err == nil {
		t.Fatal("modified module.prop accepted")
	}
}

func TestMCPAddressFileContent(t *testing.T) {
	withLAN := mcpAddressFileContent(5322, true, "192.168.1.23")
	if !strings.Contains(withLAN, "http://192.168.1.23:5322/mcp") {
		t.Fatalf("LAN address missing: %q", withLAN)
	}
	withoutLAN := mcpAddressFileContent(5322, true, "")
	if strings.Contains(withoutLAN, "未获取") || strings.Contains(withoutLAN, "局域网地址") {
		t.Fatalf("unavailable LAN placeholder should not be persisted: %q", withoutLAN)
	}
}
