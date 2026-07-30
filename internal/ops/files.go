package ops

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type fileEntry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	ModeOctal  string    `json:"modeOctal"`
	ModifiedAt time.Time `json:"modifiedAt"`
	IsDir      bool      `json:"isDir"`
	IsSymlink  bool      `json:"isSymlink"`
	UID        int       `json:"uid,omitempty"`
	GID        int       `json:"gid,omitempty"`
	LinkTarget string    `json:"linkTarget,omitempty"`
}

func describeFile(path string, info os.FileInfo) fileEntry {
	uid, gid := platformOwnership(info)
	entry := fileEntry{
		Path:       path,
		Name:       info.Name(),
		Size:       info.Size(),
		Mode:       info.Mode().String(),
		ModeOctal:  fmt.Sprintf("%#o", info.Mode().Perm()),
		ModifiedAt: info.ModTime().UTC(),
		IsDir:      info.IsDir(),
		IsSymlink:  info.Mode()&os.ModeSymlink != 0,
		UID:        uid,
		GID:        gid,
	}
	if entry.IsSymlink {
		entry.LinkTarget, _ = os.Readlink(path)
	}
	return entry
}

func (m *Manager) fsInfoOperation(req Request) Result {
	action, err := actionOf(req, "stat")
	if err != nil {
		return invalid(err.Error())
	}
	pathValue, pathErr := argOptionalString(req.Args, ".", "path")
	if pathErr != nil {
		return invalid(pathErr.Error())
	}
	path, pathErr := m.resolvePath(pathValue)
	if pathErr != nil {
		return invalid(pathErr.Error())
	}
	switch action {
	case "exists":
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return ok("路径不存在", map[string]any{"path": path, "exists": false}, "os_lstat")
			}
			return fileFailure("读取路径失败", statErr, "os_lstat")
		}
		return ok("路径存在", map[string]any{"path": path, "exists": true, "entry": describeFile(path, info)}, "os_lstat")
	case "stat":
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fileFailure("读取文件属性失败", statErr, "os_lstat")
		}
		return ok("文件属性读取成功", describeFile(path, info), "os_lstat")
	case "list":
		items, readErr := os.ReadDir(path)
		if readErr != nil {
			return fileFailure("目录读取失败", readErr, "os_readdir")
		}
		entries := make([]fileEntry, 0, len(items))
		for _, item := range items {
			info, infoErr := item.Info()
			if infoErr != nil {
				continue
			}
			entries = append(entries, describeFile(filepath.Join(path, item.Name()), info))
		}
		return ok("目录读取成功", entries, "os_readdir")
	case "size":
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fileFailure("路径大小读取失败", statErr, "os_lstat")
		}
		if !info.IsDir() {
			return ok("文件大小读取成功", map[string]any{"path": path, "bytes": info.Size()}, "os_lstat")
		}
		size, count, walkErr := directorySize(path)
		if walkErr != nil {
			return fileFailure("目录大小计算失败", walkErr, "walkdir")
		}
		return ok("目录大小计算成功", map[string]any{"path": path, "bytes": size, "entries": count}, "walkdir")
	case "disk":
		usage, usageErr := platformDiskUsage(path)
		if usageErr != nil {
			return fileFailure("磁盘空间读取失败", usageErr, "statfs")
		}
		return ok("磁盘空间读取成功", usage, "statfs")
	case "mounts":
		raw, readErr := os.ReadFile("/proc/mounts")
		if readErr != nil {
			return fileFailure("挂载表读取失败", readErr, "proc_mounts")
		}
		mounts := make([]map[string]string, 0)
		scanner := bufio.NewScanner(strings.NewReader(string(raw)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 4 {
				mounts = append(mounts, map[string]string{"source": fields[0], "target": fields[1], "filesystem": fields[2], "options": fields[3]})
			}
		}
		return ok("挂载表读取成功", mounts, "proc_mounts")
	case "selinux_context":
		context, contextErr := readSELinuxContext(path)
		if contextErr != nil {
			return fail("COMMAND_UNAVAILABLE", "无法读取 SELinux Context", contextErr, "ls_Z")
		}
		return ok("SELinux Context 读取成功", map[string]string{"path": path, "context": context}, "ls_Z")
	default:
		return invalidAction(req.Tool, action, "disk", "exists", "list", "mounts", "selinux_context", "size", "stat")
	}
}

func (m *Manager) fsReadOperation(req Request) Result {
	action, err := actionOf(req, "text")
	if err != nil {
		return invalid(err.Error())
	}
	pathValue, err := argString(req.Args, "path")
	if err != nil {
		return invalid(err.Error())
	}
	path, err := m.resolvePath(pathValue)
	if err != nil {
		return invalid(err.Error())
	}
	offset, err := argInt64(req.Args, 0, "offset")
	if err != nil || offset < 0 {
		return invalid("offset 必须是非负整数")
	}
	defaultLimit := int64(1024 * 1024)
	limit, err := argInt64(req.Args, defaultLimit, "length", "limit")
	if err != nil || limit <= 0 || limit > 16*1024*1024 {
		return invalid("length 必须在 1 到 16777216 之间")
	}
	switch action {
	case "text", "binary", "range", "lines", "tail", "resource":
	default:
		return invalidAction(req.Tool, action, "binary", "lines", "range", "resource", "tail", "text")
	}
	file, openErr := os.Open(path)
	if openErr != nil {
		return fileFailure("文件打开失败", openErr, "stream_read")
	}
	defer file.Close()
	info, statErr := file.Stat()
	if statErr != nil {
		return fileFailure("文件属性读取失败", statErr, "stream_read")
	}
	if info.IsDir() {
		return fail("INVALID_ARGUMENT", "不能把目录作为文件读取", errors.New("path is a directory"), "stream_read")
	}
	if action == "resource" {
		result := ok("文件资源已准备，传输层应流式读取该路径", map[string]any{"path": path, "bytes": info.Size(), "stream": true}, "stream_resource")
		result.Artifacts = []string{path}
		return result
	}
	if action == "lines" {
		startLine, lineErr := argInt64(req.Args, 1, "startLine", "line")
		lineCount, countErr := argInt64(req.Args, 100, "lineCount", "count")
		if lineErr != nil || countErr != nil || startLine < 1 || lineCount < 1 || lineCount > 100000 {
			return invalid("startLine 必须大于 0，lineCount 必须在 1 到 100000 之间")
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		lines := make([]string, 0, lineCount)
		var current int64
		for scanner.Scan() {
			current++
			if current < startLine {
				continue
			}
			lines = append(lines, scanner.Text())
			if int64(len(lines)) >= lineCount {
				break
			}
		}
		if scanner.Err() != nil {
			return fileFailure("按行读取失败", scanner.Err(), "scanner_lines")
		}
		return ok("按行读取成功", map[string]any{"path": path, "startLine": startLine, "lines": lines, "eof": int64(len(lines)) < lineCount}, "scanner_lines")
	}
	if action == "tail" {
		tailBytes, tailErr := argInt64(req.Args, 64*1024, "bytes", "length")
		if tailErr != nil || tailBytes < 1 || tailBytes > 16*1024*1024 {
			return invalid("tail bytes 必须在 1 到 16777216 之间")
		}
		offset = info.Size() - tailBytes
		if offset < 0 {
			offset = 0
		}
		limit = info.Size() - offset
	}
	if offset > info.Size() {
		return fail("RANGE_NOT_SATISFIABLE", "offset 超出文件长度", io.EOF, "stream_read")
	}
	if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
		return fileFailure("文件定位失败", seekErr, "stream_read")
	}
	buffer := make([]byte, limit)
	read, readErr := io.ReadFull(file, buffer)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return fileFailure("文件读取失败", readErr, "stream_read")
	}
	buffer = buffer[:read]
	data := map[string]any{
		"path":       path,
		"offset":     offset,
		"bytesRead":  read,
		"totalBytes": info.Size(),
		"eof":        offset+int64(read) >= info.Size(),
	}
	if action == "binary" {
		data["encoding"] = "base64"
		data["content"] = base64.StdEncoding.EncodeToString(buffer)
	} else {
		data["encoding"] = "utf-8"
		data["content"] = string(buffer)
	}
	return ok("文件分段读取成功", data, "stream_read")
}

func (m *Manager) fsWriteOperation(req Request) Result {
	action, err := actionOf(req, "write")
	if err != nil {
		return invalid(err.Error())
	}
	pathValue, err := argString(req.Args, "path")
	if err != nil {
		return invalid(err.Error())
	}
	path, err := m.resolvePath(pathValue)
	if err != nil {
		return invalid(err.Error())
	}
	modeValue, err := argOptionalString(req.Args, "0644", "mode")
	if err != nil {
		return invalid(err.Error())
	}
	mode, err := parseFileMode(modeValue)
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "mkdir":
		parents, boolErr := argBool(req.Args, "parents", true)
		if boolErr != nil {
			return invalid(boolErr.Error())
		}
		if parents {
			err = os.MkdirAll(path, mode)
		} else {
			err = os.Mkdir(path, mode)
		}
		if err != nil {
			return fileFailure("目录创建失败", err, "os_mkdir")
		}
		return ok("目录创建成功", map[string]string{"path": path}, "os_mkdir")
	case "touch":
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, mode)
		if createErr != nil {
			return fileFailure("文件创建失败", createErr, "os_openfile")
		}
		_ = file.Close()
		now := time.Now()
		if chtimesErr := os.Chtimes(path, now, now); chtimesErr != nil {
			return fileFailure("文件时间更新失败", chtimesErr, "os_chtimes")
		}
		return ok("文件已创建或更新时间", map[string]string{"path": path}, "os_openfile")
	case "truncate":
		size, sizeErr := argInt64(req.Args, 0, "size")
		if sizeErr != nil || size < 0 {
			return invalid("truncate size 必须是非负整数")
		}
		if err := os.Truncate(path, size); err != nil {
			return fileFailure("文件截断失败", err, "os_truncate")
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Size() != size {
			return fail("VERIFY_FAILED", "文件截断后大小读回不一致", statErr, "truncate_readback")
		}
		return ok("文件截断并读回验证成功", map[string]any{"path": path, "size": size}, "truncate_readback")
	case "patch":
		content, contentErr := argString(req.Args, "content", "data")
		if contentErr != nil {
			return invalid(contentErr.Error())
		}
		encoding, _ := argOptionalString(req.Args, "utf-8", "encoding")
		payload := []byte(content)
		if strings.EqualFold(encoding, "base64") {
			payload, contentErr = base64.StdEncoding.DecodeString(content)
			if contentErr != nil {
				return invalid("patch content 不是有效 Base64")
			}
		}
		offset, offsetErr := argInt64(req.Args, 0, "offset")
		if offsetErr != nil || offset < 0 {
			return invalid("patch offset 必须是非负整数")
		}
		file, openErr := os.OpenFile(path, os.O_WRONLY, mode)
		if openErr != nil {
			return fileFailure("补丁目标打开失败", openErr, "write_at")
		}
		written, writeErr := file.WriteAt(payload, offset)
		closeErr := file.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return fileFailure("补丁写入失败", writeErr, "write_at")
		}
		return ok("文件补丁写入成功", map[string]any{"path": path, "offset": offset, "bytesWritten": written}, "write_at")
	case "write", "append", "create":
	default:
		return invalidAction(req.Tool, action, "append", "create", "mkdir", "patch", "touch", "truncate", "write")
	}
	content, err := argOptionalString(req.Args, "", "content", "data")
	if err != nil {
		return invalid(err.Error())
	}
	encoding, err := argOptionalString(req.Args, "utf-8", "encoding")
	if err != nil {
		return invalid(err.Error())
	}
	var payload []byte
	if strings.EqualFold(encoding, "base64") {
		payload, err = base64.StdEncoding.DecodeString(content)
		if err != nil {
			return invalid("content 不是有效 Base64")
		}
	} else if strings.EqualFold(encoding, "utf-8") || strings.EqualFold(encoding, "text") {
		payload = []byte(content)
	} else {
		return invalid("encoding 仅支持 utf-8 或 base64")
	}
	createParents, err := argBool(req.Args, "createParents", true)
	if err != nil {
		return invalid(err.Error())
	}
	if createParents {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
			return fileFailure("父目录创建失败", mkErr, "stream_write")
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	switch action {
	case "append":
		flags |= os.O_APPEND
	case "create":
		flags |= os.O_EXCL
	default:
		flags |= os.O_TRUNC
	}
	file, openErr := os.OpenFile(path, flags, mode)
	if openErr != nil {
		return fileFailure("文件打开失败", openErr, "stream_write")
	}
	written, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fileFailure("文件写入失败", writeErr, "stream_write")
	}
	return ok("文件写入成功", map[string]any{"path": path, "bytesWritten": written}, "stream_write")
}

func (m *Manager) fsManageOperation(req Request) Result {
	action, err := actionOf(req, "")
	if err != nil || action == "" {
		return invalid("action 不能为空")
	}
	sourceValue, sourceErr := argOptionalString(req.Args, "", "path", "source", "src")
	var source string
	if sourceErr == nil && sourceValue != "" {
		source, sourceErr = m.resolvePath(sourceValue)
	}
	if sourceErr != nil {
		return invalid(sourceErr.Error())
	}
	switch action {
	case "mkdir":
		if source == "" {
			return invalid("缺少 path")
		}
		parents, _ := argBool(req.Args, "parents", true)
		modeText, _ := argOptionalString(req.Args, "0750", "mode")
		mode, modeErr := parseFileMode(modeText)
		if modeErr != nil {
			return invalid(modeErr.Error())
		}
		if parents {
			err = os.MkdirAll(source, mode)
		} else {
			err = os.Mkdir(source, mode)
		}
		if err != nil {
			return fileFailure("目录创建失败", err, "os_mkdir")
		}
		return ok("目录创建成功", map[string]string{"path": source}, "os_mkdir")
	case "copy", "move", "rename":
		if source == "" {
			return invalid("缺少 source/path")
		}
		destValue, destErr := argString(req.Args, "destination", "dest", "target")
		if destErr != nil {
			return invalid(destErr.Error())
		}
		dest, destErr := m.resolvePath(destValue)
		if destErr != nil {
			return invalid(destErr.Error())
		}
		if action == "copy" {
			overwrite, _ := argBool(req.Args, "overwrite", false)
			if copyErr := copyPath(source, dest, overwrite); copyErr != nil {
				return fileFailure("复制失败", copyErr, "stream_copy")
			}
		} else if moveErr := movePath(source, dest); moveErr != nil {
			return fileFailure("移动或重命名失败", moveErr, "rename_copy_fallback")
		}
		return ok("文件管理操作成功", map[string]string{"source": source, "destination": dest}, action)
	case "delete", "remove":
		if source == "" {
			return invalid("缺少 path")
		}
		recursive, boolErr := argBool(req.Args, "recursive", false)
		if boolErr != nil {
			return invalid(boolErr.Error())
		}
		confirm, _ := argBool(req.Args, "confirmDangerous", false)
		if dangerousDeletion(source) && !confirm {
			return fail("DANGEROUS_OPERATION", "拒绝删除文件系统根或关键顶层目录；确需执行时设置 confirmDangerous=true", errors.New(source), "delete_guard")
		}
		if recursive {
			err = os.RemoveAll(source)
		} else {
			err = os.Remove(source)
		}
		if err != nil {
			return fileFailure("删除失败", err, "os_remove")
		}
		return ok("删除成功", map[string]string{"path": source}, "os_remove")
	case "delete_batch":
		paths, listErr := argStringSlice(req.Args, "paths")
		if listErr != nil {
			return invalid(listErr.Error())
		}
		recursive, _ := argBool(req.Args, "recursive", false)
		results := make([]map[string]any, 0, len(paths))
		failures := 0
		for _, item := range paths {
			resolved, resolveErr := m.resolvePath(item)
			if resolveErr == nil && dangerousDeletion(resolved) {
				resolveErr = errors.New("dangerous target rejected")
			}
			if resolveErr == nil {
				if recursive {
					resolveErr = os.RemoveAll(resolved)
				} else {
					resolveErr = os.Remove(resolved)
				}
			}
			row := map[string]any{"path": resolved, "success": resolveErr == nil}
			if resolveErr != nil {
				row["error"] = resolveErr.Error()
				failures++
			}
			results = append(results, row)
		}
		if failures > 0 {
			result := fail("PARTIAL_FAILURE", "批量删除部分失败", fmt.Errorf("%d item(s) failed", failures), "os_remove_batch")
			result.Data = results
			return result
		}
		return ok("批量删除成功", results, "os_remove_batch")
	case "chmod":
		modeText, modeErr := argString(req.Args, "mode")
		if modeErr != nil {
			return invalid(modeErr.Error())
		}
		mode, modeErr := parseFileMode(modeText)
		if modeErr != nil {
			return invalid(modeErr.Error())
		}
		if err := os.Chmod(source, mode); err != nil {
			return fileFailure("权限修改失败", err, "os_chmod")
		}
		return ok("权限修改成功", map[string]any{"path": source, "mode": fmt.Sprintf("%#o", mode)}, "os_chmod")
	case "chown":
		uid, uidErr := argInt64(req.Args, -1, "uid")
		gid, gidErr := argInt64(req.Args, -1, "gid")
		if uidErr != nil || gidErr != nil || uid < -1 || gid < -1 {
			return invalid("uid/gid 必须是 -1 或非负整数")
		}
		if err := os.Chown(source, int(uid), int(gid)); err != nil {
			return fileFailure("所有者修改失败", err, "os_chown")
		}
		return ok("所有者修改成功", map[string]any{"path": source, "uid": uid, "gid": gid}, "os_chown")
	case "symlink", "hardlink":
		targetValue, targetErr := argString(req.Args, "target", "destination", "dest")
		if targetErr != nil {
			return invalid(targetErr.Error())
		}
		target, targetErr := m.resolvePath(targetValue)
		if targetErr != nil {
			return invalid(targetErr.Error())
		}
		if action == "symlink" {
			err = os.Symlink(source, target)
		} else {
			err = os.Link(source, target)
		}
		if err != nil {
			return fileFailure("链接创建失败", err, "os_link")
		}
		return ok("链接创建成功", map[string]string{"source": source, "target": target}, "os_link")
	case "set_selinux_context", "selinux":
		contextValue, contextErr := argString(req.Args, "context")
		if contextErr != nil {
			return invalid(contextErr.Error())
		}
		path, lookErr := exec.LookPath("chcon")
		if lookErr != nil {
			return unavailable("chcon")
		}
		output, cmdErr := exec.Command(path, contextValue, source).CombinedOutput()
		if cmdErr != nil {
			return fail(classifyCommandError(cmdErr, string(output)), "SELinux Context 修改失败", fmt.Errorf("%w: %s", cmdErr, output), "chcon")
		}
		actual, readErr := readSELinuxContext(source)
		if readErr != nil || actual != contextValue {
			return fail("VERIFY_FAILED", "命令执行后 SELinux Context 未通过读回验证", readErr, "chcon_readback")
		}
		return ok("SELinux Context 修改并验证成功", map[string]string{"path": source, "context": actual}, "chcon_readback")
	default:
		return invalidAction(req.Tool, action, "chmod", "chown", "copy", "delete", "delete_batch", "hardlink", "mkdir", "move", "remove", "rename", "selinux", "set_selinux_context", "symlink")
	}
}

func (m *Manager) fsHashOperation(req Request) Result {
	action, err := actionOf(req, "calculate")
	if err != nil {
		return invalid(err.Error())
	}
	pathValue, err := argString(req.Args, "path")
	if err != nil {
		return invalid(err.Error())
	}
	path, err := m.resolvePath(pathValue)
	if err != nil {
		return invalid(err.Error())
	}
	file, err := os.Open(path)
	if err != nil {
		return fileFailure("文件打开失败", err, "stream_hash")
	}
	defer file.Close()
	var digest string
	algorithm := action
	if action == "calculate" || action == "verify" {
		algorithm, err = argOptionalString(req.Args, "sha256", "algorithm")
		if err != nil {
			return invalid(err.Error())
		}
		algorithm = normalizeTool(algorithm)
	}
	switch algorithm {
	case "md5":
		hash := md5.New()
		if _, err = io.CopyBuffer(hash, file, make([]byte, 256*1024)); err == nil {
			digest = hex.EncodeToString(hash.Sum(nil))
		}
	case "sha1", "sha_1":
		hash := sha1.New()
		if _, err = io.CopyBuffer(hash, file, make([]byte, 256*1024)); err == nil {
			digest = hex.EncodeToString(hash.Sum(nil))
		}
	case "sha256", "sha_256":
		hash := sha256.New()
		if _, err = io.CopyBuffer(hash, file, make([]byte, 256*1024)); err == nil {
			digest = hex.EncodeToString(hash.Sum(nil))
		}
	default:
		return invalid("algorithm 必须是 md5、sha1 或 sha256")
	}
	if err != nil {
		return fileFailure("哈希计算失败", err, "stream_hash")
	}
	if action == "verify" {
		expected, expectedErr := argString(req.Args, "expected", "digest", "hash")
		if expectedErr != nil {
			return invalid(expectedErr.Error())
		}
		if !strings.EqualFold(expected, digest) {
			return fail("CHECKSUM_MISMATCH", "文件哈希校验失败", fmt.Errorf("expected %s, got %s", expected, digest), "stream_hash_verify")
		}
	}
	return ok("哈希计算成功", map[string]string{"path": path, "algorithm": strings.ReplaceAll(algorithm, "_", ""), "digest": digest}, "stream_hash")
}

func (m *Manager) fsSearchOperation(req Request) Result {
	action, err := actionOf(req, "search")
	if err != nil {
		return invalid(err.Error())
	}
	if action == "name" {
		action = "search"
	}
	if action != "search" && action != "content" && action != "large" && action != "duplicates" {
		return invalidAction(req.Tool, action, "content", "duplicates", "large", "name", "search")
	}
	rootValue, err := argOptionalString(req.Args, ".", "path", "root")
	if err != nil {
		return invalid(err.Error())
	}
	root, err := m.resolvePath(rootValue)
	if err != nil {
		return invalid(err.Error())
	}
	if action == "duplicates" {
		return findDuplicates(root, req.Args)
	}
	if action == "content" {
		needle, needleErr := argString(req.Args, "content", "query", "text")
		if needleErr != nil || needle == "" {
			return invalid("content 搜索需要非空 content/query/text")
		}
		limit, limitErr := argInt64(req.Args, 1000, "limit", "maxResults")
		if limitErr != nil || limit <= 0 || limit > 100000 {
			return invalid("limit 必须在 1 到 100000 之间")
		}
		results := make([]map[string]any, 0)
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			if int64(len(results)) >= limit {
				return fs.SkipAll
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return nil
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			line := 0
			for scanner.Scan() {
				line++
				if strings.Contains(scanner.Text(), needle) {
					results = append(results, map[string]any{"path": path, "line": line, "text": scanner.Text()})
					break
				}
			}
			_ = file.Close()
			return nil
		})
		if walkErr != nil {
			return fileFailure("文件内容搜索失败", walkErr, "scanner_content")
		}
		return ok("文件内容搜索完成", map[string]any{"root": root, "count": len(results), "results": results}, "scanner_content")
	}
	name, _ := argOptionalString(req.Args, "", "name", "pattern")
	extension, _ := argOptionalString(req.Args, "", "extension", "ext")
	minSize, err := argInt64(req.Args, 0, "minSize")
	if err != nil || minSize < 0 {
		return invalid("minSize 必须是非负整数")
	}
	maxSize, err := argInt64(req.Args, -1, "maxSize")
	if err != nil || maxSize < -1 {
		return invalid("maxSize 必须是 -1 或非负整数")
	}
	if action == "large" && minSize == 0 {
		minSize = 100 * 1024 * 1024
	}
	limit, err := argInt64(req.Args, 1000, "limit", "maxResults")
	if err != nil || limit <= 0 || limit > 100000 {
		return invalid("limit 必须在 1 到 100000 之间")
	}
	var modifiedAfter, modifiedBefore time.Time
	if raw, ok := req.Args["modifiedAfter"].(string); ok && raw != "" {
		modifiedAfter, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalid("modifiedAfter 必须是 RFC3339 时间")
		}
	}
	if raw, ok := req.Args["modifiedBefore"].(string); ok && raw != "" {
		modifiedBefore, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalid("modifiedBefore 必须是 RFC3339 时间")
		}
	}
	results := make([]fileEntry, 0)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if int64(len(results)) >= limit {
			return fs.SkipAll
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if name != "" {
			matched, matchErr := filepath.Match(name, entry.Name())
			if matchErr != nil {
				return matchErr
			}
			if !matched && !strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(name)) {
				return nil
			}
		}
		if extension != "" && !strings.EqualFold(filepath.Ext(entry.Name()), normalizeExtension(extension)) {
			return nil
		}
		if !info.IsDir() {
			if info.Size() < minSize || maxSize >= 0 && info.Size() > maxSize {
				return nil
			}
		} else if minSize > 0 || maxSize >= 0 {
			return nil
		}
		if !modifiedAfter.IsZero() && info.ModTime().Before(modifiedAfter) {
			return nil
		}
		if !modifiedBefore.IsZero() && info.ModTime().After(modifiedBefore) {
			return nil
		}
		results = append(results, describeFile(path, info))
		return nil
	})
	if walkErr != nil {
		return fileFailure("文件搜索失败", walkErr, "walkdir_filter")
	}
	if action == "large" {
		sort.Slice(results, func(i, j int) bool { return results[i].Size > results[j].Size })
	}
	return ok("文件搜索完成", map[string]any{"root": root, "count": len(results), "results": results}, "walkdir_filter")
}

func findDuplicates(root string, args map[string]any) Result {
	minSize, err := argInt64(args, 1, "minSize")
	if err != nil || minSize < 0 {
		return invalid("minSize 必须是非负整数")
	}
	limit, err := argInt64(args, 10000, "limit", "maxFiles")
	if err != nil || limit <= 0 || limit > 100000 {
		return invalid("maxFiles 必须在 1 到 100000 之间")
	}
	bySize := make(map[int64][]string)
	var count int64
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if count >= limit {
			return fs.SkipAll
		}
		info, infoErr := entry.Info()
		if infoErr == nil && info.Mode().IsRegular() && info.Size() >= minSize {
			bySize[info.Size()] = append(bySize[info.Size()], path)
			count++
		}
		return nil
	})
	if walkErr != nil {
		return fileFailure("重复文件扫描失败", walkErr, "size_sha256")
	}
	type group struct {
		Size   int64    `json:"size"`
		SHA256 string   `json:"sha256"`
		Paths  []string `json:"paths"`
	}
	groups := make([]group, 0)
	for size, paths := range bySize {
		if len(paths) < 2 {
			continue
		}
		byHash := make(map[string][]string)
		for _, path := range paths {
			digest, hashErr := hashFileSHA256(path)
			if hashErr == nil {
				byHash[digest] = append(byHash[digest], path)
			}
		}
		for digest, matches := range byHash {
			if len(matches) > 1 {
				groups = append(groups, group{Size: size, SHA256: digest, Paths: matches})
			}
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Size > groups[j].Size })
	return ok("重复文件扫描完成", map[string]any{"groups": groups, "scannedFiles": count}, "size_sha256")
}

func directorySize(root string) (int64, int64, error) {
	var size, count int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		count++
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, count, err
}

func copyPath(source, destination string, overwrite bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil && !overwrite {
		return os.ErrExist
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if overwrite {
			_ = os.Remove(destination)
		}
		return os.Symlink(target, destination)
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()), overwrite); err != nil {
				return err
			}
		}
		return os.Chtimes(destination, info.ModTime(), info.ModTime())
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, flags, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(output, input, make([]byte, 256*1024))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chtimes(destination, info.ModTime(), info.ModTime())
}

func movePath(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := copyPath(source, destination, false); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(source)
	}
	return os.Remove(source)
}

func dangerousDeletion(path string) bool {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	if clean == root || clean == volume {
		return true
	}
	if filepath.Separator == '/' {
		switch clean {
		case "/system", "/system_ext", "/vendor", "/product", "/odm", "/apex", "/proc", "/sys", "/data", "/metadata":
			return true
		}
	}
	return false
}

func parseFileMode(value string) (os.FileMode, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "0o")
	value = strings.TrimPrefix(value, "0")
	if value == "" {
		return 0, errors.New("mode 不能为空")
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil || parsed > 0o7777 {
		return 0, errors.New("mode 必须是八进制权限，例如 0644")
	}
	return os.FileMode(parsed), nil
}

func fileFailure(message string, err error, strategy string) Result {
	code := "IO_ERROR"
	switch {
	case errors.Is(err, os.ErrNotExist):
		code = "NOT_FOUND"
	case errors.Is(err, os.ErrPermission):
		code = "PERMISSION_DENIED"
	case errors.Is(err, os.ErrExist):
		code = "ALREADY_EXISTS"
	}
	return fail(code, message, err, strategy)
}

func readSELinuxContext(path string) (string, error) {
	command, err := exec.LookPath("ls")
	if err != nil {
		return "", err
	}
	output, err := exec.Command(command, "-Zd", path).Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 || !strings.Contains(fields[0], ":") {
		return "", errors.New("ls -Z 未返回 SELinux Context")
	}
	return fields[0], nil
}

func hashFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, bufio.NewReader(file), make([]byte, 256*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func normalizeExtension(value string) string {
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, ".") {
		return value
	}
	return "." + value
}
