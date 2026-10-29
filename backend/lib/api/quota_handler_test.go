package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type quotaClient struct {
	runnerpb.RunnerServiceClient
	usages []*runnerpb.ModelUsage
	err    error
}

func (c quotaClient) QueryQuota(context.Context, *runnerpb.QueryQuotaRequest, ...grpc.CallOption) (*runnerpb.QueryQuotaResponse, error) {
	return &runnerpb.QueryQuotaResponse{Usages: c.usages}, c.err
}

type quotaRegistry struct{ nodes []runnerhub.NodeInfo }

func (r quotaRegistry) Nodes() []runnerhub.NodeInfo { return r.nodes }

func quotaServer(nodes []runnerhub.NodeInfo, clients map[string]quotaClient) *Server {
	return &Server{
		runners: quotaRegistry{nodes: nodes},
		runnerClient: func(id string) (runnerpb.RunnerServiceClient, error) {
			c, ok := clients[id]
			if !ok {
				return nil, runnerhub.ErrRunnerOffline
			}
			return c, nil
		},
	}
}

func getQuota(t *testing.T, s *Server, query string) (int, []byte) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleQuota(rr, httptest.NewRequest(http.MethodGet, "/api/quota"+query, nil))
	return rr.Code, rr.Body.Bytes()
}

var usageA = []*runnerpb.ModelUsage{{Cli: "opencode", Model: "m1", Remaining: 0.5, RefreshDate: 7, Enabled: true,
	Limits: []*runnerpb.QuotaLimit{{Name: "5h", Remaining: 0.5, RefreshDate: 7}}}}
var usageB = []*runnerpb.ModelUsage{{Cli: "simplest", Model: "m2", Remaining: 1, Enabled: true}}

func TestQuotaSingleRunnerKeepsFlatShape(t *testing.T) {
	s := quotaServer([]runnerhub.NodeInfo{{NodeID: "a", Online: true}, {NodeID: "off", Online: false}},
		map[string]quotaClient{"a": {usages: usageA}})
	code, body := getQuota(t, s, "")
	require.Equal(t, http.StatusOK, code, string(body))
	var flat map[string][]map[string]any
	require.NoError(t, json.Unmarshal(body, &flat))
	require.Contains(t, flat, "opencode")
	assert.Equal(t, "m1", flat["opencode"][0]["model"])
	assert.NotContains(t, flat, "runners")
}

func TestQuotaAggregateAcrossRunners(t *testing.T) {
	s := quotaServer([]runnerhub.NodeInfo{{NodeID: "a", Online: true}, {NodeID: "b", Online: true}, {NodeID: "c", Online: true}, {NodeID: "off", Online: false}},
		map[string]quotaClient{"a": {usages: usageA}, "b": {usages: usageB}, "c": {err: assert.AnError}})
	code, body := getQuota(t, s, "")
	require.Equal(t, http.StatusOK, code, string(body))
	var all struct {
		Runners map[string]map[string][]map[string]any `json:"runners"`
		Errors  map[string]string                      `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(body, &all))
	assert.Len(t, all.Runners, 2)
	assert.Contains(t, all.Runners["a"], "opencode")
	assert.Contains(t, all.Runners["b"], "simplest")
	assert.Contains(t, all.Errors, "c")
	assert.NotContains(t, all.Runners, "off")
}

func TestQuotaSingleRunnerWithErrorUsesAggregateShape(t *testing.T) {
	s := quotaServer([]runnerhub.NodeInfo{{NodeID: "c", Online: true}}, map[string]quotaClient{"c": {err: assert.AnError}})
	code, body := getQuota(t, s, "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), `"errors"`)
	assert.Contains(t, string(body), `"runners"`)
}

func TestQuotaRunnerParam(t *testing.T) {
	s := quotaServer([]runnerhub.NodeInfo{{NodeID: "a", Online: true}, {NodeID: "b", Online: true}, {NodeID: "off", Online: false}, {NodeID: "bad", Online: true}},
		map[string]quotaClient{"a": {usages: usageA}, "b": {usages: usageB}, "bad": {err: assert.AnError}})

	code, body := getQuota(t, s, "?runner=b")
	require.Equal(t, http.StatusOK, code)
	var flat map[string][]map[string]any
	require.NoError(t, json.Unmarshal(body, &flat))
	assert.Contains(t, flat, "simplest")
	assert.NotContains(t, flat, "opencode")

	code, _ = getQuota(t, s, "?runner=nope")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = getQuota(t, s, "?runner=off")
	assert.Equal(t, http.StatusServiceUnavailable, code)
	code, _ = getQuota(t, s, "?runner=bad")
	assert.Equal(t, http.StatusBadGateway, code)
}

func TestQuotaNoRunnerOnline(t *testing.T) {
	s := quotaServer([]runnerhub.NodeInfo{{NodeID: "off", Online: false}}, nil)
	code, _ := getQuota(t, s, "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
}

func TestQuotaProtoRoundTripKeepsLimits(t *testing.T) {
	s := quotaServer([]runnerhub.NodeInfo{{NodeID: "a", Online: true}}, map[string]quotaClient{"a": {usages: usageA}})
	_, body := getQuota(t, s, "?runner=a")
	assert.JSONEq(t, `{"opencode":[{"model":"m1","remaining":0.5,"refresh_date":7,"limits":[{"name":"5h","remaining":0.5,"refresh_date":7}]}]}`, string(body))
}

// Creating a runner-bound chat leaves the backend's own data dirs alone.
func TestCreateRunnerBoundSessionCreatesNoLocalDirs(t *testing.T) {
	f := setupBindingFixture(t)
	rr := httptest.NewRecorder()
	f.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"currentAgent":"global-agent","runnerId":"r1"}`)))
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var created ChatSession
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	assert.NoDirExists(t, paths.SessionTmpDir(created.ChatID))
	assert.NoDirExists(t, paths.SessionDir(created.ChatID))
	assert.NoDirExists(t, paths.CABundleDir(created.ChatID))
	if _, err := os.Stat(paths.TmpDir()); err == nil {
		entries, _ := os.ReadDir(paths.TmpDir())
		assert.Empty(t, entries)
	}
}

func TestQuotaClientWithoutRegistry(t *testing.T) {
	s := &Server{runnerClient: func(string) (runnerpb.RunnerServiceClient, error) {
		return quotaClient{usages: usageB}, nil
	}}
	code, body := getQuota(t, s, "")
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Contains(t, string(body), "simplest")
}
