// Package atomicfile writes durable snapshots without exposing partial JSON.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

func Write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary snapshot: %w", err)
	}
	tmp := f.Name()
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temporary snapshot: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write temporary snapshot: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync temporary snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temporary snapshot: %w", err)
	}
	if err := replace(tmp, path); err != nil {
		return fmt.Errorf("replace snapshot: %w", err)
	}
	keep = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
