package config

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
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
}

// NewRemote connects to etcd (ETCD_ENDPOINTS / ETCD_PREFIX, like the backend)
// and starts watching. clis is the supported CLI/model map of this runner.
func NewRemote(ctx context.Context, nodeID string, clis map[string][]string) (*Remote, error) {
	mgr, client, err := config.NewEtcdConfigManager(ctx)
	if err != nil {
		return nil, err
	}
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
	return &Remote{nodeID: nodeID, client: client, tree: mgr, agents: store}, nil
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
