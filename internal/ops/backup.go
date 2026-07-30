package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) backupOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	backupRoot := filepath.Join(m.cfg.WorkDir, "backups")
	switch action {
	case "list":
		if err := os.MkdirAll(backupRoot, 0o750); err != nil {
			return fileFailure("备份目录不可用", err, "backup_store")
		}
		entries := make([]fileEntry, 0)
		walkErr := filepath.WalkDir(backupRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == backupRoot {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr == nil {
				entries = append(entries, describeFile(path, info))
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		})
		if walkErr != nil {
			return fileFailure("备份列表读取失败", walkErr, "backup_store")
		}
		return ok("备份列表读取成功", entries, "backup_store")
	case "create", "file", "directory", "app_data", "modules", "config", "schedules":
		sources := []string{}
		label := action
		switch action {
		case "create", "file", "directory":
			values, listErr := argStringSlice(req.Args, "sources", "paths", "path")
			if listErr != nil {
				return invalid(listErr.Error())
			}
			for _, value := range values {
				path, resolveErr := m.resolvePath(value)
				if resolveErr != nil {
					return invalid(resolveErr.Error())
				}
				sources = append(sources, path)
			}
		case "app_data":
			if result := requireAndroidRoot(); result != nil {
				return *result
			}
			pkg, packageErr := packageName(req.Args)
			if packageErr != nil {
				return invalid(packageErr.Error())
			}
			user, userErr := androidUser(req.Args)
			if userErr != nil || user == "all" || user == "current" {
				return invalid("app_data 备份必须明确提供数字 userId")
			}
			path := filepath.Join("/data/user", user, pkg)
			if _, statErr := os.Stat(path); statErr != nil {
				return fileFailure("应用数据目录不存在", statErr, "app_data_tar")
			}
			sources = []string{path}
			label = "app-" + pkg + "-u" + user
		case "modules":
			if result := requireAndroidRoot(); result != nil {
				return *result
			}
			sources = []string{"/data/adb/modules"}
		case "config":
			sources = []string{m.cfg.StateDir}
		case "schedules":
			sources = []string{filepath.Join(m.cfg.StateDir, "schedules")}
		}
		for _, source := range sources {
			if _, statErr := os.Lstat(source); statErr != nil {
				return fileFailure("备份源不存在", statErr, "tar_gzip")
			}
		}
		defaultOutput := filepath.Join(backupRoot, sanitizeBackupLabel(label)+"-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
		outputValue, outputErr := argOptionalString(req.Args, defaultOutput, "destination", "output")
		if outputErr != nil {
			return invalid(outputErr.Error())
		}
		output, outputErr := m.resolvePath(outputValue)
		if outputErr != nil {
			return invalid(outputErr.Error())
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
			return fileFailure("备份目标目录创建失败", err, "tar_gzip")
		}
		if err := createTAR(output, sources, true); err != nil {
			return archiveFailure("备份压缩失败", err, "tar.gz")
		}
		digest, hashErr := hashFileSHA256(output)
		if hashErr != nil {
			return fileFailure("备份校验失败", hashErr, "sha256")
		}
		info, statErr := os.Stat(output)
		if statErr != nil {
			return fileFailure("备份产物读回失败", statErr, "backup_readback")
		}
		result := ok("备份完成并已计算 SHA-256", map[string]any{"path": output, "bytes": info.Size(), "sha256": digest, "sources": sources}, "tar_gzip_sha256")
		result.Artifacts = []string{output}
		return result
	case "verify":
		pathValue, parseErr := argString(req.Args, "path")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		digest, hashErr := hashFileSHA256(path)
		if hashErr != nil {
			return fileFailure("备份哈希校验失败", hashErr, "sha256")
		}
		expected, _ := argOptionalString(req.Args, "", "sha256")
		if expected != "" && !strings.EqualFold(expected, digest) {
			return fail("CHECKSUM_MISMATCH", "备份 SHA-256 不匹配", fmt.Errorf("expected %s, got %s", expected, digest), "sha256")
		}
		entries, listErr := m.listArchive(ctx, inferArchiveFormat(path), path)
		if listErr != nil {
			return archiveFailure("备份压缩结构校验失败", listErr, inferArchiveFormat(path))
		}
		return ok("备份校验成功", map[string]any{"path": path, "sha256": digest, "entries": len(entries)}, "sha256_archive_list")
	case "restore":
		pathValue, parseErr := argString(req.Args, "path", "backup")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		destinationValue, parseErr := argString(req.Args, "destination")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		destination, parseErr := m.resolvePath(destinationValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		confirm, _ := argBool(req.Args, "confirmDangerous", false)
		if dangerousDeletion(destination) && !confirm {
			return fail("DANGEROUS_OPERATION", "恢复到关键系统顶层目录需要 confirmDangerous=true", errors.New(destination), "restore_guard")
		}
		overwrite, _ := argBool(req.Args, "overwrite", false)
		count, bytes, extractErr := m.extractArchive(ctx, inferArchiveFormat(path), path, destination, overwrite)
		if extractErr != nil {
			return archiveFailure("备份恢复失败", extractErr, inferArchiveFormat(path))
		}
		return ok("备份恢复成功", map[string]any{"backup": path, "destination": destination, "entries": count, "bytes": bytes}, "archive_restore")
	case "delete":
		pathValue, parseErr := argString(req.Args, "path")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		relative, relativeErr := filepath.Rel(backupRoot, path)
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fail("DANGEROUS_OPERATION", "zcr521_backup.delete 仅允许删除默认备份目录内的文件", errors.New(path), "backup_delete_guard")
		}
		if err := os.Remove(path); err != nil {
			return fileFailure("备份删除失败", err, "backup_delete")
		}
		return ok("备份已删除", map[string]string{"path": path}, "backup_delete")
	default:
		return invalidAction(req.Tool, action, "app_data", "config", "create", "delete", "directory", "file", "list", "modules", "restore", "schedules", "verify")
	}
}

func sanitizeBackupLabel(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 {
		return "backup"
	}
	return builder.String()
}
