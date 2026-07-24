package ttyd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

// Instance manages a single ttyd process bound to a Unix domain socket.
type Instance struct {
	SessionID  string
	SocketPath string
	Cmd        *exec.Cmd
	CreatedAt  time.Time
	Proxy      *httputil.ReverseProxy
}

// Manager manages ttyd instances indexed by session ID.
type Manager struct {
	mu        sync.RWMutex
	instances map[string]*Instance
	sockDir   string
}

// NewManager creates a ttyd session manager.
func NewManager(sockDir string) (*Manager, error) {
	if sockDir == "" {
		sockDir = filepath.Join(os.TempDir(), "asgard-ttyd-sockets")
	}
	if err := os.MkdirAll(sockDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create ttyd socket directory: %w", err)
	}
	return &Manager{
		instances: make(map[string]*Instance),
		sockDir:   sockDir,
	}, nil
}

// GetOrStart returns an existing active ttyd instance or starts a new one.
func (m *Manager) GetOrStart(sessionID string, workingDir string) (*Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inst, exists := m.instances[sessionID]; exists {
		// Verify process is still alive
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			if err := inst.Cmd.Process.Signal(syscall.Signal(0)); err == nil {
				return inst, nil
			}
		}
		// Clean up dead instance
		m.cleanupLocked(sessionID)
	}

	// Determine shell (prefer fish if installed, fallback to bash)
	shell := "bash"
	if path, err := exec.LookPath("fish"); err == nil {
		shell = path
	} else if path, err := exec.LookPath("bash"); err == nil {
		shell = path
	}

	socketPath := filepath.Join(m.sockDir, fmt.Sprintf("%s.sock", sessionID))
	_ = os.Remove(socketPath) // Clean stale socket if any

	// ttyd -i <sock> -o --once <shell>
	args := []string{"-i", socketPath, "-o", shell}
	cmd := exec.Command("ttyd", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ttyd process: %w", err)
	}

	// Wait up to 3 seconds for UNIX socket file to be created by ttyd
	ready := false
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(socketPath); err == nil {
			ready = true
			break
		}
	}
	if !ready {
		_ = cmd.Process.Kill()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("ttyd failed to create socket within timeout at %s", socketPath)
	}

	// Create reverse proxy targeting unix socket
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "unix"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	inst := &Instance{
		SessionID:  sessionID,
		SocketPath: socketPath,
		Cmd:        cmd,
		CreatedAt:  time.Now(),
		Proxy:      proxy,
	}

	m.instances[sessionID] = inst

	// Monitor process termination asynchronously for cleanup
	go func() {
		err := cmd.Wait()
		log.Info().Err(err).Str("session_id", sessionID).Msg("ttyd process exited")
		m.mu.Lock()
		m.cleanupLocked(sessionID)
		m.mu.Unlock()
	}()

	return inst, nil
}

func (m *Manager) cleanupLocked(sessionID string) {
	if inst, ok := m.instances[sessionID]; ok {
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			_ = inst.Cmd.Process.Kill()
		}
		_ = os.Remove(inst.SocketPath)
		delete(m.instances, sessionID)
	}
}

// Close cleans up all active ttyd sessions and socket files.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sessionID := range m.instances {
		m.cleanupLocked(sessionID)
	}
}
