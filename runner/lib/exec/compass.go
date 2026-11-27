package exec

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

const (
	// maxCompassBody bounds a compass request: a bit over the 1 MiB output
	// sample cap fakebash works with.
	maxCompassBody = 1<<20 + 64<<10
	// compassTimeout bounds one compass request, on top of the model call's
	// own timeout (ASGARD_JEV_TIMEOUT_MS / ASGARD_GEMINI_TIMEOUT_MS).
	compassTimeout = 30 * time.Second
	// maxCompassLog bounds the Log of a summarize request. The sandbox sends
	// either a payload under 10 KiB or the inspector extract, which is capped
	// at 32 KiB plus one line of at most 4 KiB (about 37 KiB); 256 KiB leaves a
	// wide margin. Larger logs are not sent to the model (the sandbox keeps
	// the raw output).
	maxCompassLog = 256 << 10
	// compassSlotWait is the longest a call waits for a free in-flight slot; the
	// wait is further bounded by a fraction of the client's budget.
	compassSlotWait = time.Second
	// compassMinCallBudget is the least client budget left that still makes an
	// upstream model call worthwhile (the sandbox gives the model timeout plus a
	// 1s margin; below this the call would be aborted by the client's deadline).
	compassMinCallBudget = time.Second

	// Per run: at most 4 calls at once, 120 per minute sustained (2/s) with
	// bursts of up to 10.
	maxCompassInFlight = 4
	compassRunRate     = rate.Limit(2)
	compassRunBurst    = 10
	// Runner-wide, shared by every run's status server: the cost ceiling no
	// matter how many runs are active.
	maxCompassGlobalInFlight = 16
	compassGlobalRate        = rate.Limit(10)
	compassGlobalBurst       = 40
)

var (
	errCompassBusy = errors.New("compass: no free slot")
	errCompassRate = errors.New("compass: rate limit")
)

// compassBackend makes the jev and Gemini calls for a run's sandboxes. The API
// keys it holds never leave the runner. fakebash.CompassHost implements it.
type compassBackend interface {
	Classify(ctx context.Context, req sandboxsock.CompassClassifyRequest) sandboxsock.CompassClassifyResponse
	Summarize(ctx context.Context, req sandboxsock.CompassSummarizeRequest) sandboxsock.CompassSummarizeResponse
}

// compassLimiter admits at most cap(slots) concurrent calls and a token-bucket
// rate of calls. A call waits a bounded time for a slot.
type compassLimiter struct {
	slots chan struct{}
	rate  *rate.Limiter
	wait  time.Duration
	now   func() time.Time
}

func newCompassLimiter(inFlight int, r rate.Limit, burst int) *compassLimiter {
	return &compassLimiter{slots: make(chan struct{}, inFlight), rate: rate.NewLimiter(r, burst), wait: compassSlotWait, now: time.Now}
}

// globalCompass is shared by the status servers of all runs of the runner.
var globalCompass = newCompassLimiter(maxCompassGlobalInFlight, compassGlobalRate, compassGlobalBurst)

// takeSlot waits up to wait (or until ctx ends) for a free slot.
func (l *compassLimiter) takeSlot(ctx context.Context, wait time.Duration) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	default:
	}
	if wait <= 0 {
		return errCompassBusy
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return errCompassBusy
	}
}

// reserve takes a rate token if one is available right now. The reservation
// can be cancelled to give the token back.
// It also returns the time the reservation was made at, which CancelAt needs.
func (l *compassLimiter) reserve() (*rate.Reservation, time.Time, error) {
	now := l.now()
	r := l.rate.ReserveN(now, 1)
	if !r.OK() || r.DelayFrom(now) > 0 {
		r.CancelAt(now)
		return nil, now, errCompassRate
	}
	return r, now, nil
}

// acquire reserves a call, waiting up to l.wait (or until ctx ends) for a slot;
// release must be called by the goroutine doing the upstream call once it has
// returned, when err is nil. errCompassRate means the bucket is empty.
func (l *compassLimiter) acquire(ctx context.Context) (release func(), err error) {
	return acquireBoth(ctx, l, nil, 0)
}

// acquireBoth takes a slot and a rate token of the run's limiter and of the
// runner-wide one (nil is skipped). Slots are taken first (waiting at most the
// limiters' wait, and at most maxWait overall when maxWait > 0); the rate
// tokens are then reserved without waiting and are given back if either
// bucket is empty, so a refused call spends no token.
func acquireBoth(ctx context.Context, run, global *compassLimiter, maxWait time.Duration) (release func(), err error) {
	limiters := []*compassLimiter{run}
	if global != nil {
		limiters = append(limiters, global)
	}
	var deadline time.Time
	if maxWait > 0 {
		deadline = time.Now().Add(maxWait)
	}
	var taken []*compassLimiter
	giveSlots := func() {
		for _, l := range taken {
			<-l.slots
		}
	}
	for _, l := range limiters {
		wait := l.wait
		if !deadline.IsZero() {
			wait = min(wait, time.Until(deadline))
		}
		if err := l.takeSlot(ctx, wait); err != nil {
			giveSlots()
			return nil, err
		}
		taken = append(taken, l)
	}
	var resv []*rate.Reservation
	var resvAt []time.Time
	for _, l := range limiters {
		r, at, err := l.reserve()
		if err != nil {
			for i, rv := range resv {
				rv.CancelAt(resvAt[i])
			}
			giveSlots()
			return nil, err
		}
		resv = append(resv, r)
		resvAt = append(resvAt, at)
	}
	return giveSlots, nil
}

// compassCall is the shared guard of the compass routes: no backend means the
// feature is off (200, enabled=false in the body is built by off), then the
// body cap, an optional pre-check that answers without a model call, the
// limits and the request timeout. The in-flight slots are held by the goroutine
// making the upstream call, not by the handler, so a handler that gave up
// (timeout) does not free a slot while the call is still running.
func (s *statusServer) compassCall(w http.ResponseWriter, r *http.Request, in any, off any, pre func() any, run func(ctx context.Context) any) {
	if s.compass == nil {
		writeJSON(w, http.StatusOK, off)
		return
	}
	if !decodeBounded(w, r, maxCompassBody, in) {
		return
	}
	if pre != nil {
		if v := pre(); v != nil {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	start := time.Now()
	budget := sandboxsock.DeadlineBudget(r)
	maxWait := compassSlotWait
	if budget > 0 {
		maxWait = min(maxWait, budget/4)
	}
	ctx, cancel := s.callContext(r)
	defer cancel()
	release, err := acquireBoth(ctx, s.compassLimit, s.compassGlobal, maxWait)
	if err == nil && budget > 0 && budget-time.Since(start) < compassMinCallBudget {
		// The sandbox gives up before a model call could finish.
		release()
		err = errCompassBusy
	}
	if err != nil {
		switch {
		case errors.Is(err, errCompassBusy), errors.Is(err, errCompassRate):
			http.Error(w, "too many compass calls", http.StatusTooManyRequests)
		default:
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
		}
		return
	}
	timeout := s.compassTimeout
	if timeout <= 0 {
		timeout = compassTimeout
	}
	if budget > 0 {
		timeout = min(timeout, budget-time.Since(start))
	}
	ctx, cancelT := context.WithTimeout(ctx, timeout)
	defer cancelT()

	type result struct{ v any }
	done := make(chan result, 1)
	go func() {
		defer release()
		done <- result{run(ctx)}
	}()
	select {
	case res := <-done:
		writeJSON(w, http.StatusOK, res.v)
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
			return
		}
		log.Warn().Str("run_id", s.sr.GetRunId()).Msg("compass call timed out")
		http.Error(w, "compass timeout", http.StatusGatewayTimeout)
	}
}

func (s *statusServer) handleCompassClassify(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.CompassClassifyRequest
	s.compassCall(w, r, &req, sandboxsock.CompassClassifyResponse{}, nil, func(ctx context.Context) any {
		return s.compass.Classify(ctx, req)
	})
}

func (s *statusServer) handleCompassSummarize(w http.ResponseWriter, r *http.Request) {
	var req sandboxsock.CompassSummarizeRequest
	s.compassCall(w, r, &req, sandboxsock.CompassSummarizeResponse{}, func() any {
		if len(req.Log) > maxCompassLog {
			// Enabled with no summary: the sandbox keeps the raw output.
			return sandboxsock.CompassSummarizeResponse{Enabled: true}
		}
		return nil
	}, func(ctx context.Context) any {
		return s.compass.Summarize(ctx, req)
	})
}
