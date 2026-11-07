package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/version"
)

// ErrConfigUnavailable marks a New failure caused by the shared configuration
// (etcd unreachable, undecodable or invalid) rather than by the environment of
// this process. Such a failure may heal without a restart.
var ErrConfigUnavailable = errors.New("configuration unavailable")

const (
	// degradedAddr is where the degraded status endpoint listens; the real
	// port lives in the configuration that could not be loaded.
	degradedAddr = ":8080"
	// configRetryDelay is the pause between configuration load attempts.
	configRetryDelay = 15 * time.Second
)

// newWhenConfigAvailable builds the App, retrying while the configuration is
// unavailable. Any other failure is returned as is. Meanwhile a degraded
// status endpoint answers, without error details: authentication is configured
// by the very configuration that is missing, so the reason is only logged.
func newWhenConfigAvailable(ctx context.Context, opts ...Option) (*App, error) {
	return retryConfig(ctx, configRetryDelay, degradedAddr, func() (*App, error) { return New(opts...) })
}

func retryConfig(ctx context.Context, delay time.Duration, addr string, build func() (*App, error)) (*App, error) {
	var degraded *http.Server
	stopDegraded := func() {
		if degraded == nil {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = degraded.Shutdown(shutdownCtx)
		degraded = nil
	}
	defer stopDegraded()

	var lastErr error
	attempts := 0
	for {
		a, err := build()
		if err == nil {
			stopDegraded()
			if attempts > 0 {
				a.server.Diagnostics().AddWarning("config", fmt.Sprintf("configuration loaded after %d failed attempts; last error: %v", attempts, lastErr))
			}
			return a, nil
		}
		if !errors.Is(err, ErrConfigUnavailable) {
			return nil, err
		}
		attempts++
		lastErr = err
		log.Error().Err(err).Int("attempt", attempts).Dur("retry_in", delay).Msg("configuration unavailable; backend is degraded and will retry")
		if degraded == nil {
			degraded = serveDegraded(addr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// serveDegraded starts the status endpoint of a backend without configuration.
func serveDegraded(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/system/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "degraded",
			"errors":   []string{"configuration not loaded; see the backend logs for the reason"},
			"warnings": []string{},
			"version":  version.Get(),
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"backend configuration not loaded"}`))
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn().Err(err).Str("addr", addr).Msg("degraded status endpoint failed")
		}
	}()
	return srv
}
