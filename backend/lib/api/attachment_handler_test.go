package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func createMultipartRequest(t *testing.T, targetURL string, files map[string][]byte) (*http.Request, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for filename, content := range files {
		part, err := writer.CreateFormFile("files", filename)
		require.NoError(t, err)
		_, err = io.Copy(part, bytes.NewReader(content))
		require.NoError(t, err)
	}

	err := writer.Close()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, targetURL, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, writer.FormDataContentType()
}

func setupAttachmentTestServer(t *testing.T) (*Server, *dbmodels.SessionRepository) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	err := dbmodels.AutoMigrate(testDB)
	require.NoError(t, err)

	repo := dbmodels.NewSessionRepository(testDB)
	conf := &config.Config{
		Host: "http://localhost:8080",
	}

	server := &Server{
		conf: conf,
		repo: repo,
	}
	server.mux = server.buildMuxLocked()
	return server, repo
}

func TestSessionAttachmentsUpload_Success(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-upload-test-1"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Test Chat",
	})
	require.NoError(t, err)

	files := map[string][]byte{
		"sample.png": []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRtestcontent"),
		"doc.txt":    []byte("Hello world attachment content"),
	}

	req, _ := createMultipartRequest(t, fmt.Sprintf("/api/sessions/%s/attachments", chatID), files)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var attachments []dbmodels.Attachment
	err = json.Unmarshal(rr.Body.Bytes(), &attachments)
	require.NoError(t, err)
	assert.Len(t, attachments, 2)

	for _, att := range attachments {
		assert.NotEmpty(t, att.Name)
		assert.Equal(t, "/tmp/attachments/"+att.Name, att.Path)
		assert.Greater(t, att.Size, int64(0))

		savedPath := filepath.Join(tempHome, "tmp", chatID, "attachments", att.Name)
		info, statErr := os.Stat(savedPath)
		require.NoError(t, statErr)
		assert.Equal(t, att.Size, info.Size())

		if att.Name == "sample.png" {
			assert.Equal(t, "image/png", att.MimeType)
		}
	}
}

func TestSessionAttachmentsUpload_Sanitization(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-sanitize-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Sanitize Chat",
	})
	require.NoError(t, err)

	files := map[string][]byte{
		"../../etc/passwd":  []byte("root:x:0:0:root:/root:/bin/bash"),
		"..\\..\\win.ini":   []byte("[fonts]"),
		"subdir/nested.txt": []byte("nested content"),
	}

	req, _ := createMultipartRequest(t, fmt.Sprintf("/api/sessions/%s/attachments", chatID), files)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var attachments []dbmodels.Attachment
	err = json.Unmarshal(rr.Body.Bytes(), &attachments)
	require.NoError(t, err)
	assert.Len(t, attachments, 3)

	storageDir := filepath.Join(tempHome, "tmp", chatID, "attachments")
	for _, att := range attachments {
		assert.False(t, strings.Contains(att.Name, "/"))
		assert.False(t, strings.Contains(att.Name, "\\"))
		assert.False(t, strings.Contains(att.Name, ".."))

		savedPath := filepath.Join(storageDir, att.Name)
		assert.FileExists(t, savedPath)
	}

	// Verify no files escaped to parent directories
	assert.NoFileExists(t, filepath.Join(tempHome, "tmp", "etc", "passwd"))
	assert.NoFileExists(t, filepath.Join(tempHome, "tmp", "passwd"))
}

func TestSessionAttachmentsUpload_AtomicDeduplication(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-dedupe-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Dedupe Chat",
	})
	require.NoError(t, err)

	// First upload
	req1, _ := createMultipartRequest(t, fmt.Sprintf("/api/sessions/%s/attachments", chatID), map[string][]byte{
		"report.pdf": []byte("%PDF-1.4 first upload"),
	})
	rr1 := httptest.NewRecorder()
	server.ServeHTTP(rr1, req1)
	assert.Equal(t, http.StatusOK, rr1.Code)

	var atts1 []dbmodels.Attachment
	err = json.Unmarshal(rr1.Body.Bytes(), &atts1)
	require.NoError(t, err)
	require.Len(t, atts1, 1)
	assert.Equal(t, "report.pdf", atts1[0].Name)
	assert.Equal(t, "/tmp/attachments/report.pdf", atts1[0].Path)

	// Second upload with same filename
	req2, _ := createMultipartRequest(t, fmt.Sprintf("/api/sessions/%s/attachments", chatID), map[string][]byte{
		"report.pdf": []byte("%PDF-1.4 second upload"),
	})
	rr2 := httptest.NewRecorder()
	server.ServeHTTP(rr2, req2)
	assert.Equal(t, http.StatusOK, rr2.Code)

	var atts2 []dbmodels.Attachment
	err = json.Unmarshal(rr2.Body.Bytes(), &atts2)
	require.NoError(t, err)
	require.Len(t, atts2, 1)
	assert.Equal(t, "report-1.pdf", atts2[0].Name)
	assert.Equal(t, "/tmp/attachments/report-1.pdf", atts2[0].Path)

	// Verify both files exist with distinct contents
	storageDir := filepath.Join(tempHome, "tmp", chatID, "attachments")
	c1, err := os.ReadFile(filepath.Join(storageDir, "report.pdf"))
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1.4 first upload"), c1)

	c2, err := os.ReadFile(filepath.Join(storageDir, "report-1.pdf"))
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-1.4 second upload"), c2)
}

func TestSessionAttachmentsUpload_FileTooLarge(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-large-file-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Large File Chat",
	})
	require.NoError(t, err)

	// Create multipart with fake large size in header (e.g. 21MB) or actual content
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="files"; filename="large.bin"`)
	h.Set("Content-Type", "application/octet-stream")
	part, err := writer.CreatePart(h)
	require.NoError(t, err)

	// Write 20MB + 10 bytes
	largeData := make([]byte, 20*1024*1024+10)
	_, err = part.Write(largeData)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/sessions/%s/attachments", chatID), &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)

	var res map[string]string
	err = json.Unmarshal(rr.Body.Bytes(), &res)
	require.NoError(t, err)
	assert.Contains(t, res["error"], "20MB")
}

func TestSessionAttachmentsUpload_BodyTooLarge(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-large-body-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Large Body Chat",
	})
	require.NoError(t, err)

	// 51MB of data stream simulating multi-part exceeding 50MB MaxBytesReader limit
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer func() { _ = pw.Close() }()
		defer func() { _ = writer.Close() }()
		part, err := writer.CreateFormFile("files", "chunk1.bin")
		if err != nil {
			return
		}
		// Write 18MB
		chunk := make([]byte, 1024*1024)
		for i := 0; i < 18; i++ {
			if _, err := part.Write(chunk); err != nil {
				return
			}
		}
		part2, err := writer.CreateFormFile("files", "chunk2.bin")
		if err != nil {
			return
		}
		// Write 18MB
		for i := 0; i < 18; i++ {
			if _, err := part2.Write(chunk); err != nil {
				return
			}
		}
		part3, err := writer.CreateFormFile("files", "chunk3.bin")
		if err != nil {
			return
		}
		// Write 18MB (total 54MB > 50MB)
		for i := 0; i < 18; i++ {
			if _, err := part3.Write(chunk); err != nil {
				return
			}
		}
	}()

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/sessions/%s/attachments", chatID), pr)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

func TestSessionAttachmentsUpload_InvalidChatID(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, _ := setupAttachmentTestServer(t)

	req, _ := createMultipartRequest(t, "/api/sessions/invalid%20id%20with%20spaces/attachments", map[string][]byte{
		"test.txt": []byte("content"),
	})
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSessionAttachmentsUpload_SessionNotFound(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, _ := setupAttachmentTestServer(t)

	req, _ := createMultipartRequest(t, "/api/sessions/nonexistent-session-id/attachments", map[string][]byte{
		"test.txt": []byte("content"),
	})
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSessionAttachmentDownload_Success(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-download-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Download Chat",
	})
	require.NoError(t, err)

	storageDir := filepath.Join(tempHome, "tmp", chatID, "attachments")
	require.NoError(t, os.MkdirAll(storageDir, 0755))

	content := []byte("plain text attachment content for download")
	err = os.WriteFile(filepath.Join(storageDir, "notes.txt"), content, 0600)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/attachments/notes.txt", chatID), nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "default-src 'none'; sandbox", rr.Header().Get("Content-Security-Policy"))
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, content, rr.Body.Bytes())
}

func TestSessionAttachmentDownload_PathTraversalForbidden(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-traversal-dl-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "Traversal DL Chat",
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		filename string
	}{
		{"percent encoded dot dot", "%2e%2e"},
		{"nested dot dot", "..%2F..%2Fetc%2Fpasswd"},
		{"slash path", "sub%2Ffile.txt"},
		{"backslash path", "sub%5Cfile.txt"},
		{"double dot literal in url", ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/attachments/%s", chatID, tt.filename), nil)
			rr := httptest.NewRecorder()
			server.ServeHTTP(rr, req)

			// Traversal attempts must either be redirected by ServeMux URL cleaner or rejected with 400 Bad Request
			assert.True(t, rr.Code == http.StatusBadRequest || rr.Code == http.StatusNotFound || rr.Code == http.StatusTemporaryRedirect || rr.Code == http.StatusMovedPermanently, "expected traversal attempt to be rejected or sanitized, got: %d", rr.Code)
		})
	}
}

func TestSessionAttachmentDownload_NotFound(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	server, repo := setupAttachmentTestServer(t)

	chatID := "chat-dl-404-test"
	err := repo.SaveSession(&dbmodels.Session{
		ChatID: chatID,
		Title:  "404 DL Chat",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/%s/attachments/nonexistent.txt", chatID), nil)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}
