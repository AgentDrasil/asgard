package inject

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"syscall"
	"time"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// Send delivers prompt to the aw process listening on the socket at path.
//
// It returns nil when the message was injected. It returns an error wrapping
// types.ErrNotInjectable when the message was definitely not delivered and the
// caller should queue it instead: the server said so, or nothing is listening
// (socket missing / connection refused, e.g. opencode or a finished run; this
// case also wraps ErrUnavailable). ErrBadRequest and ErrInternal are returned
// for the other server-side failures; any other error is a transport failure
// and the delivery state is unknown.
//
// ctx must carry a deadline: the server bounds only reading the request and
// writing the reply, so a frozen aw would otherwise block Send forever. A nil
// return means the message was accepted by the injector's queue, not that the
// CLI has read it yet.
func Send(ctx context.Context, path, prompt string) error {
	_, err := SendGen(ctx, path, prompt)
	return err
}

// SendGen is Send that also returns the idle generation aw reported with its
// reply (0 when it reports none).
func SendGen(ctx context.Context, path, prompt string) (uint64, error) {
	resp, err := roundTrip(ctx, path, request{Prompt: prompt})
	return resp.IdleGen, err
}

// SendControl delivers a control (ControlHold / ControlRelease) to the aw
// process listening on the control socket at path (ControlPath of its
// injection socket). holdIdle bounds an idle hold (0: aw's default). Errors
// are those of Send; an aw that predates controls does not listen there, so
// the result is ErrUnavailable.
func SendControl(ctx context.Context, path, action string, holdIdle time.Duration) error {
	_, err := roundTrip(ctx, path, request{Control: action, IdleSeconds: int64(holdIdle / time.Second)})
	return err
}

func roundTrip(ctx context.Context, path string, req request) (response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return response{}, fmt.Errorf("%w: %w: %v", types.ErrNotInjectable, ErrUnavailable, err)
		}
		return response{}, fmt.Errorf("inject: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// DialContext only covers connecting; unblock I/O on cancellation.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	b, err := json.Marshal(req)
	if err != nil {
		return response{}, fmt.Errorf("inject: encode: %w", err)
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return response{}, ioErr(ctx, "write", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return response{}, ioErr(ctx, "read reply", err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return response{}, fmt.Errorf("inject: invalid reply: %w", err)
	}
	if resp.OK {
		return resp, nil
	}
	switch resp.Reason {
	case ReasonNotInjectable:
		return resp, types.ErrNotInjectable
	case ReasonBadRequest:
		return resp, fmt.Errorf("%w: %s", ErrBadRequest, resp.Error)
	default:
		return resp, fmt.Errorf("%w: %s", ErrInternal, resp.Error)
	}
}

func ioErr(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("inject: %s: %w", op, err)
}
