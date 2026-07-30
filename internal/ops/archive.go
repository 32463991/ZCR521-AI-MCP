package ops

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

func (m *Manager) archiveOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "")
	if err != nil || action == "" {
		return invalid("action 不能为空")
	}
	format, err := argOptionalString(req.Args, "", "format", "type")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "create", "compress":
		destinationValue, destErr := argString(req.Args, "destination", "output", "path")
		if destErr != nil {
			return invalid(destErr.Error())
		}
		destination, destErr := m.resolvePath(destinationValue)
		if destErr != nil {
			return invalid(destErr.Error())
		}
		sources, sourceErr := argStringSlice(req.Args, "sources", "source", "inputs")
		if sourceErr != nil {
			return invalid(sourceErr.Error())
		}
		resolved := make([]string, 0, len(sources))
		for _, source := range sources {
			item, resolveErr := m.resolvePath(source)
			if resolveErr != nil {
				return invalid(resolveErr.Error())
			}
			if _, statErr := os.Lstat(item); statErr != nil {
				return fileFailure("压缩源不存在", statErr, "archive")
			}
			resolved = append(resolved, item)
		}
		if format == "" {
			format = inferArchiveFormat(destination)
		}
		if mkErr := os.MkdirAll(filepath.Dir(destination), 0o750); mkErr != nil {
			return fileFailure("压缩包父目录创建失败", mkErr, "archive")
		}
		if createErr := m.createArchive(ctx, strings.ToLower(format), destination, resolved); createErr != nil {
			return archiveFailure("压缩失败", createErr, format)
		}
		info, statErr := os.Stat(destination)
		if statErr != nil || info.Size() == 0 {
			return fail("VERIFY_FAILED", "压缩命令完成但产物不存在或为空", statErr, "archive_readback")
		}
		result := ok("压缩完成并已验证产物", map[string]any{"path": destination, "bytes": info.Size(), "format": format}, "archive_"+format)
		result.Artifacts = []string{destination}
		return result
	case "extract", "decompress":
		sourceValue, sourceErr := argString(req.Args, "source", "path", "archive")
		if sourceErr != nil {
			return invalid(sourceErr.Error())
		}
		source, sourceErr := m.resolvePath(sourceValue)
		if sourceErr != nil {
			return invalid(sourceErr.Error())
		}
		destinationValue, destErr := argOptionalString(req.Args, strings.TrimSuffix(source, filepath.Ext(source)), "destination", "output")
		if destErr != nil {
			return invalid(destErr.Error())
		}
		destination, destErr := m.resolvePath(destinationValue)
		if destErr != nil {
			return invalid(destErr.Error())
		}
		if format == "" {
			format = inferArchiveFormat(source)
		}
		overwrite, boolErr := argBool(req.Args, "overwrite", false)
		if boolErr != nil {
			return invalid(boolErr.Error())
		}
		if mkErr := os.MkdirAll(destination, 0o750); mkErr != nil {
			return fileFailure("解压目录创建失败", mkErr, "archive")
		}
		count, bytes, extractErr := m.extractArchive(ctx, strings.ToLower(format), source, destination, overwrite)
		if extractErr != nil {
			return archiveFailure("解压失败", extractErr, format)
		}
		return ok("解压完成", map[string]any{"source": source, "destination": destination, "entries": count, "bytes": bytes, "format": format}, "archive_"+format)
	case "list", "test":
		sourceValue, sourceErr := argString(req.Args, "source", "path", "archive")
		if sourceErr != nil {
			return invalid(sourceErr.Error())
		}
		source, sourceErr := m.resolvePath(sourceValue)
		if sourceErr != nil {
			return invalid(sourceErr.Error())
		}
		if format == "" {
			format = inferArchiveFormat(source)
		}
		if action == "test" {
			if testErr := m.testArchive(ctx, strings.ToLower(format), source); testErr != nil {
				return archiveFailure("压缩包完整性校验失败", testErr, format)
			}
			return ok("压缩包完整性校验通过", map[string]any{"format": format, "source": source, "valid": true}, "archive_test")
		}
		entries, listErr := m.listArchive(ctx, strings.ToLower(format), source)
		if listErr != nil {
			return archiveFailure("压缩包目录读取失败", listErr, format)
		}
		return ok("压缩包目录读取成功", map[string]any{"format": format, "entries": entries}, "archive_list")
	default:
		return invalidAction(req.Tool, action, "compress", "create", "decompress", "extract", "list", "test")
	}
}

func inferArchiveFormat(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar.xz"):
		return "tar.xz"
	case strings.HasSuffix(lower, ".tgz"):
		return "tgz"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".gz"):
		return "gzip"
	case strings.HasSuffix(lower, ".xz"):
		return "xz"
	case strings.HasSuffix(lower, ".7z"):
		return "7z"
	default:
		return ""
	}
}

func (m *Manager) createArchive(ctx context.Context, format, destination string, sources []string) error {
	switch format {
	case "zip":
		return createZIP(destination, sources)
	case "tar":
		return createTAR(destination, sources, false)
	case "tar.gz", "tgz":
		return createTAR(destination, sources, true)
	case "gzip", "gz":
		if len(sources) != 1 {
			return errors.New("gzip 格式仅支持单个源文件；目录请使用 tar.gz")
		}
		return gzipFile(destination, sources[0])
	case "xz":
		if len(sources) != 1 {
			return errors.New("xz 格式仅支持单个源文件；目录请使用 tar.xz")
		}
		return xzFile(destination, sources[0])
	case "tar.xz":
		return createTARXZ(destination, sources)
	case "7z":
		return m.external7z(ctx, "create", destination, sources, "")
	default:
		return fmt.Errorf("不支持的压缩格式 %q", format)
	}
}

func (m *Manager) extractArchive(ctx context.Context, format, source, destination string, overwrite bool) (int, int64, error) {
	switch format {
	case "zip":
		return extractZIP(source, destination, overwrite)
	case "tar":
		file, err := os.Open(source)
		if err != nil {
			return 0, 0, err
		}
		defer file.Close()
		return extractTAR(tar.NewReader(file), destination, overwrite)
	case "tar.gz", "tgz":
		file, err := os.Open(source)
		if err != nil {
			return 0, 0, err
		}
		defer file.Close()
		reader, err := gzip.NewReader(file)
		if err != nil {
			return 0, 0, err
		}
		defer reader.Close()
		return extractTAR(tar.NewReader(reader), destination, overwrite)
	case "gzip", "gz":
		output := filepath.Join(destination, strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)))
		written, err := gunzipFile(source, output, overwrite)
		return 1, written, err
	case "xz":
		output := filepath.Join(destination, strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)))
		if !overwrite {
			if _, err := os.Stat(output); err == nil {
				return 0, 0, os.ErrExist
			}
		}
		written, err := unxzFile(source, output, overwrite)
		if err != nil {
			return 0, 0, err
		}
		return 1, written, nil
	case "tar.xz":
		file, err := os.Open(source)
		if err != nil {
			return 0, 0, err
		}
		defer file.Close()
		reader, err := xz.NewReader(file)
		if err != nil {
			return 0, 0, err
		}
		return extractTAR(tar.NewReader(reader), destination, overwrite)
	case "7z":
		if err := m.external7z(ctx, "extract", source, nil, destination); err != nil {
			return 0, 0, err
		}
		count, bytes, err := directorySize(destination)
		return int(count), bytes, err
	default:
		return 0, 0, fmt.Errorf("不支持的解压格式 %q", format)
	}
}

func (m *Manager) listArchive(ctx context.Context, format, source string) ([]map[string]any, error) {
	switch format {
	case "zip":
		reader, err := zip.OpenReader(source)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		entries := make([]map[string]any, 0, len(reader.File))
		for _, file := range reader.File {
			entries = append(entries, map[string]any{
				"name": file.Name, "size": file.UncompressedSize64, "compressedSize": file.CompressedSize64, "directory": file.FileInfo().IsDir(),
			})
		}
		return entries, nil
	case "tar", "tar.gz", "tgz", "tar.xz":
		var reader io.ReadCloser
		path := source
		if format == "tar.xz" {
			file, err := os.Open(source)
			if err != nil {
				return nil, err
			}
			xzReader, err := xz.NewReader(file)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			reader = &multiCloser{Reader: xzReader, closers: []io.Closer{file}}
			defer reader.Close()
			return listTARReader(tar.NewReader(xzReader))
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		reader = file
		var tarReader *tar.Reader
		if format == "tar.gz" || format == "tgz" {
			gz, err := gzip.NewReader(file)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			reader = &multiCloser{Reader: gz, closers: []io.Closer{gz, file}}
			tarReader = tar.NewReader(gz)
		} else {
			tarReader = tar.NewReader(file)
		}
		defer reader.Close()
		return listTARReader(tarReader)
	default:
		return nil, fmt.Errorf("格式 %s 暂不支持内部列表；7z 请使用设备 7z 命令", format)
	}
}

func (m *Manager) testArchive(ctx context.Context, format, source string) error {
	switch format {
	case "zip":
		reader, err := zip.OpenReader(source)
		if err != nil {
			return err
		}
		defer reader.Close()
		for _, item := range reader.File {
			stream, openErr := item.Open()
			if openErr != nil {
				return openErr
			}
			_, copyErr := io.CopyBuffer(io.Discard, stream, make([]byte, 256*1024))
			closeErr := stream.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	case "tar", "tar.gz", "tgz", "tar.xz":
		_, err := m.listArchive(ctx, format, source)
		return err
	case "gzip", "gz":
		file, err := os.Open(source)
		if err != nil {
			return err
		}
		defer file.Close()
		reader, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(io.Discard, reader, make([]byte, 256*1024))
		closeErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	case "xz":
		file, err := os.Open(source)
		if err != nil {
			return err
		}
		defer file.Close()
		reader, err := xz.NewReader(file)
		if err != nil {
			return err
		}
		_, err = io.CopyBuffer(io.Discard, reader, make([]byte, 256*1024))
		return err
	case "7z":
		return m.external7z(ctx, "test", source, nil, "")
	default:
		return fmt.Errorf("不支持的压缩校验格式 %q", format)
	}
}

type multiCloser struct {
	io.Reader
	closers []io.Closer
}

func (m *multiCloser) Close() error {
	var first error
	for _, closer := range m.closers {
		if err := closer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func createZIP(destination string, sources []string) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	for _, source := range sources {
		base := filepath.Dir(source)
		err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(relative)
			if info.IsDir() {
				header.Name += "/"
			} else {
				header.Method = zip.Deflate
			}
			out, err := writer.CreateHeader(header)
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				input, err := os.Open(path)
				if err != nil {
					return err
				}
				_, copyErr := io.CopyBuffer(out, input, make([]byte, 256*1024))
				closeErr := input.Close()
				if copyErr != nil {
					return copyErr
				}
				return closeErr
			}
			return nil
		})
		if err != nil {
			break
		}
	}
	closeErr := writer.Close()
	fileErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return fileErr
}

func createTAR(destination string, sources []string, compressed bool) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	var output io.Writer = file
	var gzipWriter *gzip.Writer
	if compressed {
		gzipWriter = gzip.NewWriter(file)
		output = gzipWriter
	}
	writeErr := writeTAR(output, sources)
	var gzipErr error
	if gzipWriter != nil {
		gzipErr = gzipWriter.Close()
	}
	fileErr := file.Close()
	for _, candidate := range []error{writeErr, gzipErr, fileErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func writeTAR(output io.Writer, sources []string) error {
	writer := tar.NewWriter(output)
	var err error
	for _, source := range sources {
		base := filepath.Dir(source)
		err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			var link string
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(path)
				if err != nil {
					return err
				}
			}
			header, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(relative)
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				input, err := os.Open(path)
				if err != nil {
					return err
				}
				_, copyErr := io.CopyBuffer(writer, input, make([]byte, 256*1024))
				closeErr := input.Close()
				if copyErr != nil {
					return copyErr
				}
				return closeErr
			}
			return nil
		})
		if err != nil {
			break
		}
	}
	writerErr := writer.Close()
	for _, candidate := range []error{err, writerErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func createTARXZ(destination string, sources []string) error {
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer, err := xz.NewWriter(file)
	if err != nil {
		_ = file.Close()
		return err
	}
	writeErr := writeTAR(writer, sources)
	closeXZErr := writer.Close()
	closeFileErr := file.Close()
	for _, candidate := range []error{writeErr, closeXZErr, closeFileErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func listTARReader(reader *tar.Reader) ([]map[string]any, error) {
	entries := make([]map[string]any, 0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, map[string]any{"name": header.Name, "size": header.Size, "mode": fmt.Sprintf("%#o", header.Mode), "type": header.Typeflag})
		if len(entries) > 100000 {
			return nil, errors.New("压缩包条目超过安全上限 100000")
		}
	}
	return entries, nil
}

func extractZIP(source, destination string, overwrite bool) (int, int64, error) {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return 0, 0, err
	}
	defer reader.Close()
	var count int
	var total int64
	for _, item := range reader.File {
		target, err := safeArchivePath(destination, item.Name)
		if err != nil {
			return count, total, err
		}
		if err := rejectArchiveSymlinkComponents(destination, target); err != nil {
			return count, total, err
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(target, item.Mode().Perm()); err != nil {
				return count, total, err
			}
			continue
		}
		if !overwrite {
			if _, err := os.Lstat(target); err == nil {
				return count, total, os.ErrExist
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return count, total, err
		}
		input, err := item.Open()
		if err != nil {
			return count, total, err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, item.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return count, total, err
		}
		written, copyErr := io.CopyBuffer(output, input, make([]byte, 256*1024))
		closeOutErr := output.Close()
		closeInErr := input.Close()
		for _, candidate := range []error{copyErr, closeOutErr, closeInErr} {
			if candidate != nil {
				return count, total, candidate
			}
		}
		total += written
		count++
		if count > 100000 || total > 100*1024*1024*1024 {
			return count, total, errors.New("解压内容超过安全上限")
		}
	}
	return count, total, nil
}

func extractTAR(reader *tar.Reader, destination string, overwrite bool) (int, int64, error) {
	var count int
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, total, err
		}
		target, err := safeArchivePath(destination, header.Name)
		if err != nil {
			return count, total, err
		}
		if err := rejectArchiveSymlinkComponents(destination, target); err != nil {
			return count, total, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, os.FileMode(header.Mode).Perm())
		case tar.TypeReg, tar.TypeRegA:
			if !overwrite {
				if _, statErr := os.Lstat(target); statErr == nil {
					return count, total, os.ErrExist
				}
			}
			if err = os.MkdirAll(filepath.Dir(target), 0o750); err == nil {
				var output *os.File
				output, err = os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode).Perm())
				if err == nil {
					var written int64
					written, err = io.CopyBuffer(output, reader, make([]byte, 256*1024))
					closeErr := output.Close()
					if err == nil {
						err = closeErr
					}
					total += written
				}
			}
		case tar.TypeSymlink:
			if err = validateArchiveSymlink(destination, target, header.Linkname); err != nil {
				return count, total, err
			}
			err = os.Symlink(header.Linkname, target)
		case tar.TypeLink:
			var linkTarget string
			linkTarget, err = safeArchivePath(destination, header.Linkname)
			if err == nil {
				err = rejectArchiveSymlinkComponents(destination, linkTarget)
			}
			if err == nil {
				err = os.Link(linkTarget, target)
			}
		default:
			continue
		}
		if err != nil {
			return count, total, err
		}
		count++
		if count > 100000 || total > 100*1024*1024*1024 {
			return count, total, errors.New("解压内容超过安全上限")
		}
	}
	return count, total, nil
}

func safeArchivePath(root, name string) (string, error) {
	if archivePathIsAbsolute(name) {
		return "", fmt.Errorf("拒绝压缩包绝对路径 %q", name)
	}
	normalized := strings.ReplaceAll(name, `\`, "/")
	target := filepath.Join(root, filepath.FromSlash(normalized))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("拒绝路径穿越条目 %q", name)
	}
	return target, nil
}

func rejectArchiveSymlinkComponents(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("拒绝目标目录之外的解压路径 %q", target)
	}
	current := filepath.Clean(root)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝经符号链接解压 %q", current)
		}
	}
	return nil
}

func validateArchiveSymlink(root, target, linkName string) error {
	if archivePathIsAbsolute(linkName) {
		return errors.New("拒绝解压绝对路径符号链接")
	}
	normalized := strings.ReplaceAll(linkName, `\`, "/")
	resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(normalized))
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("拒绝指向目标目录之外的符号链接 %q", linkName)
	}
	return nil
}

func archivePathIsAbsolute(name string) bool {
	if filepath.IsAbs(name) {
		return true
	}
	normalized := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalized, "/") {
		return true
	}
	return len(normalized) >= 2 &&
		((normalized[0] >= 'a' && normalized[0] <= 'z') ||
			(normalized[0] >= 'A' && normalized[0] <= 'Z')) &&
		normalized[1] == ':'
}

func gzipFile(destination, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("gzip 不能直接压缩目录")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer := gzip.NewWriter(output)
	writer.Name = filepath.Base(source)
	_, copyErr := io.CopyBuffer(writer, input, make([]byte, 256*1024))
	closeGzipErr := writer.Close()
	closeFileErr := output.Close()
	for _, candidate := range []error{copyErr, closeGzipErr, closeFileErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func gunzipFile(source, destination string, overwrite bool) (int64, error) {
	if !overwrite {
		if _, err := os.Stat(destination); err == nil {
			return 0, os.ErrExist
		}
	}
	input, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.CopyBuffer(output, reader, make([]byte, 256*1024))
	closeErr := output.Close()
	if copyErr != nil {
		return written, copyErr
	}
	return written, closeErr
}

func xzFile(destination, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("xz 不能直接压缩目录")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer, err := xz.NewWriter(output)
	if err != nil {
		_ = output.Close()
		return err
	}
	_, copyErr := io.CopyBuffer(writer, input, make([]byte, 256*1024))
	closeXZErr := writer.Close()
	closeFileErr := output.Close()
	for _, candidate := range []error{copyErr, closeXZErr, closeFileErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func unxzFile(source, destination string, overwrite bool) (int64, error) {
	if !overwrite {
		if _, err := os.Stat(destination); err == nil {
			return 0, os.ErrExist
		}
	}
	input, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	reader, err := xz.NewReader(input)
	if err != nil {
		return 0, err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.CopyBuffer(output, reader, make([]byte, 256*1024))
	closeErr := output.Close()
	if copyErr != nil {
		return written, copyErr
	}
	return written, closeErr
}

func (m *Manager) external7z(ctx context.Context, action, archive string, sources []string, destination string) error {
	command, findErr := find7Zip()
	if findErr != nil {
		return findErr
	}
	args := []string{}
	switch action {
	case "create":
		args = append([]string{"a", "-y", archive}, sources...)
	case "extract":
		args = []string{"x", "-y", "-o" + destination, archive}
	case "test":
		args = []string{"t", "-y", archive}
	default:
		return errors.New("未知 7z 操作")
	}
	result := m.runCommand(ctx, commandSpec{Name: command, Args: args, Dir: m.cfg.WorkDir, Timeout: 30 * time.Minute, Strategy: "external_7z"})
	if !result.Success {
		return fmt.Errorf("%s: %s", result.Code, result.Error)
	}
	return nil
}

func find7Zip() (string, error) {
	var configuredErr error
	if configured := strings.TrimSpace(os.Getenv("ZCR521_7ZZ")); configured != "" {
		info, err := os.Stat(configured)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			executable := info.Mode().Perm()&0o111 != 0
			if runtime.GOOS == "windows" {
				switch strings.ToLower(filepath.Ext(configured)) {
				case ".exe", ".com", ".bat", ".cmd":
					executable = true
				}
			}
			if executable {
				return filepath.Clean(configured), nil
			}
			configuredErr = fmt.Errorf("ZCR521_7ZZ 不是可执行文件: %s", configured)
		} else if err != nil {
			configuredErr = fmt.Errorf("ZCR521_7ZZ 不可用: %w", err)
		} else {
			configuredErr = fmt.Errorf("ZCR521_7ZZ 必须是非空普通文件: %s", configured)
		}
	}
	for _, candidate := range []string{"7zz", "7z", "7za"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	if configuredErr != nil {
		return "", fmt.Errorf("COMMAND_UNAVAILABLE: %w; PATH 中也没有 7zz/7z/7za", configuredErr)
	}
	return "", errors.New("COMMAND_UNAVAILABLE: 7zz/7z/7za")
}

func archiveFailure(message string, err error, format string) Result {
	code := "ARCHIVE_FAILED"
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "command_unavailable") {
		code = "COMMAND_UNAVAILABLE"
	}
	if errors.Is(err, os.ErrExist) {
		code = "ALREADY_EXISTS"
	}
	return fail(code, message, err, "archive_"+format)
}
