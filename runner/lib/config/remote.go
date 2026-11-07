package config

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

// Remote is the runner's live view of the shared etcd configuration: the
// agent definitions effective for this node (agents/ plus
// runners/<node>/agents/, materialized into the runner's data dir) and the aw/,
// simplest/, credentials/ and asgard/proxy.yaml trees.
//
// A configuration that cannot be loaded (etcd unreachable, a SOPS signature or
// decode failure) never makes the runner exit: the Remote keeps retrying in the
// background and Health reports why it is not usable, so the runner can stay
// connected to the backend as unhealthy and be debugged instead of crash-looping.
type Remote struct {
	nodeID string
	// client, mgr and agents are nil when etcd could not be set up at all.
	client *clientv3.Client
	mgr    interface {
		Start(context.Context) error
		Get() *config.AsgardTreeConfig
	}
	agents *config.AgentStore

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	onTree   []func(*config.AsgardTreeConfig)
	onAgents []func()
	// loadErr is why the initial load has not completed yet.
	loadErr error
	// treeErr is the latest rejected configuration revision; the next accepted
	// revision clears it. The previous good tree stays in use meanwhile.
	treeErr     error
	treeReady   bool
	agentsReady bool
	stops       []context.CancelFunc
}

const (
	loadRetryDelay     = 10 * time.Second
	loadAttemptTimeout = 20 * time.Second
)

// NewRemote connects to etcd (ETCD_ENDPOINTS / ETCD_PREFIX, like the backend)
// and starts watching. clis is the supported CLI/model map of this runner. It
// never fails: see Remote and Health.
func NewRemote(ctx context.Context, nodeID string, clis map[string][]string) *Remote {
	r := &Remote{nodeID: nodeID}
	r.ctx, r.cancel = context.WithCancel(ctx)
	mgr, client, err := config.NewEtcdConfigManager(r.ctx)
	if err != nil {
		// Not retryable: the environment itself is wrong.
		r.loadErr = fmt.Errorf("etcd configuration unavailable: %w", err)
		log.Error().Err(err).Msg("etcd configuration unavailable; runner is unhealthy")
		return r
	}
	r.mgr, r.client = mgr, client
	mgr.OnChange(func(_, newTree *config.AsgardTreeConfig) {
		if newTree == nil {
			return
		}
		r.mu.Lock()
		r.treeErr = nil
		r.mu.Unlock()
		r.fireTree(newTree)
	})
	mgr.OnError(func(err error) {
		log.Error().Err(err).Msg("etcd configuration rejected; keeping the previous configuration")
		r.mu.Lock()
		r.treeErr = fmt.Errorf("etcd configuration rejected: %w", err)
		r.mu.Unlock()
	})
	store := config.NewEtcdAgentStore(client, config.EtcdPrefix())
	store.SetSupportedCLIs(clis)
	store.OnChange(r.fireAgents)
	r.agents = store

	if !r.tryLoad() {
		r.wg.Add(1)
		go r.retryLoop()
	}
	return r
}

// tryLoad starts whatever is not loaded yet and reports whether all of it is.
func (r *Remote) tryLoad() bool {
	r.mu.Lock()
	treeReady, agentsReady := r.treeReady, r.agentsReady
	r.mu.Unlock()

	if !treeReady {
		if err := r.attempt(r.mgr.Start); err != nil {
			r.setLoadErr(fmt.Errorf("start etcd config: %w", err))
			return false
		}
		r.mu.Lock()
		r.treeReady = true
		r.mu.Unlock()
		r.fireTree(r.mgr.Get())
	}
	if !agentsReady {
		if err := r.attempt(r.agents.Start); err != nil {
			r.setLoadErr(fmt.Errorf("start agent store: %w", err))
			return false
		}
		r.mu.Lock()
		r.agentsReady = true
		r.mu.Unlock()
		r.fireAgents()
	}
	r.setLoadErr(nil)
	return true
}

// attempt runs one start step, giving up after loadAttemptTimeout so an
// unreachable etcd cannot block forever. The context outlives a successful
// step: it also drives the watchers the step launched.
func (r *Remote) attempt(start func(context.Context) error) error {
	stop, err := config.StartBounded(r.ctx, loadAttemptTimeout, start)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.stops = append(r.stops, stop)
	r.mu.Unlock()
	return nil
}

func (r *Remote) setLoadErr(err error) {
	r.mu.Lock()
	changed := (r.loadErr == nil) != (err == nil)
	r.loadErr = err
	r.mu.Unlock()
	switch {
	case err != nil:
		log.Error().Err(err).Msg("configuration not loaded; runner is unhealthy, retrying")
	case changed:
		log.Info().Msg("configuration loaded; runner is healthy again")
	}
}

func (r *Remote) retryLoop() {
	defer r.wg.Done()
	t := time.NewTicker(loadRetryDelay)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			if r.tryLoad() {
				return
			}
		}
	}
}

func (r *Remote) fireTree(tree *config.AsgardTreeConfig) {
	r.mu.Lock()
	fns := slices.Clone(r.onTree)
	r.mu.Unlock()
	for _, fn := range fns {
		fn(tree)
	}
}

func (r *Remote) fireAgents() {
	r.mu.Lock()
	fns := slices.Clone(r.onAgents)
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Health returns why the configuration is not usable, or nil when it is
// loaded and valid. The runner reports it to the backend in every heartbeat.
func (r *Remote) Health() error {
	r.mu.Lock()
	loadErr, treeErr, agentsReady := r.loadErr, r.treeErr, r.agentsReady
	r.mu.Unlock()
	switch {
	case loadErr != nil:
		return loadErr
	case treeErr != nil:
		return treeErr
	case agentsReady:
		if _, err := r.agents.AgentsFor(r.nodeID); err != nil {
			return fmt.Errorf("agent definitions: %w", err)
		}
	}
	return nil
}

// OnTreeChange registers fn, called with the new tree after the aw/, simplest/,
// credentials/ or asgard/ configuration changed.
func (r *Remote) OnTreeChange(fn func(*config.AsgardTreeConfig)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onTree = append(r.onTree, fn)
}

// notLoaded explains why the configuration is not available.
func (r *Remote) notLoaded() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return fmt.Errorf("configuration not loaded: %w", r.loadErr)
	}
	return errors.New("configuration not loaded")
}

// SandboxConfig builds the backend-style config the sandboxes are set up
// from: the shared asgard/config.yaml values, the proxy configuration (as the
// backend applies it at startup) and the local config path to mask.
func (r *Remote) SandboxConfig() (*config.Config, error) {
	tree := r.Tree()
	if tree == nil {
		return nil, r.notLoaded()
	}
	return SandboxConfigFromTree(tree)
}

// SandboxConfigFromTree derives the sandbox config from tree.
func SandboxConfigFromTree(tree *config.AsgardTreeConfig) (*config.Config, error) {
	conf := tree.Asgard.Config.Clone()
	if tree.Asgard.Proxy.Enable || len(tree.Asgard.Proxy.Rules) > 0 {
		proxyCfg := tree.Asgard.Proxy
		conf.SetProxy(&proxyCfg)
	}
	if err := conf.ApplyDefaultsAndValidate(); err != nil {
		return nil, fmt.Errorf("invalid configuration from etcd: %w", err)
	}
	conf.ConfigPath = paths.ConfigFile()
	return conf, nil
}

// Agents returns the effective agent list of this runner.
func (r *Remote) Agents() ([]*agentspec.Agent, error) {
	r.mu.Lock()
	ready := r.agentsReady
	r.mu.Unlock()
	if !ready {
		return nil, r.notLoaded()
	}
	return r.agents.AgentsFor(r.nodeID)
}

// OnAgentsChange registers fn, called after the agent definitions changed or
// became available for the first time.
func (r *Remote) OnAgentsChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onAgents = append(r.onAgents, fn)
}

// Tree returns the latest aw/, simplest/, credentials/ and proxy
// configuration (nil before the first load).
func (r *Remote) Tree() *config.AsgardTreeConfig {
	if r.mgr == nil {
		return nil
	}
	return r.mgr.Get()
}

// Close stops the watchers and the etcd client.
func (r *Remote) Close() {
	r.cancel()
	r.wg.Wait()
	r.mu.Lock()
	stops := r.stops
	r.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
	if r.agents != nil {
		r.agents.Close()
	}
	if r.client != nil {
		_ = r.client.Close()
	}
}
