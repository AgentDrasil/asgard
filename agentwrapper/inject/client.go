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
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w: %w: %v", types.ErrNotInjectable, ErrUnavailable, err)
		}
		return fmt.Errorf("inject: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// DialContext only covers connecting; unblock I/O on cancellation.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	b, err := json.Marshal(request{Prompt: prompt})
	if err != nil {
		return fmt.Errorf("inject: encode: %w", err)
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return ioErr(ctx, "write", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return ioErr(ctx, "read reply", err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("inject: invalid reply: %w", err)
	}
	if resp.OK {
		return nil
	}
	switch resp.Reason {
	case ReasonNotInjectable:
		return types.ErrNotInjectable
	case ReasonBadRequest:
		return fmt.Errorf("%w: %s", ErrBadRequest, resp.Error)
	default:
		return fmt.Errorf("%w: %s", ErrInternal, resp.Error)
	}
}

func ioErr(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("inject: %s: %w", op, err)
}
