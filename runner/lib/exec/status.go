package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
)

const (
	// SocketName is the status socket's file name inside the per-run sock dir.
	SocketName = "asgard.sock"
	// SandboxStatusURL is how the sandboxes address the status socket: the
	// sock dir is bind mounted at /fakebash in both of them. aw and the
	// sandbox tools (ask-user, call-peer, ...) understand the unix:// form
	// (HTTP over the socket).
	SandboxStatusURL = sandboxsock.DefaultURL

	maxEventContent  = 4 << 20
	maxEventMetadata = 4 << 20
	maxEventArtifact = 1000

	// stopTimeout bounds how long stopping the status socket waits for
	// in-flight updates.
	stopTimeout = 10 * time.Second
)

// statusUpdate is the JSON body aw posts to the status endpoint (the backend's
// AgentStatusUpdate). Any chat/node/token ids it carries are ignored: the
// runner stamps the identity of the run it started.
type statusUpdate struct {
	StepIndex int            `json:"step_index"`
	Source    string         `json:"source"`
	EntryType string         `json:"entry_type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// statusServer serves the sandbox -> runner callbacks of one run on a unix
// socket: status updates become RunEvents, ask-user / call-peer / find-peer /
// metrics calls are forwarded to the backend (see callbacks.go).
type statusServer struct {
	sr      *runnerpb.StartRun
	agent   *agentspec.Agent
	runDir  string
	sink    conn.Sink
	ctx     context.Context
	mu      sync.Mutex // serializes event_index assignment and sink delivery
	nextIdx uint64

	// callbacks reaches the backend's AgentCallbackService; nil rejects the
	// callback routes.
	callbacks CallbacksFunc
	// callSeq numbers the sandbox calls of this run (call_seq).
	callSeq  atomic.Uint64
	retryMin time.Duration
	retryMax time.Duration
}

func newStatusServer(ctx context.Context, sr *runnerpb.StartRun, agent *agentspec.Agent, runDir string, sink conn.Sink) *statusServer {
	return &statusServer{sr: sr, agent: agent, runDir: runDir, sink: sink, ctx: ctx, retryMin: callRetryMin, retryMax: callRetryMax}
}

// serve listens on sockDir/asgard.sock. The returned stop func closes the
// listener and waits for in-flight requests.
func (s *statusServer) serve(sockDir string) (stop func(), err error) {
	ln, closeDir, err := listenUnix(sockDir)
	if err != nil {
		return nil, fmt.Errorf("listening on status socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+sandboxsock.PathStatus, s.handleStatus)
	s.registerCallbacks(mux)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	return func() {
		// Wait for in-flight updates so every RunEvent is queued before the
		// output and result; force-close if a handler is stuck.
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
		<-done
		closeDir()
	}, nil
}

// maxSockPath is a conservative sun_path limit (108 on Linux, 104 on BSD).
const maxSockPath = 100

// listenUnix listens on sockDir/asgard.sock. Per-run sock dirs live under the
// user's home, so the path can exceed sun_path; in that case the socket is
// bound through /proc/self/fd/<dir fd>, which is short.
func listenUnix(sockDir string) (net.Listener, func(), error) {
	path := filepath.Join(sockDir, SocketName)
	if len(path) < maxSockPath {
		ln, err := net.Listen("unix", path)
		return ln, func() {}, err
	}
	dir, err := os.Open(sockDir)
	if err != nil {
		return nil, nil, err
	}
	ln, err := net.Listen("unix", fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), SocketName))
	if err != nil {
		_ = dir.Close()
		return nil, nil, err
	}
	return ln, func() { _ = dir.Close() }, nil
}

func (s *statusServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	var u statusUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*maxEventContent)).Decode(&u); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.emit(r.Context(), u); err != nil {
		log.Warn().Err(err).Str("run_id", s.sr.GetRunId()).Msg("failed to forward status update")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// emit sends u as RunEvents (several when it names more than 1000 artifacts).
func (s *statusServer) emit(reqCtx context.Context, u statusUpdate) error {
	arts := s.artifacts(u.Metadata)
	// Everything sandbox-controlled that lands in a proto string must be
	// valid UTF-8, or the message could never be marshaled (or replayed).
	content := truncateUTF8(strings.ToValidUTF8(u.Content, "\uFFFD"), maxEventContent)
	source := strings.ToValidUTF8(u.Source, "\uFFFD")
	entryType := strings.ToValidUTF8(u.EntryType, "\uFFFD")
	var meta []byte
	if len(u.Metadata) > 0 {
		b, err := json.Marshal(u.Metadata)
		switch {
		case err != nil:
			log.Warn().Err(err).Str("run_id", s.sr.GetRunId()).Msg("dropping unencodable status metadata")
		case len(b) > maxEventMetadata:
			log.Warn().Int("size", len(b)).Str("run_id", s.sr.GetRunId()).Msg("dropping oversized status metadata")
		default:
			meta = b
		}
	}

	// Unblock when either the sandbox request or the run goes away.
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	stop := context.AfterFunc(reqCtx, cancel)
	defer stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	for first := true; first || len(arts) > 0; first = false {
		n := min(len(arts), maxEventArtifact)
		s.nextIdx++
		ev := &runnerpb.RunEvent{
			RunId:      s.sr.GetRunId(),
			ChatId:     s.sr.GetChatId(),
			NodeId:     s.sr.GetStatusScope().GetNodeId(),
			RunToken:   s.sr.GetStatusScope().GetRunToken(),
			StepIndex:  int32(u.StepIndex),
			Source:     source,
			EntryType:  entryType,
			Artifacts:  arts[:n],
			EventIndex: s.nextIdx,
		}
		if first {
			ev.Content, ev.MetadataJson = content, meta
		}
		arts = arts[n:]
		if err := s.sink.Event(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// artifacts computes the artifact files named in metadata["target_files"]
// with the same shared logic the backend applies to single-agent status
// updates (agents.StatusArtifacts).
func (s *statusServer) artifacts(meta map[string]any) []*runnerpb.Artifact {
	files, _ := meta["target_files"].([]any)
	targets := make([]string, 0, len(files))
	for _, f := range files {
		if tf, ok := f.(string); ok && tf != "" {
			targets = append(targets, tf)
		}
	}
	var out []*runnerpb.Artifact
	for _, a := range agents.StatusArtifacts(s.sr.GetChatId(), targets, &s.agent.Config, s.runDir) {
		out = append(out, &runnerpb.Artifact{
			ViewerPath: strings.ToValidUTF8(a.ViewerPath, "\uFFFD"),
			AbsPath:    strings.ToValidUTF8(a.HostPath, "\uFFFD"),
		})
	}
	return out
}
