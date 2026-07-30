package ops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ulikunitz/xz"
	"github.com/zcr521/android-ai-mcp/internal/schema"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	return New(Config{
		WorkDir:      filepath.Join(root, "work"),
		StateDir:     filepath.Join(root, "state"),
		ShellTimeout: 2 * time.Second,
	})
}

func TestAllPublicToolsAreDispatched(t *testing.T) {
	manager := testManager(t)
	if got, want := len(SupportedTools()), 48; got != want {
		t.Fatalf("public tool count = %d, want %d", got, want)
	}
	for _, tool := range SupportedTools() {
		result := manager.Execute(context.Background(), Request{Tool: tool, Args: map[string]any{}})
		if result.Code == "UNKNOWN_TOOL" {
			t.Errorf("%s was not dispatched: %+v", tool, result)
		}
	}
}

func TestPackageNameRejectsPathTraversal(t *testing.T) {
	for _, value := range []string{"..", ".hidden", "com..escape", "com/example", `com\example`, "com.example;id"} {
		if _, err := packageName(map[string]any{"package": value}); err == nil {
			t.Errorf("packageName accepted unsafe value %q", value)
		}
	}
	if got, err := packageName(map[string]any{"package": "com.example.app_1"}); err != nil || got != "com.example.app_1" {
		t.Fatalf("valid package rejected: %q, %v", got, err)
	}
}

func TestSystemlessStagePathIsScopedToModuleSystemTree(t *testing.T) {
	tests := map[string]string{
		"/system/bin/demo":     "/data/adb/modules/zcr521.android.mcp/system/bin/demo",
		"/system_ext/etc/demo": "/data/adb/modules/zcr521.android.mcp/system/system_ext/etc/demo",
		"/product/etc/demo":    "/data/adb/modules/zcr521.android.mcp/system/product/etc/demo",
		"/vendor/etc/demo":     "/data/adb/modules/zcr521.android.mcp/system/vendor/etc/demo",
		"/odm/etc/demo":        "/data/adb/modules/zcr521.android.mcp/system/odm/etc/demo",
	}
	for target, want := range tests {
		got, err := systemlessStagePath(target)
		if err != nil || filepath.ToSlash(got) != want {
			t.Errorf("systemlessStagePath(%q) = %q, %v; want %q", target, got, err, want)
		}
	}
	for _, target := range []string{
		"/", "/system", "/data/local/tmp/demo", "../system/bin/demo", "/system/../data/demo",
	} {
		if got, err := systemlessStagePath(target); err == nil {
			t.Errorf("unsafe target %q mapped to %q", target, got)
		}
	}
}

func TestClearDirectoryContentsPreservesRootAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	if err := os.MkdirAll(filepath.Join(cache, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "nested", "item"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := clearDirectoryContents(cache); err != nil || removed != 1 {
		t.Fatalf("clearDirectoryContents = %d, %v", removed, err)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("cache root was not preserved: %v", err)
	}
	link := filepath.Join(root, "cache-link")
	if err := os.Symlink(cache, link); err == nil {
		if _, err := clearDirectoryContents(link); err == nil {
			t.Fatal("symlink cache root was accepted")
		}
	}
}

func TestEverySchemaActionReachesAnOperation(t *testing.T) {
	manager := testManager(t)
	registry := schema.All("test")
	if got, want := len(registry.Tools), len(SupportedTools()); got != want {
		t.Fatalf("schema tool count = %d, ops tool count = %d", got, want)
	}
	for _, tool := range registry.Tools {
		properties := tool.InputSchema["properties"].(map[string]any)
		actionSchema := properties["action"].(map[string]any)
		actions := actionSchema["enum"].([]any)
		for _, rawAction := range actions {
			action := rawAction.(string)
			t.Run(tool.Name+"/"+action, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
				result := manager.Execute(ctx, Request{Tool: tool.Name, Args: map[string]any{"action": action}})
				if result.Code == "UNKNOWN_TOOL" || result.Strategy == "action_validation" {
					t.Fatalf("schema action is not dispatched: %+v", result)
				}
			})
		}
	}
}

func TestChineseErrorsAreValidUTF8(t *testing.T) {
	manager := testManager(t)
	result := manager.Execute(context.Background(), Request{
		Tool: "zcr521_fs_info",
		Args: map[string]any{"action": "definitely_unknown", "path": "."},
	})
	combined := result.Message + "\n" + result.Error
	if !utf8.ValidString(combined) {
		t.Fatalf("error text is not valid UTF-8: %q", combined)
	}
	for _, broken := range []string{"\uFFFD", "褰撳", "璺", "锛", "鎵ц", "绔"} {
		if strings.Contains(combined, broken) {
			t.Fatalf("error text contains mojibake marker %q: %q", broken, combined)
		}
	}
	if !strings.Contains(result.Message, "不支持") {
		t.Fatalf("expected readable Chinese error, got %q", result.Message)
	}
}

func TestFileReadWriteHashAndSearch(t *testing.T) {
	manager := testManager(t)
	write := manager.Execute(context.Background(), Request{
		Tool: "zcr521_fs_write",
		Args: map[string]any{"action": "write", "path": "nested/hello.txt", "content": "你好 Android"},
	})
	if !write.Success {
		t.Fatalf("write failed: %+v", write)
	}
	read := manager.Execute(context.Background(), Request{
		Tool: "zcr521_fs_read",
		Args: map[string]any{"action": "text", "path": "nested/hello.txt"},
	})
	if !read.Success {
		t.Fatalf("read failed: %+v", read)
	}
	data := read.Data.(map[string]any)
	if data["content"] != "你好 Android" {
		t.Fatalf("unexpected content: %#v", data["content"])
	}
	hash := manager.Execute(context.Background(), Request{
		Tool: "zcr521_fs_hash",
		Args: map[string]any{"action": "sha256", "path": "nested/hello.txt"},
	})
	if !hash.Success {
		t.Fatalf("hash failed: %+v", hash)
	}
	search := manager.Execute(context.Background(), Request{
		Tool: "zcr521_fs_search",
		Args: map[string]any{"action": "search", "path": ".", "extension": ".txt"},
	})
	if !search.Success {
		t.Fatalf("search failed: %+v", search)
	}
}

func TestXZAndTarXZRoundTrip(t *testing.T) {
	manager := testManager(t)
	if err := os.MkdirAll(manager.cfg.WorkDir, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("streaming-xz-内容\n"), 4096)
	if err := os.WriteFile(filepath.Join(manager.cfg.WorkDir, "payload.txt"), payload, 0o640); err != nil {
		t.Fatal(err)
	}
	xzCreate := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "create", "format": "xz", "destination": "payload.txt.xz", "sources": []string{"payload.txt"}},
	})
	if !xzCreate.Success {
		t.Fatalf("xz create failed: %+v", xzCreate)
	}
	xzTest := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "test", "format": "xz", "source": "payload.txt.xz"},
	})
	if !xzTest.Success {
		t.Fatalf("xz test failed: %+v", xzTest)
	}
	xzExtract := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "extract", "format": "xz", "source": "payload.txt.xz", "destination": "xz-out"},
	})
	if !xzExtract.Success {
		t.Fatalf("xz extract failed: %+v", xzExtract)
	}
	got, err := os.ReadFile(filepath.Join(manager.cfg.WorkDir, "xz-out", "payload.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("xz round-trip payload differs")
	}

	if err := os.MkdirAll(filepath.Join(manager.cfg.WorkDir, "tree", "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.cfg.WorkDir, "tree", "child", "a.txt"), []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	tarCreate := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "create", "format": "tar.xz", "destination": "tree.tar.xz", "sources": []string{"tree"}},
	})
	if !tarCreate.Success {
		t.Fatalf("tar.xz create failed: %+v", tarCreate)
	}
	tarExtract := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "extract", "format": "tar.xz", "source": "tree.tar.xz", "destination": "tar-out"},
	})
	if !tarExtract.Success {
		t.Fatalf("tar.xz extract failed: %+v", tarExtract)
	}
	got, err = os.ReadFile(filepath.Join(manager.cfg.WorkDir, "tar-out", "tree", "child", "a.txt"))
	if err != nil || string(got) != "a" {
		t.Fatalf("tar.xz round-trip failed: %v %q", err, got)
	}
}

func TestFind7ZipPrefersConfiguredExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZCR521_7ZZ", executable)
	got, err := find7Zip()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != filepath.Clean(executable) {
		t.Fatalf("find7Zip() = %q, want configured executable %q", got, executable)
	}
}

func TestTarXZRejectsPathTraversal(t *testing.T) {
	manager := testManager(t)
	if err := os.MkdirAll(manager.cfg.WorkDir, 0o750); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(manager.cfg.WorkDir, "evil.tar.xz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	xzWriter, err := xz.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(xzWriter)
	content := []byte("escape")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../../escaped.txt", Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := xzWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "extract", "format": "tar.xz", "source": "evil.tar.xz", "destination": "safe"},
	})
	if result.Success {
		t.Fatalf("malicious tar.xz unexpectedly succeeded: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(manager.cfg.WorkDir, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote outside destination: %v", err)
	}
}

func TestZIPRejectsPathTraversal(t *testing.T) {
	manager := testManager(t)
	if err := os.MkdirAll(manager.cfg.WorkDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manager.cfg.WorkDir, "evil.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../../escaped.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("escape"))
	_ = writer.Close()
	_ = file.Close()
	result := manager.Execute(context.Background(), Request{
		Tool: "zcr521_archive",
		Args: map[string]any{"action": "extract", "source": "evil.zip", "destination": "safe"},
	})
	if result.Success {
		t.Fatalf("malicious zip unexpectedly succeeded: %+v", result)
	}
}

func TestChunkUploadResumeAndComplete(t *testing.T) {
	manager := testManager(t)
	payload := []byte("chunked upload payload")
	first := payload[:8]
	second := payload[8:]
	for _, part := range []struct {
		offset int64
		chunk  []byte
	}{{offset: 0, chunk: first}, {offset: int64(len(first)), chunk: second}} {
		result := manager.Execute(context.Background(), Request{
			Tool: "zcr521_transfer_upload",
			Args: map[string]any{
				"action": "chunk", "path": "uploads/result.bin", "offset": part.offset,
				"content": base64.StdEncoding.EncodeToString(part.chunk),
			},
		})
		if !result.Success {
			t.Fatalf("chunk failed: %+v", result)
		}
	}
	complete := manager.Execute(context.Background(), Request{
		Tool: "zcr521_transfer_upload",
		Args: map[string]any{"action": "complete", "path": "uploads/result.bin", "expectedSize": len(payload)},
	})
	if !complete.Success {
		t.Fatalf("complete failed: %+v", complete)
	}
	got, err := os.ReadFile(filepath.Join(manager.cfg.WorkDir, "uploads", "result.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("uploaded payload differs: %v", err)
	}
}

func TestHTTPDownloadAndChecksum(t *testing.T) {
	payload := bytes.Repeat([]byte("download"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Length", "8192")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	manager := testManager(t)
	result := manager.Execute(context.Background(), Request{
		Tool: "zcr521_download",
		Args: map[string]any{"url": server.URL + "/payload", "path": "downloads/payload.bin"},
	})
	if !result.Success {
		t.Fatalf("download failed: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(manager.cfg.WorkDir, "downloads", "payload.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded payload differs: %v", err)
	}
}

func TestShellHonorsCurrentIdentityOnHost(t *testing.T) {
	manager := testManager(t)
	command := "printf host-shell"
	if runtime.GOOS == "windows" {
		command = "echo host-shell"
	}
	result := manager.Execute(context.Background(), Request{
		Tool: "zcr521_shell",
		Args: map[string]any{"action": "execute", "command": command, "identity": "current"},
	})
	if !result.Success || !strings.Contains(result.Stdout, "host-shell") {
		t.Fatalf("host shell failed: %+v", result)
	}
}
