package frontend

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zcr521/android-ai-mcp/internal/atomicfile"
)

const transferIDBytes = 16

// TransferOptions controls the resumable upload and download store.
type TransferOptions struct {
	Directory      string
	MaxUploadBytes int64
	MaxChunkBytes  int64
	UploadTTL      time.Duration
	ArtifactTTL    time.Duration
}

// TransferArtifact is an expiring, opaque download reference.
type TransferArtifact struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	DownloadURL string    `json:"downloadUrl"`
	DevicePath  string    `json:"devicePath,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Path        string    `json:"-"`
}

type uploadRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Total     int64     `json:"total"`
	Received  int64     `json:"received"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type artifactRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Owned     bool      `json:"owned"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type uploadCreateRequest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// TransferManager stores only bounded metadata in memory. File contents are
// always copied directly between an HTTP stream and disk.
type TransferManager struct {
	options   TransferOptions
	uploads   string
	artifacts string
	mu        sync.Mutex
	locks     map[string]*transferLock
	now       func() time.Time
}

type transferLock struct {
	mutex sync.Mutex
	refs  int
}

// NewTransferManager creates private metadata and spool directories.
func NewTransferManager(options TransferOptions) (*TransferManager, error) {
	if strings.TrimSpace(options.Directory) == "" {
		return nil, errors.New("传输目录为空")
	}
	if options.MaxUploadBytes <= 0 {
		return nil, errors.New("最大上传大小必须大于零")
	}
	if options.MaxChunkBytes <= 0 || options.MaxChunkBytes > options.MaxUploadBytes {
		return nil, errors.New("分块大小超出有效范围")
	}
	if options.UploadTTL <= 0 || options.ArtifactTTL <= 0 {
		return nil, errors.New("传输 TTL 必须大于零")
	}
	manager := &TransferManager{
		options:   options,
		uploads:   filepath.Join(options.Directory, "uploads"),
		artifacts: filepath.Join(options.Directory, "artifacts"),
		locks:     map[string]*transferLock{},
		now:       time.Now,
	}
	for _, directory := range []string{options.Directory, manager.uploads, manager.artifacts} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func (m *TransferManager) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var request uploadCreateRequest
	if err := decodeSingleJSON(r, &request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "上传元数据超过大小限制")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_UPLOAD", err.Error())
		return
	}
	record, err := m.CreateUpload(request.Name, request.Size, request.SHA256)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errTransferTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeAPIError(w, status, "UPLOAD_REJECTED", err.Error())
		return
	}
	w.Header().Set("Location", "/transfer/upload/"+record.ID)
	writeJSON(w, http.StatusCreated, m.uploadResponse(record))
}

func (m *TransferManager) handleUpload(w http.ResponseWriter, r *http.Request) {
	remainder, ok := trimPrefix(r.URL.Path, "/transfer/upload/", "/transfer/uploads/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || !validTransferID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			record, err := m.UploadStatus(id)
			if err != nil {
				transferLookupError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, m.uploadResponse(record))
		case http.MethodPut:
			record, err := m.WriteChunk(id, r.Header.Get("Content-Range"), r.Body)
			if err != nil {
				transferWriteError(w, err)
				return
			}
			w.Header().Set("Upload-Offset", strconv.FormatInt(record.Received, 10))
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if err := m.CancelUpload(id); err != nil {
				transferLookupError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(w, "GET, PUT, DELETE")
		}
		return
	}
	if len(parts) == 2 && parts[1] == "complete" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return
		}
		artifact, err := m.CompleteUpload(id)
		if err != nil {
			transferWriteError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, artifact)
		return
	}
	http.NotFound(w, r)
}

func (m *TransferManager) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	id, ok := trimPrefix(r.URL.Path, "/transfer/download/", "/transfer/files/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !validTransferID(id) {
		http.NotFound(w, r)
		return
	}
	artifact, err := m.loadArtifact(id)
	if err != nil {
		transferLookupError(w, err)
		return
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		writeAPIError(w, http.StatusGone, "ARTIFACT_UNAVAILABLE", "文件已不可用")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeAPIError(w, http.StatusGone, "ARTIFACT_UNAVAILABLE", "文件已不可用")
		return
	}
	if info.Size() != artifact.Size {
		writeAPIError(w, http.StatusConflict, "ARTIFACT_CHANGED", "文件在发布后发生变化，请重新发布")
		return
	}
	w.Header().Set("Content-Disposition", contentDisposition(artifact.Name))
	w.Header().Set("Cache-Control", "private, no-store")
	if artifact.SHA256 != "" {
		decoded, _ := hex.DecodeString(artifact.SHA256)
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(decoded))
		w.Header().Set("X-Checksum-SHA256", artifact.SHA256)
		w.Header().Set("ETag", `"`+artifact.SHA256+`"`)
	}
	http.ServeContent(w, r, artifact.Name, info.ModTime(), file)
}

var (
	errTransferNotFound = errors.New("传输不存在")
	errTransferExpired  = errors.New("传输已过期")
	errTransferTooLarge = errors.New("文件超过上传限制")
	errOffsetConflict   = errors.New("分块偏移与服务端进度不一致")
	errHashMismatch     = errors.New("SHA-256 校验失败")
)

// CreateUpload starts an empty resumable upload.
func (m *TransferManager) CreateUpload(name string, size int64, expectedSHA256 string) (uploadRecord, error) {
	name, err := safeFilename(name)
	if err != nil {
		return uploadRecord{}, err
	}
	if size < 0 {
		return uploadRecord{}, errors.New("文件大小不能为负数")
	}
	if size > m.options.MaxUploadBytes {
		return uploadRecord{}, errTransferTooLarge
	}
	expectedSHA256, err = normalizeSHA256(expectedSHA256)
	if err != nil {
		return uploadRecord{}, err
	}
	m.cleanupExpired()
	id, err := randomTransferID()
	if err != nil {
		return uploadRecord{}, err
	}
	now := m.now().UTC()
	record := uploadRecord{
		ID:        id,
		Name:      name,
		Total:     size,
		SHA256:    expectedSHA256,
		CreatedAt: now,
		UpdatedAt: now,
	}
	part, err := os.OpenFile(m.uploadPartPath(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return uploadRecord{}, err
	}
	if err := part.Close(); err != nil {
		_ = os.Remove(m.uploadPartPath(id))
		return uploadRecord{}, err
	}
	if err := m.saveUpload(record); err != nil {
		_ = os.Remove(m.uploadPartPath(id))
		return uploadRecord{}, err
	}
	return record, nil
}

// UploadStatus returns the durable offset used for resumption.
func (m *TransferManager) UploadStatus(id string) (uploadRecord, error) {
	unlock := m.lockTransfer(id)
	defer unlock()
	return m.loadUpload(id)
}

// WriteChunk appends one Content-Range chunk. Out-of-order writes are rejected
// so retries cannot create sparse or silently corrupted files.
func (m *TransferManager) WriteChunk(id, contentRange string, body io.Reader) (uploadRecord, error) {
	start, end, total, err := parseContentRange(contentRange)
	if err != nil {
		return uploadRecord{}, err
	}
	length := end - start + 1
	if length > m.options.MaxChunkBytes {
		return uploadRecord{}, errTransferTooLarge
	}
	unlock := m.lockTransfer(id)
	defer unlock()
	record, err := m.loadUpload(id)
	if err != nil {
		return uploadRecord{}, err
	}
	if total != record.Total || start != record.Received || end >= record.Total {
		return uploadRecord{}, errOffsetConflict
	}
	file, err := os.OpenFile(m.uploadPartPath(id), os.O_WRONLY, 0o600)
	if err != nil {
		return uploadRecord{}, err
	}
	defer file.Close()
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return uploadRecord{}, err
	}
	written, copyErr := io.CopyN(file, body, length)
	if copyErr != nil {
		_ = file.Truncate(start)
		return uploadRecord{}, fmt.Errorf("分块长度不足: %w", copyErr)
	}
	var extra [1]byte
	if count, readErr := body.Read(extra[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		_ = file.Truncate(start)
		return uploadRecord{}, errors.New("分块长度超过 Content-Range")
	}
	if written != length {
		_ = file.Truncate(start)
		return uploadRecord{}, errors.New("分块写入不完整")
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(start)
		return uploadRecord{}, err
	}
	record.Received += written
	record.UpdatedAt = m.now().UTC()
	if err := m.saveUpload(record); err != nil {
		_ = file.Truncate(start)
		return uploadRecord{}, err
	}
	return record, nil
}

// CompleteUpload verifies the full stream and atomically publishes it.
func (m *TransferManager) CompleteUpload(id string) (TransferArtifact, error) {
	unlock := m.lockTransfer(id)
	defer unlock()
	record, err := m.loadUpload(id)
	if err != nil {
		return TransferArtifact{}, err
	}
	if record.Received != record.Total {
		return TransferArtifact{}, errors.New("上传尚未完成")
	}
	actualSHA256, err := fileSHA256(m.uploadPartPath(id))
	if err != nil {
		return TransferArtifact{}, err
	}
	if record.SHA256 != "" && record.SHA256 != actualSHA256 {
		return TransferArtifact{}, errHashMismatch
	}
	finalPath := filepath.Join(m.artifacts, id+"-"+record.Name)
	if err := os.Rename(m.uploadPartPath(id), finalPath); err != nil {
		return TransferArtifact{}, err
	}
	now := m.now().UTC()
	artifact := artifactRecord{
		ID:        id,
		Name:      record.Name,
		Path:      finalPath,
		Size:      record.Total,
		SHA256:    actualSHA256,
		Owned:     true,
		CreatedAt: now,
		ExpiresAt: now.Add(m.options.ArtifactTTL),
	}
	if err := m.saveArtifact(artifact); err != nil {
		_ = os.Rename(finalPath, m.uploadPartPath(id))
		return TransferArtifact{}, err
	}
	_ = os.Remove(m.uploadMetaPath(id))
	return publicArtifact(artifact, true), nil
}

// CancelUpload removes an incomplete upload and its metadata.
func (m *TransferManager) CancelUpload(id string) error {
	unlock := m.lockTransfer(id)
	defer unlock()
	if _, err := m.loadUpload(id); err != nil {
		return err
	}
	partErr := os.Remove(m.uploadPartPath(id))
	metaErr := os.Remove(m.uploadMetaPath(id))
	if partErr != nil && !errors.Is(partErr, os.ErrNotExist) {
		return partErr
	}
	if metaErr != nil && !errors.Is(metaErr, os.ErrNotExist) {
		return metaErr
	}
	return nil
}

// PublishFile registers an existing regular file for an opaque download.
// External files are never removed by transfer expiry.
func (m *TransferManager) PublishFile(path, name, sha string, ttl time.Duration) (TransferArtifact, error) {
	m.cleanupExpired()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return TransferArtifact{}, err
	}
	path = absolute
	info, err := os.Stat(path)
	if err != nil {
		return TransferArtifact{}, err
	}
	if !info.Mode().IsRegular() {
		return TransferArtifact{}, errors.New("只能发布普通文件")
	}
	if name == "" {
		name = filepath.Base(path)
	}
	name, err = safeFilename(name)
	if err != nil {
		return TransferArtifact{}, err
	}
	sha, err = normalizeSHA256(sha)
	if err != nil {
		return TransferArtifact{}, err
	}
	if ttl <= 0 {
		ttl = m.options.ArtifactTTL
	}
	id, err := randomTransferID()
	if err != nil {
		return TransferArtifact{}, err
	}
	now := m.now().UTC()
	record := artifactRecord{
		ID:        id,
		Name:      name,
		Path:      path,
		Size:      info.Size(),
		SHA256:    sha,
		Owned:     false,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := m.saveArtifact(record); err != nil {
		return TransferArtifact{}, err
	}
	return publicArtifact(record, false), nil
}

// ResolveArtifact resolves an opaque transfer ID for trusted in-process
// integration. The device path is never exposed for externally published
// downloads, but is returned here so a broker adapter can consume the file.
func (m *TransferManager) ResolveArtifact(id string) (TransferArtifact, error) {
	record, err := m.loadArtifact(id)
	if err != nil {
		return TransferArtifact{}, err
	}
	return publicArtifact(record, record.Owned), nil
}

func (m *TransferManager) loadUpload(id string) (uploadRecord, error) {
	if !validTransferID(id) {
		return uploadRecord{}, errTransferNotFound
	}
	data, err := os.ReadFile(m.uploadMetaPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return uploadRecord{}, errTransferNotFound
	}
	if err != nil {
		return uploadRecord{}, err
	}
	var record uploadRecord
	if err := json.Unmarshal(data, &record); err != nil || record.ID != id {
		return uploadRecord{}, errors.New("上传元数据损坏")
	}
	if m.now().After(record.UpdatedAt.Add(m.options.UploadTTL)) {
		_ = os.Remove(m.uploadPartPath(id))
		_ = os.Remove(m.uploadMetaPath(id))
		return uploadRecord{}, errTransferExpired
	}
	return record, nil
}

func (m *TransferManager) loadArtifact(id string) (artifactRecord, error) {
	if !validTransferID(id) {
		return artifactRecord{}, errTransferNotFound
	}
	data, err := os.ReadFile(m.artifactMetaPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return artifactRecord{}, errTransferNotFound
	}
	if err != nil {
		return artifactRecord{}, err
	}
	var record artifactRecord
	if err := json.Unmarshal(data, &record); err != nil || record.ID != id {
		return artifactRecord{}, errors.New("文件元数据损坏")
	}
	if m.now().After(record.ExpiresAt) {
		if record.Owned {
			_ = os.Remove(record.Path)
		}
		_ = os.Remove(m.artifactMetaPath(id))
		return artifactRecord{}, errTransferExpired
	}
	return record, nil
}

func (m *TransferManager) saveUpload(record uploadRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicfile.Write(m.uploadMetaPath(record.ID), append(data, '\n'), 0o600)
}

func (m *TransferManager) saveArtifact(record artifactRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return atomicfile.Write(m.artifactMetaPath(record.ID), append(data, '\n'), 0o600)
}

func (m *TransferManager) cleanupExpired() {
	entries, err := os.ReadDir(m.uploads)
	if err != nil {
		return
	}
	now := m.now()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validTransferID(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.uploads, name))
		if err != nil {
			continue
		}
		var record uploadRecord
		if json.Unmarshal(data, &record) == nil && now.After(record.UpdatedAt.Add(m.options.UploadTTL)) {
			_ = os.Remove(m.uploadPartPath(id))
			_ = os.Remove(m.uploadMetaPath(id))
		}
	}
	entries, err = os.ReadDir(m.artifacts)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validTransferID(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.artifacts, name))
		if err != nil {
			continue
		}
		var record artifactRecord
		if json.Unmarshal(data, &record) == nil && now.After(record.ExpiresAt) {
			if record.Owned {
				_ = os.Remove(record.Path)
			}
			_ = os.Remove(m.artifactMetaPath(id))
		}
	}
}

func (m *TransferManager) lockTransfer(id string) func() {
	m.mu.Lock()
	entry := m.locks[id]
	if entry == nil {
		entry = &transferLock{}
		m.locks[id] = entry
	}
	entry.refs++
	m.mu.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.locks, id)
		}
		m.mu.Unlock()
	}
}

func (m *TransferManager) uploadPartPath(id string) string {
	return filepath.Join(m.uploads, id+".part")
}

func (m *TransferManager) uploadMetaPath(id string) string {
	return filepath.Join(m.uploads, id+".json")
}

func (m *TransferManager) artifactMetaPath(id string) string {
	return filepath.Join(m.artifacts, id+".json")
}

func (m *TransferManager) uploadResponse(record uploadRecord) map[string]any {
	return map[string]any{
		"id":            record.ID,
		"name":          record.Name,
		"size":          record.Total,
		"received":      record.Received,
		"sha256":        record.SHA256,
		"createdAt":     record.CreatedAt,
		"updatedAt":     record.UpdatedAt,
		"expiresAt":     record.UpdatedAt.Add(m.options.UploadTTL),
		"maxChunkBytes": m.options.MaxChunkBytes,
		"uploadUrl":     "/transfer/upload/" + record.ID,
		"statusUrl":     "/transfer/upload/" + record.ID,
		"completeUrl":   "/transfer/upload/" + record.ID + "/complete",
	}
}

func publicArtifact(record artifactRecord, exposeDevicePath bool) TransferArtifact {
	artifact := TransferArtifact{
		ID:          record.ID,
		Name:        record.Name,
		Size:        record.Size,
		SHA256:      record.SHA256,
		DownloadURL: "/transfer/download/" + record.ID,
		ExpiresAt:   record.ExpiresAt,
		Path:        record.Path,
	}
	if exposeDevicePath {
		artifact.DevicePath = record.Path
	}
	return artifact
}

func transferLookupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTransferNotFound):
		writeAPIError(w, http.StatusNotFound, "TRANSFER_NOT_FOUND", err.Error())
	case errors.Is(err, errTransferExpired):
		writeAPIError(w, http.StatusGone, "TRANSFER_EXPIRED", err.Error())
	default:
		writeAPIError(w, http.StatusInternalServerError, "TRANSFER_ERROR", err.Error())
	}
}

func transferWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTransferNotFound):
		transferLookupError(w, err)
	case errors.Is(err, errTransferExpired):
		transferLookupError(w, err)
	case errors.Is(err, errTransferTooLarge):
		writeAPIError(w, http.StatusRequestEntityTooLarge, "CHUNK_TOO_LARGE", err.Error())
	case errors.Is(err, errOffsetConflict), errors.Is(err, errHashMismatch):
		writeAPIError(w, http.StatusConflict, "TRANSFER_CONFLICT", err.Error())
	default:
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "CHUNK_TOO_LARGE", err.Error())
			return
		}
		writeAPIError(w, http.StatusBadRequest, "TRANSFER_REJECTED", err.Error())
	}
}

func parseContentRange(value string) (start, end, total int64, err error) {
	const prefix = "bytes "
	if !strings.HasPrefix(value, prefix) {
		return 0, 0, 0, errors.New("缺少有效 Content-Range")
	}
	rangeAndTotal := strings.Split(strings.TrimPrefix(value, prefix), "/")
	if len(rangeAndTotal) != 2 {
		return 0, 0, 0, errors.New("Content-Range 格式错误")
	}
	bounds := strings.Split(rangeAndTotal[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, errors.New("Content-Range 格式错误")
	}
	start, err = strconv.ParseInt(bounds[0], 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("Content-Range 起点无效")
	}
	end, err = strconv.ParseInt(bounds[1], 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("Content-Range 终点无效")
	}
	total, err = strconv.ParseInt(rangeAndTotal[1], 10, 64)
	if err != nil {
		return 0, 0, 0, errors.New("Content-Range 总大小无效")
	}
	if start < 0 || end < start || total <= 0 || end >= total {
		return 0, 0, 0, errors.New("Content-Range 范围无效")
	}
	return start, end, total, nil
}

func safeFilename(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || len(value) > 255 {
		return "", errors.New("文件名无效")
	}
	if strings.ContainsAny(value, `/\`) || filepath.Base(value) != value {
		return "", errors.New("文件名不得包含路径")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("文件名包含控制字符")
		}
	}
	return value, nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("SHA-256 必须是 64 位十六进制字符串")
	}
	return value, nil
}

func randomTransferID() (string, error) {
	var value [transferIDBytes]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validTransferID(id string) bool {
	if len(id) != transferIDBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == transferIDBytes
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func contentDisposition(name string) string {
	value := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if value == "" {
		return "attachment"
	}
	return value
}

func trimPrefix(value string, prefixes ...string) (string, bool) {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix), true
		}
	}
	return "", false
}
