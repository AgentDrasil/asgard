package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
)

const (
	maxAttachmentSingleFileSize = runnerfs.MaxAttachmentSize // 20MB
	maxAttachmentTotalBodySize  = 50 * 1024 * 1024           // 50MB
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

	fs, ferr := s.chatFS(sessionID)
	if ferr != nil {
		writeFSError(w, ferr)
		return
	}

	savedAttachments := make([]dbmodels.Attachment, 0, len(allHeaders))

	for _, fh := range allHeaders {
		safeName := sanitizeAttachmentFilename(fh.Filename)
		ext := filepath.Ext(safeName)

		src, openSrcErr := fh.Open()
		if openSrcErr != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to open uploaded file: "+openSrcErr.Error())
			return
		}

		sampleBuf := make([]byte, 512)
		nSample, readSampleErr := io.ReadFull(src, sampleBuf)
		if readSampleErr != nil && !errors.Is(readSampleErr, io.EOF) && !errors.Is(readSampleErr, io.ErrUnexpectedEOF) {
			_ = src.Close()
			writeJSONError(w, http.StatusInternalServerError, "failed to read uploaded file: "+readSampleErr.Error())
			return
		}
		sample := sampleBuf[:nSample]

		// The runner stores the file under a free name; the request id makes a
		// retried upload attach to the original instead of storing it twice.
		res, putErr := fs.PutAttachment(r.Context(), newRequestID(), safeName, io.MultiReader(bytes.NewReader(sample), src))
		_ = src.Close()
		if putErr != nil {
			writeFSError(w, putErr)
			return
		}

		savedAttachments = append(savedAttachments, dbmodels.Attachment{
			Name:     res.Name,
			Path:     "/tmp/attachments/" + res.Name,
			Size:     res.Size,
			MimeType: detectMimeType(ext, sample),
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
	if !runnerfs.ValidAttachmentName(filename) {
		writeJSONError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	fs, ferr := s.chatFS(sessionID)
	if ferr != nil {
		writeFSError(w, ferr)
		return
	}
	f, err := fs.OpenAttachment(r.Context(), filename)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer func() { _ = f.Close() }()

	mimeType := detectMimeType(filepath.Ext(filename), nil)

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	http.ServeContent(w, r, filename, f.ModTime, f)
}

// newRequestID returns a random id for idempotent runner RPCs.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
