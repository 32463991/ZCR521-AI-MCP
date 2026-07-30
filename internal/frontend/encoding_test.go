package frontend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOwnedSourcesAreValidUTF8WithoutKnownMojibake(t *testing.T) {
	roots := []string{
		".",
		filepath.Join("..", "webui"),
		filepath.Join("..", "..", "cmd", "zcr521-bridge"),
		filepath.Join("..", "..", "scripts", "usb"),
	}
	extensions := map[string]bool{
		".go": true, ".html": true, ".css": true, ".js": true,
		".ps1": true, ".sh": true,
	}
	// Construct these sentinels from code points so the test source cannot
	// trigger its own scan.
	forbidden := []string{
		string(rune(0xfffd)),
		string(rune(0x8928)),
		string(rune(0x7490)),
		string(rune(0x9352)),
		string([]rune{0x7039, 0x590a, 0x5168}),
	}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !extensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !utf8.Valid(data) {
				t.Errorf("%s is not valid UTF-8", path)
			}
			for _, bad := range forbidden {
				if bytes.Contains(data, []byte(bad)) {
					t.Errorf("%s contains mojibake sentinel U+%04X", path, []rune(bad)[0])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
