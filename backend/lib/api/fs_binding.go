package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// WithRunnerFS replaces how a chat's directories are reached (tests).
func WithRunnerFS(resolve func(chatID string) (runnerfs.FS, error)) ServerOption {
	return func(s *Server) { s.fsResolver = resolve }
}

// chatFS returns the file access surface of a chat: its runner's RunnerService
// when runners are configured, this host's own layout otherwise (development
// and tests without a runner).
func (s *Server) chatFS(chatID string) (runnerfs.FS, error) {
	if s.fsResolver != nil {
		return s.fsResolver(chatID)
	}
	if s.runnerClient == nil {
		return runnerfs.NewLocal(chatID), nil
	}
	node, err := s.chatRunner(chatID)
	if err != nil {
		return nil, runnerfs.Errf(runnerfs.CodeBadRequest, "%v", err)
	}
	client, err := s.nodeClient(node)
	if err != nil {
		return nil, err
	}
	return runnerfs.NewRemote(client, chatID), nil
}

// runnerClientFunc returns the RunnerService client of an online runner.
type runnerClientFunc func(nodeID string) (runnerpb.RunnerServiceClient, error)

// fsErrorStatus maps a runnerfs error onto the HTTP status and message.
func fsErrorStatus(err error) (int, string) {
	status := http.StatusInternalServerError
	switch runnerfs.CodeOf(err) {
	case runnerfs.CodeNotFound:
		status = http.StatusNotFound
	case runnerfs.CodeDenied:
		status = http.StatusForbidden
	case runnerfs.CodeBadRequest, runnerfs.CodeIsDir:
		status = http.StatusBadRequest
	case runnerfs.CodeTooLarge:
		status = http.StatusRequestEntityTooLarge
	case runnerfs.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	var fe *runnerfs.Error
	msg := err.Error()
	if !errors.As(err, &fe) {
		msg = fmt.Sprintf("file operation failed: %v", err)
	}
	return status, msg
}

// writeFSError answers the HTTP status matching a runnerfs error.
func writeFSError(w http.ResponseWriter, err error) {
	status, msg := fsErrorStatus(err)
	writeJSONError(w, status, msg)
}

// WithRunnerClient resolves runner nodes to their RunnerService clients
// without a runner hub (tests, embedding).
func WithRunnerClient(fn func(nodeID string) (runnerpb.RunnerServiceClient, error)) ServerOption {
	return func(s *Server) { s.runnerClient = fn }
}

// nodeClient returns the RunnerService client of an online runner, or a
// CodeUnavailable error (503) when it is offline.
func (s *Server) nodeClient(node string) (runnerpb.RunnerServiceClient, error) {
	client, err := s.runnerClient(node)
	if err != nil {
		return nil, runnerfs.Errf(runnerfs.CodeUnavailable, "runner %q is offline; try again when it reconnects", node)
	}
	return client, nil
}

// requestRunner picks the runner of a request that names no chat: the runner
// query parameter, or the only known runner. It returns a CodeBadRequest error
// when it stays ambiguous.
func (s *Server) requestRunner(r *http.Request) (string, error) {
	if id := r.URL.Query().Get("runner"); id != "" {
		return id, nil
	}
	if s.runners != nil {
		var only string
		n := 0
		for _, node := range s.runners.Nodes() {
			only = node.NodeID
			n++
		}
		if n == 1 {
			return only, nil
		}
	}
	return "", runnerfs.Errf(runnerfs.CodeBadRequest, "runner is required")
}

// runnerOnlyFS is the FS of requests that name a runner but no chat.
func (s *Server) runnerOnlyFS(r *http.Request) (runnerfs.FS, error) {
	if s.fsResolver != nil {
		return s.fsResolver("")
	}
	if s.runnerClient == nil {
		return runnerfs.NewLocal(""), nil
	}
	node, err := s.requestRunner(r)
	if err != nil {
		return nil, err
	}
	client, err := s.nodeClient(node)
	if err != nil {
		return nil, err
	}
	return runnerfs.NewRemote(client, ""), nil
}

// checkWorkspaceRunDir is the backend's defense-in-depth check of a stored
// session run dir before a workspace-scoped request is handed to a runner
// (which enforces its own allowlist). With runners configured the run dir
// must be non-empty (400 otherwise; there is no silent fallback), and after
// normalization absolute, free of ".." components, and inside the chat's
// runner-side tmp/session dirs or one of the runner agents' (and workflows')
// run_dirs and mount_dirs. Without runners nothing is restricted. It returns
// the HTTP status to answer (0 = acceptable) and a message.
func (s *Server) checkWorkspaceRunDir(chatID, runDir string) (int, string) {
	if s.runnerClient == nil {
		return 0, ""
	}
	if runDir == "" {
		return http.StatusBadRequest, "session has no run dir for a workspace path"
	}
	for _, c := range strings.Split(filepath.ToSlash(runDir), "/") {
		if c == ".." {
			return http.StatusForbidden, "access denied: session run dir is not an allowed directory"
		}
	}
	clean := NormalizeSessionRunDir(runDir, chatID)
	if !filepath.IsAbs(clean) {
		return http.StatusForbidden, "access denied: session run dir is not an allowed directory"
	}
	// Runner-side per-chat dirs: <anything>/asgard/data/{tmp,sessions}/<chat>[/...].
	for _, ns := range []string{"tmp", "sessions"} {
		marker := "/asgard/data/" + ns + "/" + chatID
		if strings.HasSuffix(clean, marker) || strings.Contains(clean, marker+"/") {
			return 0, ""
		}
	}
	if list, ok := s.sessionAgents(chatID); ok && run.IsAllowedDir(clean, agents.AllowedDirs(list, false)) {
		return 0, ""
	}
	return http.StatusForbidden, "access denied: session run dir is not an allowed directory"
}
