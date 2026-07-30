package ops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) downloadOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "download")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "status":
		req.Tool = "task_get"
		return m.taskOperation(req)
	case "cancel":
		req.Tool = "task_cancel"
		return m.taskOperation(req)
	case "batch":
		rawItems, exists := req.Args["items"].([]any)
		if !exists || len(rawItems) == 0 || len(rawItems) > 64 {
			return invalid("batch items 必须是包含 1 到 64 个下载参数对象的数组")
		}
		results := make([]Result, 0, len(rawItems))
		succeeded := 0
		for _, rawItem := range rawItems {
			item, ok := rawItem.(map[string]any)
			if !ok {
				return invalid("batch items 中的每一项都必须是对象")
			}
			childArgs := copyArgs(item)
			childArgs["action"] = "start"
			child := m.downloadOperation(ctx, Request{Tool: req.Tool, Args: childArgs})
			results = append(results, child)
			if child.Success {
				succeeded++
			}
		}
		result := ok("批量下载完成", map[string]any{"count": len(results), "succeeded": succeeded, "results": results}, "http_batch_download")
		if succeeded != len(results) {
			result.Success = false
			result.Code = "PARTIAL_FAILURE"
			result.Message = "批量下载部分失败"
		}
		return result
	case "download", "start":
	default:
		return invalidAction(req.Tool, action, "batch", "cancel", "download", "start", "status")
	}
	rawURL, err := argString(req.Args, "url")
	if err != nil {
		return invalid(err.Error())
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return invalid("url 必须是有效的 HTTP 或 HTTPS 地址")
	}
	defaultName := filepath.Base(parsed.Path)
	if defaultName == "." || defaultName == "/" || defaultName == "" {
		defaultName = "download-" + time.Now().UTC().Format("20060102-150405")
	}
	defaultPath := filepath.Join(m.cfg.WorkDir, "downloads", defaultName)
	pathValue, err := argOptionalString(req.Args, defaultPath, "path", "destination", "output")
	if err != nil {
		return invalid(err.Error())
	}
	destination, err := m.resolvePath(pathValue)
	if err != nil {
		return invalid(err.Error())
	}
	overwrite, err := argBool(req.Args, "overwrite", false)
	if err != nil {
		return invalid(err.Error())
	}
	resume, err := argBool(req.Args, "resume", true)
	if err != nil {
		return invalid(err.Error())
	}
	retries, err := argInt64(req.Args, 2, "retries")
	if err != nil || retries < 0 || retries > 10 {
		return invalid("retries 必须在 0 到 10 之间")
	}
	expectedSHA, err := argOptionalString(req.Args, "", "sha256")
	if err != nil {
		return invalid(err.Error())
	}
	if expectedSHA != "" {
		if len(expectedSHA) != 64 {
			return invalid("sha256 必须是 64 位十六进制摘要")
		}
		if _, err := hex.DecodeString(expectedSHA); err != nil {
			return invalid("sha256 必须是十六进制摘要")
		}
	}
	maxBytes, err := argInt64(req.Args, 0, "maxBytes")
	if err != nil || maxBytes < 0 {
		return invalid("maxBytes 必须是非负整数，0 表示不限制")
	}
	headers, err := argStringMap(req.Args, "headers")
	if err != nil {
		return invalid(err.Error())
	}
	timeout, err := argDuration(req.Args, 30*time.Minute)
	if err != nil {
		return invalid(err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fileFailure("下载目录创建失败", err, "http_download")
	}
	if !overwrite {
		if _, statErr := os.Stat(destination); statErr == nil {
			return fail("ALREADY_EXISTS", "目标文件已存在", os.ErrExist, "http_download")
		}
	}
	temp := destination + ".part"
	if !resume {
		_ = os.Remove(temp)
	}

	started := time.Now()
	var downloaded int64
	var status int
	var lastErr error
	for attempt := int64(0); attempt <= retries; attempt++ {
		downloaded, status, lastErr = downloadAttempt(ctx, rawURL, temp, headers, timeout, maxBytes)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if attempt < retries {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	if lastErr != nil {
		code := "DOWNLOAD_FAILED"
		if errors.Is(lastErr, context.Canceled) {
			code = "CANCELLED"
		} else if errors.Is(lastErr, context.DeadlineExceeded) {
			code = "TIMEOUT"
		}
		result := fail(code, "文件下载失败，临时文件已保留用于断点续传", lastErr, "http_range_download")
		result.Data = map[string]any{"temporaryPath": temp, "bytesDownloaded": downloaded, "httpStatus": status}
		return result
	}
	digest, err := hashFileSHA256(temp)
	if err != nil {
		return fileFailure("下载完成但 SHA-256 计算失败", err, "http_range_download")
	}
	if expectedSHA != "" && !strings.EqualFold(digest, expectedSHA) {
		return fail("CHECKSUM_MISMATCH", "下载文件 SHA-256 校验失败，临时文件已保留", fmt.Errorf("expected %s, got %s", expectedSHA, digest), "http_range_download")
	}
	if overwrite {
		_ = os.Remove(destination)
	}
	if err := os.Rename(temp, destination); err != nil {
		return fileFailure("下载完成但原子重命名失败", err, "atomic_rename")
	}
	elapsed := time.Since(started)
	speed := float64(downloaded)
	if elapsed > 0 {
		speed /= elapsed.Seconds()
	}
	result := ok("文件下载完成并已验证", map[string]any{
		"path":            destination,
		"bytes":           downloaded,
		"sha256":          digest,
		"httpStatus":      status,
		"durationMs":      elapsed.Milliseconds(),
		"averageBytesSec": speed,
	}, "http_range_download")
	result.Artifacts = []string{destination}
	return result
}

func downloadAttempt(parent context.Context, rawURL, path string, headers map[string]string, timeout time.Duration, maxBytes int64) (int64, int, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var offset int64
	if info, err := os.Stat(path); err == nil {
		offset = info.Size()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return offset, 0, err
	}
	request.Header.Set("User-Agent", "ZCR521-Android-AI-MCP/1")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数超过 10")
			}
			for key, values := range via[0].Header {
				for _, value := range values {
					next.Header.Add(key, value)
				}
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return offset, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return offset, response.StatusCode, fmt.Errorf("HTTP %d %s", response.StatusCode, response.Status)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && response.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}
	if maxBytes > 0 {
		total := response.ContentLength
		if total >= 0 && total+offset > maxBytes {
			return offset, response.StatusCode, fmt.Errorf("响应大小 %d 超过 maxBytes=%d", total+offset, maxBytes)
		}
	}
	file, err := os.OpenFile(path, flags, 0o640)
	if err != nil {
		return offset, response.StatusCode, err
	}
	reader := io.Reader(response.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(response.Body, maxBytes-offset+1)
	}
	written, copyErr := io.CopyBuffer(file, reader, make([]byte, 256*1024))
	closeErr := file.Close()
	if copyErr != nil {
		return offset + written, response.StatusCode, copyErr
	}
	if closeErr != nil {
		return offset + written, response.StatusCode, closeErr
	}
	if maxBytes > 0 && offset+written > maxBytes {
		return offset + written, response.StatusCode, fmt.Errorf("下载大小超过 maxBytes=%d", maxBytes)
	}
	return offset + written, response.StatusCode, nil
}

func (m *Manager) uploadOperation(req Request) Result {
	action, err := actionOf(req, "")
	if err != nil || action == "" {
		return invalid("action 不能为空")
	}
	pathValue, err := argString(req.Args, "path", "destination")
	if err != nil {
		return invalid(err.Error())
	}
	destination, err := m.resolvePath(pathValue)
	if err != nil {
		return invalid(err.Error())
	}
	temp := destination + ".uploading"
	switch action {
	case "create":
		if mkErr := os.MkdirAll(filepath.Dir(temp), 0o750); mkErr != nil {
			return fileFailure("上传目录创建失败", mkErr, "chunk_upload")
		}
		overwrite, boolErr := argBool(req.Args, "overwrite", false)
		if boolErr != nil {
			return invalid(boolErr.Error())
		}
		flags := os.O_CREATE | os.O_WRONLY
		if overwrite {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_EXCL
		}
		file, createErr := os.OpenFile(temp, flags, 0o640)
		if createErr != nil {
			if errors.Is(createErr, os.ErrExist) {
				return fail("ALREADY_EXISTS", "上传会话已存在", createErr, "chunk_upload")
			}
			return fileFailure("上传会话创建失败", createErr, "chunk_upload")
		}
		if closeErr := file.Close(); closeErr != nil {
			return fileFailure("上传会话创建失败", closeErr, "chunk_upload")
		}
		return ok("上传会话创建成功", map[string]any{"path": destination, "temporaryPath": temp, "nextOffset": 0}, "chunk_upload")
	case "status":
		info, statErr := os.Stat(temp)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return ok("上传会话不存在", map[string]any{"path": destination, "active": false, "bytes": 0}, "chunk_upload")
			}
			return fileFailure("上传状态读取失败", statErr, "chunk_upload")
		}
		return ok("上传状态读取成功", map[string]any{"path": destination, "temporaryPath": temp, "active": true, "bytes": info.Size()}, "chunk_upload")
	case "cancel":
		if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fileFailure("上传取消失败", err, "chunk_upload")
		}
		return ok("上传已取消，临时文件已删除", map[string]string{"path": destination}, "chunk_upload")
	case "chunk", "write_chunk":
		content, contentErr := argString(req.Args, "content", "data")
		if contentErr != nil {
			return invalid(contentErr.Error())
		}
		payload, decodeErr := base64.StdEncoding.DecodeString(content)
		if decodeErr != nil {
			return invalid("分块 content 必须是 Base64")
		}
		if len(payload) > 8*1024*1024 {
			return invalid("单个上传分块不得超过 8 MiB")
		}
		offset, offsetErr := argInt64(req.Args, 0, "offset")
		if offsetErr != nil || offset < 0 {
			return invalid("offset 必须是非负整数")
		}
		if mkErr := os.MkdirAll(filepath.Dir(temp), 0o750); mkErr != nil {
			return fileFailure("上传目录创建失败", mkErr, "chunk_upload")
		}
		file, openErr := os.OpenFile(temp, os.O_CREATE|os.O_RDWR, 0o640)
		if openErr != nil {
			return fileFailure("上传临时文件打开失败", openErr, "chunk_upload")
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return fileFailure("上传临时文件读取失败", statErr, "chunk_upload")
		}
		if info.Size() != offset {
			_ = file.Close()
			return fail("OFFSET_MISMATCH", "上传 offset 与服务端已接收长度不一致", fmt.Errorf("expected %d, got %d", info.Size(), offset), "chunk_upload")
		}
		if _, seekErr := file.Seek(offset, io.SeekStart); seekErr != nil {
			_ = file.Close()
			return fileFailure("上传文件定位失败", seekErr, "chunk_upload")
		}
		written, writeErr := file.Write(payload)
		syncErr := file.Sync()
		closeErr := file.Close()
		for _, candidate := range []error{writeErr, syncErr, closeErr} {
			if candidate != nil {
				return fileFailure("上传分块写入失败", candidate, "chunk_upload")
			}
		}
		return ok("上传分块写入成功", map[string]any{"path": destination, "offset": offset, "bytesWritten": written, "nextOffset": offset + int64(written)}, "chunk_upload")
	case "complete":
		expectedSize, sizeErr := argInt64(req.Args, -1, "size", "expectedSize")
		if sizeErr != nil || expectedSize < -1 {
			return invalid("expectedSize 必须是 -1 或非负整数")
		}
		expectedSHA, shaErr := argOptionalString(req.Args, "", "sha256")
		if shaErr != nil {
			return invalid(shaErr.Error())
		}
		info, statErr := os.Stat(temp)
		if statErr != nil {
			return fileFailure("上传临时文件不存在", statErr, "chunk_upload")
		}
		if expectedSize >= 0 && info.Size() != expectedSize {
			return fail("SIZE_MISMATCH", "上传文件大小校验失败", fmt.Errorf("expected %d, got %d", expectedSize, info.Size()), "chunk_upload")
		}
		digest, hashErr := hashFileSHA256(temp)
		if hashErr != nil {
			return fileFailure("上传文件哈希计算失败", hashErr, "chunk_upload")
		}
		if expectedSHA != "" && !strings.EqualFold(expectedSHA, digest) {
			return fail("CHECKSUM_MISMATCH", "上传文件 SHA-256 校验失败", fmt.Errorf("expected %s, got %s", expectedSHA, digest), "chunk_upload")
		}
		overwrite, _ := argBool(req.Args, "overwrite", false)
		if _, statErr := os.Stat(destination); statErr == nil {
			if !overwrite {
				return fail("ALREADY_EXISTS", "上传目标已存在", os.ErrExist, "chunk_upload")
			}
			if removeErr := os.Remove(destination); removeErr != nil {
				return fileFailure("无法覆盖上传目标", removeErr, "chunk_upload")
			}
		}
		if renameErr := os.Rename(temp, destination); renameErr != nil {
			return fileFailure("上传完成重命名失败", renameErr, "chunk_upload")
		}
		result := ok("上传完成并已校验", map[string]any{"path": destination, "bytes": info.Size(), "sha256": digest}, "chunk_upload")
		result.Artifacts = []string{destination}
		return result
	default:
		return invalidAction(req.Tool, action, "cancel", "chunk", "complete", "create", "status", "write_chunk")
	}
}

func (m *Manager) exportOperation(req Request) Result {
	action, err := actionOf(req, "prepare")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "task":
		taskReq := Request{Tool: "task_artifacts", Args: req.Args}
		taskResult := m.taskOperation(taskReq)
		if !taskResult.Success {
			return taskResult
		}
		artifacts, ok := taskResult.Data.([]string)
		if !ok || len(artifacts) == 0 {
			return fail("NOT_FOUND", "任务没有可导出的产物", os.ErrNotExist, "task_export")
		}
		index, indexErr := argInt64(req.Args, 0, "artifactIndex", "index")
		if indexErr != nil || index < 0 || index >= int64(len(artifacts)) {
			return invalid("artifactIndex 超出任务产物范围")
		}
		childArgs := copyArgs(req.Args)
		childArgs["action"] = "file"
		childArgs["path"] = artifacts[index]
		return m.exportOperation(Request{Tool: req.Tool, Args: childArgs})
	case "directory":
		pathValue, pathErr := argString(req.Args, "path")
		if pathErr != nil {
			return invalid(pathErr.Error())
		}
		source, pathErr := m.resolvePath(pathValue)
		if pathErr != nil {
			return invalid(pathErr.Error())
		}
		info, statErr := os.Stat(source)
		if statErr != nil {
			return fileFailure("导出目录读取失败", statErr, "directory_export")
		}
		if !info.IsDir() {
			return invalid("directory action 的 path 必须是目录")
		}
		defaultDestination := filepath.Join(m.cfg.StateDir, "exports", info.Name()+"-"+time.Now().UTC().Format("20060102-150405")+".tar.xz")
		destinationValue, destinationErr := argOptionalString(req.Args, defaultDestination, "destination", "output")
		if destinationErr != nil {
			return invalid(destinationErr.Error())
		}
		destination, destinationErr := m.resolvePath(destinationValue)
		if destinationErr != nil {
			return invalid(destinationErr.Error())
		}
		if mkErr := os.MkdirAll(filepath.Dir(destination), 0o750); mkErr != nil {
			return fileFailure("目录导出产物目录创建失败", mkErr, "directory_export")
		}
		if createErr := createTARXZ(destination, []string{source}); createErr != nil {
			return archiveFailure("目录导出压缩失败", createErr, "tar.xz")
		}
		childArgs := copyArgs(req.Args)
		childArgs["action"] = "file"
		childArgs["path"] = destination
		result := m.exportOperation(Request{Tool: req.Tool, Args: childArgs})
		if result.Success {
			result.Strategy = "directory_tar_xz_export"
		}
		return result
	case "file", "prepare", "info":
	default:
		return invalidAction(req.Tool, action, "directory", "file", "info", "prepare", "task")
	}
	pathValue, err := argString(req.Args, "path")
	if err != nil {
		return invalid(err.Error())
	}
	path, err := m.resolvePath(pathValue)
	if err != nil {
		return invalid(err.Error())
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileFailure("导出路径读取失败", err, "stream_export")
	}
	if info.IsDir() {
		return fail("INVALID_ARGUMENT", "目录必须先通过 zcr521_archive 压缩后再导出", errors.New("directory export requires archive"), "stream_export")
	}
	digest, err := hashFileSHA256(path)
	if err != nil {
		return fileFailure("导出文件校验失败", err, "stream_export")
	}
	result := ok("导出文件已准备；传输层应对该路径流式响应，不应编码为巨大 Base64", map[string]any{
		"path": path, "name": info.Name(), "bytes": info.Size(), "sha256": digest, "stream": true,
	}, "stream_export")
	result.Artifacts = []string{path}
	return result
}

func computeSHA256(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, reader, make([]byte, 256*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
