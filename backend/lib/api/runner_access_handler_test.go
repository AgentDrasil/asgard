package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/auth/authtest"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
)

func newAccessServer(t *testing.T) (*Server, *authtest.Provider) {
	t.Helper()
	p := authtest.NewProvider(t)
	svc, err := auth.New(context.Background(), &auth.Config{
		Issuer: p.URL(), ClientID: authtest.ClientID, ClientSecret: "secret-1",
	}, p.URL())
	require.NoError(t, err)

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	srv := &Server{
		conf:    &config.Config{Host: p.URL()},
		auth:    svc,
		access:  dbmodels.NewRunnerAccessRepository(testDB),
		runners: fakeRunnerRegistry{nodes: []runnerhub.NodeInfo{{NodeID: "runner-a", Online: true}, {NodeID: "runner-b"}}},
	}
	srv.mux = srv.buildMuxLocked()
	return srv, p
}

func bearer(t *testing.T, p *authtest.Provider, sub string, roles ...string) map[string]string {
	t.Helper()
	claims := p.Claims()
	claims["sub"] = sub
	claims["email"] = sub + "@example.com"
	if len(roles) > 0 {
		claims["roles"] = roles
	}
	return map[string]string{"Authorization": "Bearer " + p.Sign(t, claims)}
}

func doJSON(srv *Server, method, target string, headers map[string]string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func runnerIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	var got RunnersResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	ids := []string{}
	for _, r := range got.Runners {
		ids = append(ids, r.NodeID)
	}
	return ids
}

func TestRunnerAccess_AdminAssignsRunnerToUser(t *testing.T) {
	t.Parallel()
	srv, p := newAccessServer(t)
	admin := bearer(t, p, "admin-1", "admin")
	user := bearer(t, p, "user-1")

	// A new user sees nothing and is recorded by logging in.
	assert.Empty(t, runnerIDs(t, doJSON(srv, http.MethodGet, "/api/runners", user, nil)))
	_ = doJSON(srv, http.MethodGet, "/api/runners", admin, nil)

	var manage ManageRunnersResponse
	rec := doJSON(srv, http.MethodGet, "/api/manage/runners", admin, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &manage))
	require.Len(t, manage.Runners, 2)
	for _, r := range manage.Runners {
		assert.Empty(t, r.Users, "a runner starts unassigned")
	}
	subjects := []string{}
	for _, u := range manage.Users {
		subjects = append(subjects, u.Subject)
	}
	assert.ElementsMatch(t, []string{"user-1", "admin-1"}, subjects)

	rec = doJSON(srv, http.MethodPut, "/api/manage/runners/runner-a/users", admin, SetRunnerUsersRequest{Users: []string{"user-1"}})
	require.Equal(t, http.StatusNoContent, rec.Code)

	assert.Equal(t, []string{"runner-a"}, runnerIDs(t, doJSON(srv, http.MethodGet, "/api/runners", user, nil)))
	assert.Empty(t, runnerIDs(t, doJSON(srv, http.MethodGet, "/api/runners", admin, nil)), "admin role alone grants no runner")

	rec = doJSON(srv, http.MethodPut, "/api/manage/runners/runner-a/users", admin, SetRunnerUsersRequest{Users: []string{}})
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, runnerIDs(t, doJSON(srv, http.MethodGet, "/api/runners", user, nil)))
}

func TestRunnerAccess_ManageEndpointsFailClosed(t *testing.T) {
	t.Parallel()
	srv, p := newAccessServer(t)
	admin := bearer(t, p, "admin-1", "admin")
	user := bearer(t, p, "user-1")
	_ = doJSON(srv, http.MethodGet, "/api/runners", user, nil)

	tests := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		body    any
		want    int
	}{
		{"non-admin list", http.MethodGet, "/api/manage/runners", user, nil, http.StatusForbidden},
		{"non-admin set", http.MethodPut, "/api/manage/runners/runner-a/users", user, SetRunnerUsersRequest{Users: []string{"user-1"}}, http.StatusForbidden},
		{"no token", http.MethodGet, "/api/manage/runners", nil, nil, http.StatusUnauthorized},
		{"unknown runner", http.MethodPut, "/api/manage/runners/nope/users", admin, SetRunnerUsersRequest{}, http.StatusNotFound},
		{"unknown user", http.MethodPut, "/api/manage/runners/runner-a/users", admin, SetRunnerUsersRequest{Users: []string{"ghost"}}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := doJSON(srv, tt.method, tt.path, tt.headers, tt.body)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestAuthorizeRunner(t *testing.T) {
	t.Parallel()
	srv, _ := newAccessServer(t)
	require.NoError(t, srv.access.Assign("runner-a", "user-1", "admin"))

	reqFor := func(sub string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
		if sub == "" {
			return r
		}
		return r.WithContext(auth.WithUser(r.Context(), &auth.User{Subject: sub}))
	}

	tests := []struct {
		name   string
		sub    string
		runner string
		want   int // 0 = allowed
	}{
		{"assigned", "user-1", "runner-a", 0},
		{"other runner", "user-1", "runner-b", http.StatusForbidden},
		{"other user", "user-2", "runner-a", http.StatusForbidden},
		{"anonymous", "", "runner-a", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := srv.authorizeRunner(reqFor(tt.sub), tt.runner)
			if tt.want == 0 {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, tt.want, err.status)
		})
	}

	noDB := &Server{}
	require.NotNil(t, noDB.authorizeRunner(reqFor("user-1"), "runner-a"), "no database fails closed")
}

func TestRunnerAccess_RevokedUserCannotUseBoundChat(t *testing.T) {
	f := setupBindingFixture(t)
	require.Equal(t, http.StatusAccepted, f.post(t, "global-agent", map[string]any{"prompt": "one", "chatId": "chat-rev", "runnerId": "r1"}).Code)
	require.Eventually(t, func() bool { _, busy := f.server.activeExecutions.Load("chat-rev"); return !busy }, 5*time.Second, 10*time.Millisecond)

	_, _, err := f.server.access.SetAssignments("r1", nil, "admin")
	require.NoError(t, err)

	rr := f.post(t, "global-agent", map[string]any{"prompt": "two", "chatId": "chat-rev"})
	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}
