// Package inject carries a user message from the host into a running
// `aw claude` / `aw agy` process inside the agent sandbox.
//
// The per-run host socket directory is bind-mounted at /fakebash in the agent
// sandbox (next to fakebash.sock), so aw listens on /fakebash/inject.sock and
// the host dials <sockDir>/inject.sock. No bwrap change is needed.
//
// Protocol: the client writes one JSON line {"prompt":"..."}, the server
// replies with one JSON line ({"ok":true} or {"ok":false,"reason":...,
// "error":...}) once the injection function returned, then closes. The reply
// is synchronous so the caller knows whether to fall back to queueing.
//
// Control requests ({"control":"hold","idle_seconds":1800} or
// {"control":"release"}) carry no prompt: they keep the CLI's stdin open
// after its turn while the run has live subagents, or let it close (see
// common.Injector). They are answered like an injection but travel on a
// separate socket, control.sock next to inject.sock. An aw that predates
// controls does not listen there, so a newer runner gets ErrUnavailable and
// the control is a no-op; had it been sent over inject.sock, that aw would
// have injected it as an empty user turn. The server in turn rejects a
// control on inject.sock and an injection on control.sock.
package inject

import (
	"errors"
	"os"
	"path/filepath"
)

const (
	// SocketName is the file name of the socket inside the socket directory.
	SocketName = "inject.sock"

	// ControlSocketName is the file name of the control socket, which sits
	// next to the injection socket.
	ControlSocketName = "control.sock"

	// DefaultSandboxPath is where the socket lives inside the agent sandbox.
	DefaultSandboxPath = "/fakebash/" + SocketName

	// EnvSock overrides the in-sandbox socket path.
	EnvSock = "ASGARD_INJECT_SOCK"

	// maxRequestBytes bounds one request line.
	maxRequestBytes = 16 << 20
)

// Reasons carried in a failed reply.
const (
	ReasonNotInjectable = "not_injectable"
	ReasonBadRequest    = "bad_request"
	ReasonInternal      = "internal"
)

var (
	// ErrBadRequest means the server rejected the request as malformed.
	ErrBadRequest = errors.New("inject: bad request")
	// ErrInternal means the injection failed for a reason other than the run
	// being over; the message may or may not have been delivered.
	ErrInternal = errors.New("inject: internal error")
	// ErrUnavailable means nothing is listening (socket missing or connection
	// refused), e.g. opencode/simplest runs or an already finished aw. Send
	// also wraps types.ErrNotInjectable in that case, so callers can fall back
	// to queueing by checking only that one.
	ErrUnavailable = errors.New("inject: no listener")
)

// Control actions of a control request.
const (
	ControlHold    = "hold"
	ControlRelease = "release"
)

// ControlPath returns the control socket that belongs to the injection socket
// at injectPath.
func ControlPath(injectPath string) string {
	return filepath.Join(filepath.Dir(injectPath), ControlSocketName)
}

type request struct {
	Prompt string `json:"prompt"`
	// Control, when set, makes the request a control (Prompt is ignored).
	Control string `json:"control,omitempty"`
	// IdleSeconds bounds an idle hold; 0 means the server's default.
	IdleSeconds int64 `json:"idle_seconds,omitempty"`
}

type response struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// IdleGen is the injector's idle generation right after it accepted an
	// injection (0: unknown, e.g. an aw that predates the field).
	IdleGen uint64 `json:"idle_gen,omitempty"`
}

// SocketPath returns the path aw should listen on, or "" when the feature is
// disabled (no override and no /fakebash, i.e. outside the sandbox).
func SocketPath() string {
	if p := os.Getenv(EnvSock); p != "" {
		return p
	}
	if st, err := os.Stat("/fakebash"); err == nil && st.IsDir() {
		return DefaultSandboxPath
	}
	return ""
}
