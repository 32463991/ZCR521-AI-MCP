package frontend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTransferManagerForTest(t *testing.T) *TransferManager {
	t.Helper()
	manager, err := NewTransferManager(TransferOptions{
		Directory:      t.TempDir(),
		MaxUploadBytes: 1 << 20,
		MaxChunkBytes:  5,
		UploadTTL:      time.Hour,
		ArtifactTTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestResumableUploadPersistsOffsetAndPublishes(t *testing.T) {
	manager := newTransferManagerForTest(t)
	content := []byte("hello world")
	sum := sha256.Sum256(content)
	record, err := manager.CreateUpload("greeting.txt", int64(len(content)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	record, err = manager.WriteChunk(record.ID, "bytes 0-4/11", bytes.NewReader(content[:5]))
	if err != nil {
		t.Fatal(err)
	}
	if record.Received != 5 {
		t.Fatalf("received = %d, want 5", record.Received)
	}
	if _, err := manager.WriteChunk(record.ID, "bytes 7-10/11", bytes.NewReader(content[7:])); !errors.Is(err, errOffsetConflict) {
		t.Fatalf("out-of-order error = %v, want offset conflict", err)
	}

	// Recreate the manager to prove the resume offset is on disk, not just in
	// process memory.
	resumed, err := NewTransferManager(manager.options)
	if err != nil {
		t.Fatal(err)
	}
	status, err := resumed.UploadStatus(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Received != 5 {
		t.Fatalf("durable offset = %d, want 5", status.Received)
	}
	if _, err := resumed.WriteChunk(record.ID, "bytes 5-9/11", bytes.NewReader(content[5:10])); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.WriteChunk(record.ID, "bytes 10-10/11", bytes.NewReader(content[10:])); err != nil {
		t.Fatal(err)
	}
	artifact, err := resumed.CompleteUpload(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifact.DownloadURL, "/transfer/download/") {
		t.Fatalf("noncanonical download URL %q", artifact.DownloadURL)
	}
	if artifact.DevicePath == "" {
		t.Fatal("completed upload did not expose its temporary device path")
	}
	got, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("published data = %q, want %q", got, content)
	}
}

func TestUploadExpiryRemovesPartialFile(t *testing.T) {
	manager := newTransferManagerForTest(t)
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	record, err := manager.CreateUpload("x.bin", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	part := manager.uploadPartPath(record.ID)
	now = now.Add(manager.options.UploadTTL + time.Second)
	if _, err := manager.UploadStatus(record.ID); !errors.Is(err, errTransferExpired) {
		t.Fatalf("status error = %v, want expired", err)
	}
	if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file still exists: %v", err)
	}
}

func TestTransferHTTPUsesCanonicalPathsAndRangeDownload(t *testing.T) {
	options := testOptions(t)
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}

	createBody := `{"name":"range.txt","size":11,"sha256":""}`
	create := localRequest(http.MethodPost, "http://127.0.0.1:5322/transfer/upload", strings.NewReader(createBody))
	create.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createResponse.Code, createResponse.Body.String())
	}
	var upload map[string]any
	if err := json.Unmarshal(createResponse.Body.Bytes(), &upload); err != nil {
		t.Fatal(err)
	}
	id, _ := upload["id"].(string)
	if upload["uploadUrl"] != "/transfer/upload/"+id {
		t.Fatalf("uploadUrl = %#v", upload["uploadUrl"])
	}
	chunks := []struct {
		rangeHeader string
		data        string
	}{
		{"bytes 0-4/11", "hello"},
		{"bytes 5-9/11", " worl"},
		{"bytes 10-10/11", "d"},
	}
	for _, chunk := range chunks {
		request := localRequest(http.MethodPut, "http://127.0.0.1:5322/transfer/upload/"+id, strings.NewReader(chunk.data))
		request.Header.Set("Content-Range", chunk.rangeHeader)
		request.ContentLength = int64(len(chunk.data))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("chunk %s status = %d: %s", chunk.rangeHeader, response.Code, response.Body.String())
		}
	}
	complete := localRequest(http.MethodPost, "http://127.0.0.1:5322/transfer/upload/"+id+"/complete", nil)
	completeResponse := httptest.NewRecorder()
	server.ServeHTTP(completeResponse, complete)
	if completeResponse.Code != http.StatusCreated {
		t.Fatalf("complete status = %d: %s", completeResponse.Code, completeResponse.Body.String())
	}
	var artifact TransferArtifact
	if err := json.Unmarshal(completeResponse.Body.Bytes(), &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.DownloadURL != "/transfer/download/"+id {
		t.Fatalf("download URL = %q", artifact.DownloadURL)
	}
	download := localRequest(http.MethodGet, "http://127.0.0.1:5322"+artifact.DownloadURL, nil)
	download.Header.Set("Range", "bytes=6-10")
	downloadResponse := httptest.NewRecorder()
	server.ServeHTTP(downloadResponse, download)
	if downloadResponse.Code != http.StatusPartialContent {
		t.Fatalf("download status = %d: %s", downloadResponse.Code, downloadResponse.Body.String())
	}
	if got := downloadResponse.Body.String(); got != "world" {
		t.Fatalf("range body = %q", got)
	}
	if downloadResponse.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatal("missing Accept-Ranges: bytes")
	}
}

func TestPublishFileStreamsWithoutExposingPath(t *testing.T) {
	options := testOptions(t)
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := server.PublishFile(path, "result.txt", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(path)) {
		t.Fatalf("artifact leaked source path: %s", encoded)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, localRequest(http.MethodGet, "http://127.0.0.1:5322"+artifact.DownloadURL, nil))
	if response.Code != http.StatusOK || response.Body.String() != "secret" {
		t.Fatalf("download = %d %q", response.Code, response.Body.String())
	}
}

func TestChunkRejectsExtraBytesAndRollsBack(t *testing.T) {
	manager := newTransferManagerForTest(t)
	record, err := manager.CreateUpload("x.bin", 5, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.WriteChunk(record.ID, "bytes 0-4/5", strings.NewReader("123456"))
	if err == nil {
		t.Fatal("extra byte was accepted")
	}
	status, err := manager.UploadStatus(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Received != 0 {
		t.Fatalf("received = %d after rollback", status.Received)
	}
	file, err := os.Open(manager.uploadPartPath(record.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("partial data survived rollback: %q", data)
	}
}
