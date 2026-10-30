package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/version"
)

type fakeRunnerRegistry struct{ nodes []runnerhub.NodeInfo }

func (f fakeRunnerRegistry) Nodes() []runnerhub.NodeInfo { return f.nodes }

func TestHandleRunners(t *testing.T) {
	t.Parallel()

	seen := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		reg  RunnerRegistry
		want []RunnerInfoResponse
	}{
		{"no hub", nil, []RunnerInfoResponse{}},
		{
			"snapshot",
			fakeRunnerRegistry{nodes: []runnerhub.NodeInfo{
				{NodeID: "runner-a", Online: true, LastSeen: seen, Version: "v1", ActiveRuns: 2, SupportedCLIs: map[string][]string{"opencode": {"m1"}}},
				{NodeID: "runner-b"},
			}},
			[]RunnerInfoResponse{
				{NodeID: "runner-a", Online: true, LastSeen: &seen, Version: "v1", ActiveRuns: 2, SupportedCLIs: map[string][]string{"opencode": {"m1"}}},
				{NodeID: "runner-b", SupportedCLIs: map[string][]string{}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, p := newAccessServer(t)
			srv.runners = tt.reg
			for _, w := range tt.want {
				require.NoError(t, srv.access.Assign(w.NodeID, "user-1", "admin"))
			}

			rec := serve(srv, http.MethodGet, "/api/runners", bearer(t, p, "user-1"))
			require.Equal(t, http.StatusOK, rec.Code)

			var got RunnersResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, version.Get(), got.BackendVersion)
			assert.Equal(t, tt.want, got.Runners)
		})
	}
}
