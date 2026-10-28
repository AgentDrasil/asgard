package config

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

// Remote is the runner's live view of the shared etcd configuration: the
// agent definitions effective for this node (agents/ plus
// runners/<node>/agents/, materialized into the runner's data dir) and the aw/,
// simplest/, credentials/ and asgard/proxy.yaml trees. Later commits consume
// these when executing runs.
type Remote struct {
	nodeID string
	client *clientv3.Client
	tree   interface {
		Get() *config.AsgardTreeConfig
	}
	agents *config.AgentStore

	mu     sync.Mutex
	onTree []func(*config.AsgardTreeConfig)
}

// NewRemote connects to etcd (ETCD_ENDPOINTS / ETCD_PREFIX, like the backend)
// and starts watching. clis is the supported CLI/model map of this runner.
func NewRemote(ctx context.Context, nodeID string, clis map[string][]string) (*Remote, error) {
	mgr, client, err := config.NewEtcdConfigManager(ctx)
	if err != nil {
		return nil, err
	}
	r := &Remote{nodeID: nodeID, client: client, tree: mgr}
	mgr.OnChange(func(_, newTree *config.AsgardTreeConfig) {
		if newTree == nil {
			return
		}
		r.mu.Lock()
		fns := slices.Clone(r.onTree)
		r.mu.Unlock()
		for _, fn := range fns {
			fn(newTree)
		}
	})
	if err := mgr.Start(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("start etcd config: %w", err)
	}
	store := config.NewEtcdAgentStore(client, config.EtcdPrefix())
	store.SetSupportedCLIs(clis)
	if err := store.Start(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("start agent store: %w", err)
	}
	r.agents = store
	return r, nil
}

// OnTreeChange registers fn, called with the new tree after the aw/, simplest/,
// credentials/ or asgard/ configuration changed.
func (r *Remote) OnTreeChange(fn func(*config.AsgardTreeConfig)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onTree = append(r.onTree, fn)
}

// SandboxConfig builds the backend-style config the sandboxes are set up
// from: the shared asgard/config.yaml values, the proxy configuration (as the
// backend applies it at startup) and the local config path to mask.
func (r *Remote) SandboxConfig() (*config.Config, error) {
	tree := r.tree.Get()
	if tree == nil {
		return nil, errors.New("configuration not loaded")
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
func (r *Remote) Agents() ([]*agentspec.Agent, error) { return r.agents.AgentsFor(r.nodeID) }

// OnAgentsChange registers fn, called after the agent definitions changed.
func (r *Remote) OnAgentsChange(fn func()) { r.agents.OnChange(fn) }

// Tree returns the latest aw/, simplest/, credentials/ and proxy
// configuration (nil before the first load).
func (r *Remote) Tree() *config.AsgardTreeConfig { return r.tree.Get() }

// Close stops the watchers and the etcd client.
func (r *Remote) Close() {
	r.agents.Close()
	_ = r.client.Close()
}
