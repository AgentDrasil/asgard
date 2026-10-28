package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestCheckWorkspaceRunDir(t *testing.T) {
	t.Parallel()
	local := &Server{}
	remote := &Server{runnerClient: func(string) (runnerpb.RunnerServiceClient, error) { return nil, nil }}

	tests := []struct {
		name string
		s    *Server
		dir  string
		want int
	}{
		{"no runners: unrestricted", local, "", 0},
		{"empty is an explicit 400", remote, "", http.StatusBadRequest},
		{"dotdot component", remote, "/home/u/asgard/data/tmp/chat1/../x", http.StatusForbidden},
		{"dots inside a name are fine", remote, "/r/asgard/data/tmp/chat1/a..b", 0},
		{"relative", remote, "work/dir", http.StatusForbidden},
		{"runner tmp dir", remote, "/r/asgard/data/tmp/chat1", 0},
		{"runner session subdir", remote, "/r/asgard/data/sessions/chat1/sub", 0},
		{"other chat's dir", remote, "/r/asgard/data/tmp/chat2", http.StatusForbidden},
		{"unknown agent dir", remote, "/srv/project", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, _ := tt.s.checkWorkspaceRunDir("chat1", tt.dir)
			assert.Equal(t, tt.want, code)
		})
	}
}
