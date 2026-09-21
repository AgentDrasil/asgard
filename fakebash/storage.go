package fakebash

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	DefaultOutputDir = "/tmp/fakebash-outputs"
	LatestSymlink    = "latest.log"
)

var validCmdIDRegex = regexp.MustCompile(`^c-\d+-\d+-[0-9a-fA-F]+$`)

// StorageFile defines the interface for an active streaming log file.
type StorageFile interface {
	Append(payload []byte) error
	Finish(exitCode int) error
	Path() string
	ID() string
}

// Storage defines the interface for managing fakebash output logs.
type Storage interface {
	CreateFile(cmd string) (StorageFile, error)
	GetPath(cmdID string) (string, error)
	Read(cmdID string) ([]byte, error)
	ReadRange(cmdID string, offset, limit int64) ([]byte, error)
	GetHeadAndTail(cmdID string, headBytes, tailBytes int) (head, tail []byte, total int64, lines int, err error)
}

// diskStorage implements the Storage interface.
type diskStorage struct {
	baseDir string
	mu      sync.Mutex
}

// NewStorage creates a new Storage instance with the given directory.
// If baseDir is empty, it checks FAKEBASH_OUTPUT_DIR, falling back to DefaultOutputDir.
func NewStorage(baseDir string) (Storage, error) {
	if baseDir == "" {
		baseDir = os.Getenv("FAKEBASH_OUTPUT_DIR")
	}
	if baseDir == "" {
		baseDir = DefaultOutputDir
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory %s: %w", baseDir, err)
	}

	return &diskStorage{
		baseDir: baseDir,
	}, nil
}

// GenerateCmdID creates a unique command ID in format: c-<unix-ms>-<pid>-<rand4>
func GenerateCmdID() string {
	nowMs := time.Now().UnixMilli()
	pid := os.Getpid()
	randBytes := make([]byte, 2)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback to timestamp low bits if rand fails
		return fmt.Sprintf("c-%d-%d-%04x", nowMs, pid, nowMs&0xffff)
	}
	return fmt.Sprintf("c-%d-%d-%s", nowMs, pid, hex.EncodeToString(randBytes))
}

type storageFile struct {
	storage  *diskStorage
	id       string
	path     string
	file     *os.File
	mu       sync.Mutex
	finished bool
}

func (s *diskStorage) CreateFile(cmd string) (StorageFile, error) {
	cmdID := GenerateCmdID()
	filePath := filepath.Join(s.baseDir, cmdID+".log")

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file %s: %w", filePath, err)
	}

	header := fmt.Sprintf("# CMD: %s | TIME: %s\n", cmd, time.Now().Format(time.RFC3339))
	if _, err := f.WriteString(header); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to write header to %s: %w", filePath, err)
	}

	return &storageFile{
		storage: s,
		id:      cmdID,
		path:    filePath,
		file:    f,
	}, nil
}

func (sf *storageFile) Append(payload []byte) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	if sf.finished {
		return errors.New("cannot append to finished storage file")
	}

	_, err := sf.file.Write(payload)
	return err
}

func (sf *storageFile) Finish(exitCode int) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	if sf.finished {
		return nil
	}
	sf.finished = true

	footer := fmt.Sprintf("\n# EXIT: %d\n", exitCode)
	_, _ = sf.file.WriteString(footer)
	_ = sf.file.Sync()
	_ = sf.file.Close()

	// Atomically update latest.log symlink
	return sf.storage.atomicUpdateLatest(sf.id + ".log")
}

func (sf *storageFile) Path() string {
	return sf.path
}

func (sf *storageFile) ID() string {
	return sf.id
}

func (s *diskStorage) atomicUpdateLatest(targetFilename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tmpSymlink := filepath.Join(s.baseDir, fmt.Sprintf(".latest.%s.tmp", targetFilename))
	latestPath := filepath.Join(s.baseDir, LatestSymlink)

	_ = os.Remove(tmpSymlink)
	if err := os.Symlink(targetFilename, tmpSymlink); err != nil {
		return fmt.Errorf("failed to create temp symlink: %w", err)
	}

	if err := os.Rename(tmpSymlink, latestPath); err != nil {
		_ = os.Remove(tmpSymlink)
		return fmt.Errorf("failed to atomically update latest symlink: %w", err)
	}

	return nil
}

func (s *diskStorage) GetPath(cmdID string) (string, error) {
	trimmed := strings.TrimSpace(cmdID)
	if trimmed == "" || trimmed == "latest" {
		latestPath := filepath.Join(s.baseDir, LatestSymlink)
		resolved, err := filepath.EvalSymlinks(latestPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve latest log: %w", err)
		}
		return resolved, nil
	}

	if !validCmdIDRegex.MatchString(trimmed) {
		return "", fmt.Errorf("invalid command ID format: %s", trimmed)
	}

	targetPath := filepath.Join(s.baseDir, trimmed+".log")
	// Double check path traversal safety
	cleanTarget := filepath.Clean(targetPath)
	cleanBase := filepath.Clean(s.baseDir)
	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path traversal attempt: %s", cmdID)
	}

	if _, err := os.Stat(cleanTarget); err != nil {
		return "", fmt.Errorf("log file not found for ID %s: %w", trimmed, err)
	}

	return cleanTarget, nil
}

func (s *diskStorage) Read(cmdID string) ([]byte, error) {
	filePath, err := s.GetPath(cmdID)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filePath)
}

func (s *diskStorage) ReadRange(cmdID string, offset, limit int64) ([]byte, error) {
	filePath, err := s.GetPath(cmdID)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	fileSize := info.Size()
	if offset < 0 {
		offset = 0
	}
	if offset >= fileSize {
		return []byte{}, nil
	}

	if limit <= 0 || offset+limit > fileSize {
		limit = fileSize - offset
	}

	buf := make([]byte, limit)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

func (s *diskStorage) GetHeadAndTail(cmdID string, headBytes, tailBytes int) (head, tail []byte, total int64, lines int, err error) {
	filePath, err := s.GetPath(cmdID)
	if err != nil {
		return nil, nil, 0, 0, err
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, nil, 0, 0, err
	}
	total = info.Size()

	if headBytes <= 0 {
		headBytes = 1024
	}
	if tailBytes <= 0 {
		tailBytes = 1024
	}

	// Count newlines by streaming through the file with 64KB buffer
	buf := make([]byte, 64*1024)
	lines = 0
	for {
		n, rErr := f.Read(buf)
		if n > 0 {
			for i := 0; i < n; i++ {
				if buf[i] == '\n' {
					lines++
				}
			}
		}
		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			return nil, nil, 0, 0, rErr
		}
	}

	if total == 0 {
		return []byte{}, []byte{}, 0, lines, nil
	}

	// Read Head
	actualHead := int64(headBytes)
	if actualHead > total {
		actualHead = total
	}
	head = make([]byte, actualHead)
	if _, err := f.ReadAt(head, 0); err != nil && err != io.EOF {
		return nil, nil, 0, 0, err
	}

	// Read Tail
	actualTail := int64(tailBytes)
	if actualTail > total {
		actualTail = total
	}
	tail = make([]byte, actualTail)
	offset := total - actualTail
	if _, err := f.ReadAt(tail, offset); err != nil && err != io.EOF {
		return nil, nil, 0, 0, err
	}

	return head, tail, total, lines, nil
}
