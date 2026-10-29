package runnerfs

import (
	"context"
	"errors"
	"io"
	"time"
	"uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Limits of a command's captured output on the backend. The runner streams
// everything; the backend keeps the head of stdout (the node output) and the
// tail of stderr (only its end appears in error messages).
const (
	MaxCommandStdout = 32 << 20
	MaxCommandStderr = 64 << 10
)

// CommandSpec describes one command node execution on the chat's runner. All
// paths are the workflow's view; the runner maps the chat's own tmp/session
// dirs onto its layout and confines everything else.
type CommandSpec struct {
	// RequestID makes the call idempotent and resumable; empty gets a fresh
	// one.
	RequestID string
	Command   string
	// WorkingDir is relative to RunDir or runner-absolute; empty is RunDir.
	WorkingDir string
	RunDir     string
	// TmpDir is the dir OutputFile is relative to.
	TmpDir     string
	OutputFile string
	// Sandbox is the effective sandbox setting; a runner refuses false unless
	// its operator allowed unsandboxed commands.
	Sandbox           bool
	AllowCrossSession bool
	// Timeout is the node timeout (0: none), enforced by the runner.
	Timeout time.Duration
}

// CommandArtifact is the declared output file the command produced.
type CommandArtifact struct {
	Name string
	// ViewerPath is the file API form (/tmp/<rel>, /session/<rel> or absolute).
	ViewerPath string
	// AbsPath is the path on the runner.
	AbsPath string
}

// CommandResult is the outcome of a command. On an error it still holds the
// output received so far.
type CommandResult struct {
	ExitCode int
	// Stdout is capped at MaxCommandStdout (StdoutTruncated), Stderr keeps its
	// last MaxCommandStderr bytes.
	Stdout          []byte
	StdoutTruncated bool
	Stderr          []byte
	Artifacts       []CommandArtifact
}

// CommandRunner is implemented by an FS whose chat runs commands on a runner.
// The host-local FS does not: runner-less development runs commands itself.
type CommandRunner interface {
	// RunCommand executes the command and returns its exit code, output and
	// artifacts. Cancelling ctx kills the command.
	RunCommand(ctx context.Context, spec CommandSpec) (CommandResult, error)
}

var _ CommandRunner = Remote{}

// Resume policy after a stream reset.
var (
	commandRetryBackoff = 200 * time.Millisecond
	commandMaxBackoff   = 5 * time.Second
	commandMaxAttempts  = 8
	// commandCancelWait bounds the explicit cancel call.
	commandCancelWait = 5 * time.Second
)

// commandStream accumulates the output received over possibly several
// streams of one request.
type commandStream struct {
	stdoutOff, stderrOff uint64
	attempts             int
	res                  CommandResult
	stderrTail           []byte
}

func (c *commandStream) addStdout(d []byte) {
	if room := MaxCommandStdout - len(c.res.Stdout); room < len(d) {
		d = d[:max(room, 0)]
		c.res.StdoutTruncated = true
	}
	c.res.Stdout = append(c.res.Stdout, d...)
}

func (c *commandStream) addStderr(d []byte) {
	c.stderrTail = append(c.stderrTail, d...)
	if over := len(c.stderrTail) - MaxCommandStderr; over > 0 {
		c.stderrTail = append([]byte(nil), c.stderrTail[over:]...)
	}
}

// apply consumes one data frame, dropping bytes already received. A gap means
// output was lost, which cannot be repaired.
func apply(off *uint64, sd *runnerpb.StreamData, add func([]byte)) error {
	if sd.GetOffset() > *off {
		return Errf(CodeInternal, "command output gap: expected offset %d, got %d", *off, sd.GetOffset())
	}
	d := sd.GetData()
	if skip := *off - sd.GetOffset(); skip > 0 {
		if skip >= uint64(len(d)) {
			return nil
		}
		d = d[skip:]
	}
	add(d)
	*off += uint64(len(d))
	return nil
}

// RunCommand implements CommandRunner. A stream reset is resumed with the same
// request_id and the offsets received so far, so no output is lost or
// duplicated and the command is not run twice.
func (r Remote) RunCommand(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	if spec.RequestID == "" {
		spec.RequestID = uuid.NewV7().String()
	}
	sandbox := spec.Sandbox
	req := &runnerpb.RunCommandRequest{
		ChatId:            r.ChatID,
		Command:           spec.Command,
		WorkingDir:        spec.WorkingDir,
		RunDir:            RunDirRef(r.ChatID, spec.RunDir),
		Sandbox:           &sandbox,
		AllowCrossSession: spec.AllowCrossSession,
		OutputFile:        spec.OutputFile,
		Tmp:               RunDirRef(r.ChatID, spec.TmpDir),
		RequestId:         spec.RequestID,
	}
	if spec.Timeout > 0 {
		req.TimeoutSeconds = int32((spec.Timeout + time.Second - 1) / time.Second)
	}

	var cs commandStream
	finish := func(err error) (CommandResult, error) {
		cs.res.Stderr = cs.stderrTail
		return cs.res, err
	}
	backoff := commandRetryBackoff
	for attempt := 0; ; attempt++ {
		done, err := r.commandOnce(ctx, req, &cs)
		if done {
			return finish(nil)
		}
		if ctx.Err() != nil {
			r.cancelCommand(req)
			return finish(ctx.Err())
		}
		if !retryableStream(err) || attempt+1 >= commandMaxAttempts {
			return finish(ToError(err))
		}
		select {
		case <-ctx.Done():
			r.cancelCommand(req)
			return finish(ctx.Err())
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, commandMaxBackoff)
	}
}

// commandOnce runs one stream of the request from the offsets received so far.
func (r Remote) commandOnce(ctx context.Context, req *runnerpb.RunCommandRequest, cs *commandStream) (bool, error) {
	req = &runnerpb.RunCommandRequest{
		ChatId: req.ChatId, Command: req.Command, WorkingDir: req.WorkingDir, RunDir: req.RunDir,
		Sandbox: req.Sandbox, AllowCrossSession: req.AllowCrossSession, TimeoutSeconds: req.TimeoutSeconds,
		OutputFile: req.OutputFile, Tmp: req.Tmp, RequestId: req.RequestId,
		ResumeStdoutOffset: cs.stdoutOff, ResumeStderrOffset: cs.stderrOff, Resumed: cs.attempts > 0,
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { cs.attempts++ }()
	st, err := r.Client.RunCommand(sctx, req)
	if err != nil {
		return false, err
	}
	for {
		f, err := st.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, io.ErrUnexpectedEOF
			}
			return false, err
		}
		switch p := f.GetPayload().(type) {
		case *runnerpb.RunCommandFrame_Stdout:
			if err := apply(&cs.stdoutOff, p.Stdout, cs.addStdout); err != nil {
				return false, err
			}
		case *runnerpb.RunCommandFrame_Stderr:
			if err := apply(&cs.stderrOff, p.Stderr, cs.addStderr); err != nil {
				return false, err
			}
		case *runnerpb.RunCommandFrame_Exit:
			cs.res.ExitCode = int(p.Exit.GetCode())
			cs.res.Artifacts = nil
			for _, a := range p.Exit.GetArtifacts() {
				cs.res.Artifacts = append(cs.res.Artifacts, CommandArtifact{Name: a.GetName(), ViewerPath: a.GetViewerPath(), AbsPath: a.GetAbsPath()})
			}
			return true, nil
		}
	}
}

// retryableStream reports whether err is a stream reset worth resuming (as
// opposed to an answer of the runner).
func retryableStream(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.Canceled, codes.DeadlineExceeded, codes.Aborted:
		return true
	}
	return false
}

// cancelCommand tells the runner to kill the command right away; without it
// the runner would wait out the reattach grace after a plain RPC cancel. The
// request carries only the ids and cancel, which the runner
// handles as a kill of the in-flight request_id.
func (r Remote) cancelCommand(req *runnerpb.RunCommandRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), commandCancelWait)
	defer cancel()
	st, err := r.Client.RunCommand(ctx, &runnerpb.RunCommandRequest{
		ChatId: req.ChatId, RequestId: req.RequestId, Cancel: true,
	})
	if err != nil {
		return
	}
	for {
		if _, err := st.Recv(); err != nil {
			return
		}
	}
}
