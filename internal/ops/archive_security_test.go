package ops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTARRejectsEscapingSymlink(t *testing.T) {
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	if err := writer.WriteHeader(&tar.Header{
		Name:     "link",
		Linkname: "../../outside",
		Typeflag: tar.TypeSymlink,
		Mode:     0o777,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, _, err := extractTAR(tar.NewReader(bytes.NewReader(data.Bytes())), destination, false); err == nil ||
		!strings.Contains(err.Error(), "目标目录之外") {
		t.Fatalf("escaping symlink error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "link")); !os.IsNotExist(err) {
		t.Fatalf("escaping symlink was created: %v", err)
	}
}

func TestZIPRejectsExistingSymlinkParent(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "destination")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(destination, "link")); err != nil {
		t.Skipf("host cannot create symlink: %v", err)
	}

	archivePath := filepath.Join(root, "malicious.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("link/escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("escape")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, err := extractZIP(archivePath, destination, true); err == nil ||
		!strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("symlink-parent extraction error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped through symlink parent: %v", err)
	}
}

func TestArchiveSymlinkValidationAllowsOnlyInRootTargets(t *testing.T) {
	root := filepath.Join("root", "extract")
	target := filepath.Join(root, "nested", "link")
	if err := validateArchiveSymlink(root, target, "../real/file"); err != nil {
		t.Fatalf("safe link rejected: %v", err)
	}
	for _, link := range []string{"../../../outside", "/absolute/outside"} {
		if err := validateArchiveSymlink(root, target, link); err == nil {
			t.Fatalf("unsafe link accepted: %q", link)
		}
	}
}

func FuzzSafeArchivePaths(f *testing.F) {
	for _, seed := range []string{
		"file.txt",
		"nested/file.txt",
		"../escape",
		"../../escape",
		"/absolute",
		`C:\absolute`,
		`..\escape`,
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 4096 {
			t.Skip()
		}
		root := filepath.Join("root", "extract")
		target, err := safeArchivePath(root, name)
		if err != nil {
			return
		}
		relative, relErr := filepath.Rel(root, target)
		if relErr != nil ||
			relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("accepted escaping path %q -> %q", name, target)
		}
	})
}
