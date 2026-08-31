package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

const (
	maxAttachmentSingleFileSize = 20 * 1024 * 1024 // 20MB
	maxAttachmentTotalBodySize  = 50 * 1024 * 1024 // 50MB
	maxAttachmentDedupeRetries  = 100
)

// sanitizeAttachmentFilename cleans an uploaded filename, stripping paths and control characters.
func sanitizeAttachmentFilename(name string) string {
	// Replace Windows separators to ensure filepath.Base works across platforms
	name = strings.ReplaceAll(name, "\\", "/")
	base := filepath.Base(name)
	base = strings.TrimSpace(base)

	var sb strings.Builder
	for _, r := range base {
		if r >= 32 && r != 127 && r != '/' && r != '\\' {
			sb.WriteRune(r)
		}
	}
	cleaned := strings.TrimSpace(sb.String())
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		cleaned = "attachment"
	}
	return cleaned
}

// handleSessionAttachmentsUpload handles POST /api/sessions/{id}/attachments
func (s *Server) handleSessionAttachmentsUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.PathValue("id")
	if sessionID == "" || !IsValidChatID(sessionID) {
		writeJSONError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	if s.repo != nil {
		sess, err := s.repo.GetSession(sessionID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to query session: "+err.Error())
			return
		}
		if sess == nil {
			writeJSONError(w, http.StatusNotFound, "session not found")
			return
		}
	}

	// Enforce 50MB total body size
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentTotalBodySize)
	if err := r.ParseMultipartForm(maxAttachmentTotalBodySize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(err.Error(), "request body too large") {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body exceeds 50MB limit")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	var allHeaders []*multipart.FileHeader
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		for _, headers := range r.MultipartForm.File {
			allHeaders = append(allHeaders, headers...)
		}
	}

	if len(allHeaders) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	// Pre-check file sizes from headers
	for _, fh := range allHeaders {
		if fh.Size > maxAttachmentSingleFileSize {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "file size exceeds 20MB limit")
			return
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve storage directory")
		return
	}

	storageDir := filepath.Join(home, "tmp", sessionID, "attachments")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create storage directory: "+err.Error())
		return
	}

	savedAttachments := make([]dbmodels.Attachment, 0, len(allHeaders))

	for _, fh := range allHeaders {
		safeName := sanitizeAttachmentFilename(fh.Filename)
		ext := filepath.Ext(safeName)
		stem := strings.TrimSuffix(safeName, ext)

		var finalFile *os.File
		var finalName string
		var finalPath string

		for attempt := 0; attempt < maxAttachmentDedupeRetries; attempt++ {
			candidate := safeName
			if attempt > 0 {
				candidate = fmt.Sprintf("%s-%d%s", stem, attempt, ext)
			}
			candidatePath := filepath.Join(storageDir, candidate)
			f, openErr := os.OpenFile(candidatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if openErr == nil {
				finalFile = f
				finalName = candidate
				finalPath = candidatePath
				break
			}
			if os.IsExist(openErr) {
				continue
			}
			writeJSONError(w, http.StatusInternalServerError, "failed to create attachment file: "+openErr.Error())
			return
		}

		if finalFile == nil {
			writeJSONError(w, http.StatusInternalServerError, "too many filename collisions")
			return
		}

		src, openSrcErr := fh.Open()
		if openSrcErr != nil {
			_ = finalFile.Close()
			_ = os.Remove(finalPath)
			writeJSONError(w, http.StatusInternalServerError, "failed to open uploaded file: "+openSrcErr.Error())
			return
		}

		sampleBuf := make([]byte, 512)
		nSample, readSampleErr := io.ReadFull(src, sampleBuf)
		if readSampleErr != nil && !errors.Is(readSampleErr, io.EOF) && !errors.Is(readSampleErr, io.ErrUnexpectedEOF) {
			_ = src.Close()
			_ = finalFile.Close()
			_ = os.Remove(finalPath)
			writeJSONError(w, http.StatusInternalServerError, "failed to read uploaded file: "+readSampleErr.Error())
			return
		}

		sample := sampleBuf[:nSample]
		if nSample > 0 {
			if _, writeErr := finalFile.Write(sample); writeErr != nil {
				_ = src.Close()
				_ = finalFile.Close()
				_ = os.Remove(finalPath)
				writeJSONError(w, http.StatusInternalServerError, "failed to write file header: "+writeErr.Error())
				return
			}
		}

		remainingLimit := int64(maxAttachmentSingleFileSize - nSample + 1)
		limitedReader := io.LimitReader(src, remainingLimit)
		copied, copyErr := io.Copy(finalFile, limitedReader)
		_ = src.Close()
		_ = finalFile.Close()

		totalWritten := int64(nSample) + copied
		if totalWritten > maxAttachmentSingleFileSize {
			_ = os.Remove(finalPath)
			writeJSONError(w, http.StatusRequestEntityTooLarge, "file size exceeds 20MB limit")
			return
		}

		if copyErr != nil {
			_ = os.Remove(finalPath)
			writeJSONError(w, http.StatusInternalServerError, "failed to save attachment: "+copyErr.Error())
			return
		}

		mimeType := detectMimeType(ext, sample)

		savedAttachments = append(savedAttachments, dbmodels.Attachment{
			Name:     finalName,
			Path:     "/tmp/attachments/" + finalName,
			Size:     totalWritten,
			MimeType: mimeType,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(savedAttachments)
}

// handleSessionAttachmentDownload handles GET /api/sessions/{id}/attachments/{filename}
func (s *Server) handleSessionAttachmentDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.PathValue("id")
	if sessionID == "" || !IsValidChatID(sessionID) {
		writeJSONError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	filename := r.PathValue("filename")
	if filename == "" || filepath.Base(filename) != filename || strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.Contains(filename, "..") {
		writeJSONError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve storage directory")
		return
	}

	filePath := filepath.Join(home, "tmp", sessionID, "attachments", filename)
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "attachment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to stat attachment: "+err.Error())
		return
	}

	if info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "requested path is a directory")
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to open attachment: "+err.Error())
		return
	}
	defer func() { _ = file.Close() }()

	ext := filepath.Ext(filename)
	mimeType := detectMimeType(ext, nil)

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	http.ServeContent(w, r, filename, info.ModTime(), file)
}
