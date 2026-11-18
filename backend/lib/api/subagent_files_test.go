package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// sharedRig is a spawn tree root -> mid -> leaf plus an unrelated tree and a
// plain chat, over a private data dir (runner-less Local file system).
type sharedRig struct {
	server                *Server
	repo                  *dbmodels.SessionRepository
	root, mid, leaf       string
	otherRoot, otherChild string
	plain                 string
	workspace             string
}

func newSharedRig(t *testing.T) *sharedRig {
	t.Helper()
	t.Setenv(runnerpaths.EnvDataDir, t.TempDir())
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	repo.SetSessionDirFunc(runnerpaths.SessionDir)
	server := &Server{conf: &config.Config{Host: "http://localhost:8080"}, repo: repo, eventHub: NewSessionEventHub()}
	server.mux = server.buildMuxLocked()
	t.Cleanup(server.eventHub.Close)

	rig := &sharedRig{
		server: server, repo: repo, workspace: t.TempDir(),
		root: "root1", mid: "mid1", leaf: "leaf1", otherRoot: "oroot", otherChild: "ochild", plain: "plain1",
	}
	save := func(id, parent, agent, label string) {
		require.NoError(t, repo.SaveSession(&dbmodels.Session{
			ChatID: id, ParentChatID: parent, CurrentAgent: agent, SpawnLabel: label, RunDir: rig.workspace,
		}))
	}
	save(rig.root, "", "lead", "")
	save(rig.mid, rig.root, "coder", "mid label")
	save(rig.leaf, rig.mid, "tester", "")
	save(rig.otherRoot, "", "lead", "")
	save(rig.otherChild, rig.otherRoot, "coder", "other")
	save(rig.plain, "", "lead", "")
	return rig
}

func (r *sharedRig) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func q(kv ...string) string {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v.Encode()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// A subagent is a chat of its own: its /tmp and /session are its own dirs and
// the file APIs serve them under the child's session id.
func TestSubagentChatServesItsOwnTmpAndSession(t *testing.T) {
	rig := newSharedRig(t)
	writeFile(t, filepath.Join(runnerpaths.SessionTmpDir(rig.leaf), "out", "r.txt"), "leaf tmp")
	writeFile(t, filepath.Join(runnerpaths.SessionDir(rig.leaf), "n.md"), "leaf session")
	writeFile(t, filepath.Join(runnerpaths.SessionTmpDir(rig.root), "r.txt"), "root tmp")
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/tmp/out/r.txt", "/session/n.md"}))

	for path, want := range map[string]string{"/tmp/out/r.txt": "leaf tmp", "/session/n.md": "leaf session"} {
		rec := rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.leaf, "path", path))
		require.Equal(t, http.StatusOK, rec.Code, path+rec.Body.String())
		var resp WorkspaceFileResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, want, resp.Content)
	}

	rec := rig.get(t, "/api/files/tree?"+q("session_id", rig.leaf, "path", "/tmp"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var tree FileTreeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tree))
	require.Len(t, tree.Entries, 1)
	assert.Equal(t, "/tmp/out", tree.Entries[0].Path)

	// The parent does not see the child's tmp under its own id.
	rec = rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.root, "path", "/tmp/out/r.txt"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// The artifacts a child records are published on its own stream and on every
// ancestor's, and the ancestors' session responses list them.
func TestSubagentArtifactsReachAncestors(t *testing.T) {
	rig := newSharedRig(t)
	update := AgentStatusUpdate{
		Content: "wrote", EntryType: "activity", ArtifactsKnown: true,
		Artifacts: []string{"/tmp/a.txt", "/shared/b.txt"},
	}
	chS, _, cancelS := rig.server.eventHub.Subscribe(rig.leaf, 0)
	defer cancelS()
	chP, _, cancelP := rig.server.eventHub.Subscribe(rig.root, 0)
	defer cancelP()
	chM, _, cancelM := rig.server.eventHub.Subscribe(rig.mid, 0)
	defer cancelM()

	recordStatusUpdate(rig.server, rig.repo, rig.leaf, update, nil, rig.workspace)

	next := func(ch <-chan SessionEvent, typ string) SessionEvent {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case ev := <-ch:
				if ev.Type == typ {
					return ev
				}
			case <-deadline:
				t.Fatalf("no %s event", typ)
			}
		}
	}
	own := next(chS, EventTypeArtifact)
	assert.Equal(t, rig.leaf, own.ChatID)
	for _, ch := range []<-chan SessionEvent{chP, chM} {
		ev := next(ch, EventTypeSubagentArtifact)
		assert.Equal(t, rig.leaf, ev.Payload["id"])
		assert.Equal(t, "tester", ev.Payload["agent"])
		assert.Equal(t, []string{"/tmp/a.txt", "/shared/b.txt"}, ev.Payload["artifacts"])
	}

	for _, id := range []string{rig.root, rig.mid} {
		rec := rig.get(t, "/api/sessions/"+id)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var cs ChatSession
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))
		require.Len(t, cs.SubagentArtifacts, 1, id)
		assert.Equal(t, rig.leaf, cs.SubagentArtifacts[0].ChatID)
		assert.Equal(t, []string{"/tmp/a.txt", "/shared/b.txt"}, cs.SubagentArtifacts[0].Artifacts)
		assert.Empty(t, cs.Artifacts)
	}
	// The leaf itself and an unrelated chat list no descendants.
	rec := rig.get(t, "/api/sessions/"+rig.leaf)
	var cs ChatSession
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))
	assert.Empty(t, cs.SubagentArtifacts)
	assert.Equal(t, []string{"/tmp/a.txt", "/shared/b.txt"}, []string(cs.Artifacts))
}

func TestSubagentArtifactLabelFallsBackToTitle(t *testing.T) {
	rig := newSharedRig(t)
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/tmp/a"}))
	require.NoError(t, rig.repo.AppendArtifacts(rig.mid, []string{"/tmp/m"}))
	got := rig.server.descendantArtifacts(rig.root)
	require.Len(t, got, 2)
	assert.Equal(t, "mid label", got[0].Label)
	assert.Equal(t, "coder", got[0].Agent)
	assert.Equal(t, rig.leaf, got[1].ChatID)
	assert.Equal(t, "", got[1].Label)
}

func TestSharedFilesTreeContentSearch(t *testing.T) {
	rig := newSharedRig(t)
	shared := runnerpaths.SharedChatDir(rig.root)
	writeFile(t, filepath.Join(shared, "plan.md"), "the plan")
	writeFile(t, filepath.Join(shared, "dir", "out.txt"), "deep")
	writeFile(t, filepath.Join(runnerpaths.SharedChatDir(rig.otherRoot), "secret.txt"), "other tree")
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/shared/plan.md"}))

	// Every chat of the tree sees the same directory, by path or by scope.
	for _, id := range []string{rig.root, rig.mid, rig.leaf} {
		for _, params := range [][]string{
			{"session_id", id, "path", "/shared"},
			{"session_id", id, "scope", "shared"},
			{"session_id", id, "scope", "shared", "path", "."},
		} {
			rec := rig.get(t, "/api/files/tree?"+q(params...))
			require.Equal(t, http.StatusOK, rec.Code, id+rec.Body.String())
			var tree FileTreeResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tree))
			require.Len(t, tree.Entries, 2, id)
			assert.Equal(t, "/shared/dir", tree.Entries[0].Path)
			assert.True(t, tree.Entries[0].IsDir)
			assert.Equal(t, "/shared/plan.md", tree.Entries[1].Path)
		}
		rec := rig.get(t, "/api/files/tree?"+q("session_id", id, "path", "/shared/dir"))
		require.Equal(t, http.StatusOK, rec.Code)
		var sub FileTreeResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sub))
		require.Len(t, sub.Entries, 1)
		assert.Equal(t, "/shared/dir/out.txt", sub.Entries[0].Path)

		rec = rig.get(t, "/api/files/content?"+q("session_id", id, "path", "/shared/plan.md"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var fc FileContentResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fc))
		assert.Equal(t, "the plan", fc.Content)
		assert.Equal(t, "/shared/plan.md", fc.Path)

		rec = rig.get(t, "/api/files/content?"+q("session_id", id, "scope", "shared", "path", "dir/out.txt"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rec = rig.get(t, "/api/files/search?"+q("session_id", id, "scope", "shared", "query", "OUT"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var sr FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sr))
		require.Len(t, sr.Files, 1)
		assert.Equal(t, "/shared/dir/out.txt", sr.Files[0].Path)
		assert.Equal(t, "shared", sr.Files[0].Scope)

		// The unscoped search also lists the tree's shared files.
		rec = rig.get(t, "/api/files/search?"+q("session_id", id, "query", "plan"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sr))
		var paths []string
		for _, f := range sr.Files {
			paths = append(paths, f.Path)
		}
		assert.Contains(t, paths, "/shared/plan.md")
	}
}

func TestSharedNeverReachesAnotherTree(t *testing.T) {
	rig := newSharedRig(t)
	writeFile(t, filepath.Join(runnerpaths.SharedChatDir(rig.root), "mine.txt"), "mine")
	writeFile(t, filepath.Join(runnerpaths.SharedChatDir(rig.otherRoot), "secret.txt"), "other tree")
	require.NoError(t, rig.repo.AppendArtifacts(rig.otherChild, []string{"/shared/secret.txt"}))

	// The other tree's own members read their file, ours cannot by any path form.
	rec := rig.get(t, "/api/files/content?"+q("session_id", rig.otherChild, "path", "/shared/secret.txt"))
	require.Equal(t, http.StatusOK, rec.Code)
	for _, p := range []string{
		"/shared/secret.txt",
		"/shared/../oroot/secret.txt",
		"/shared/" + rig.otherRoot + "/secret.txt",
		"/shared/./../../shared/" + rig.otherRoot + "/secret.txt",
		"../" + rig.otherRoot + "/secret.txt",
	} {
		for _, scope := range []string{"", "shared"} {
			rec := rig.get(t, "/api/files/content?"+q("session_id", rig.leaf, "path", p, "scope", scope))
			assert.NotEqual(t, http.StatusOK, rec.Code, "%s %s", scope, p)
			assert.NotContains(t, rec.Body.String(), "other tree")
			rec = rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.leaf, "path", p, "scope", scope))
			assert.NotEqual(t, http.StatusOK, rec.Code, "%s %s", scope, p)
			assert.NotContains(t, rec.Body.String(), "other tree")
		}
	}
	rec = rig.get(t, "/api/files/tree?"+q("session_id", rig.leaf, "scope", "shared", "path", "../"+rig.otherRoot))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = rig.get(t, "/api/files/tree?"+q("session_id", rig.leaf, "path", "/shared"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret.txt")
	rec = rig.get(t, "/api/files/search?"+q("session_id", rig.leaf, "scope", "shared", "query", "secret"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret.txt")
}

func TestSharedSymlinkEscapeRefused(t *testing.T) {
	rig := newSharedRig(t)
	shared := runnerpaths.SharedChatDir(rig.root)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "host.txt"), "host secret")
	require.NoError(t, os.MkdirAll(shared, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(shared, "link")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "host.txt"), filepath.Join(shared, "file-link")))
	require.NoError(t, rig.repo.AppendArtifacts(rig.root, []string{"/shared/link/host.txt", "/shared/file-link"}))

	for _, p := range []string{"/shared/link/host.txt", "/shared/file-link"} {
		rec := rig.get(t, "/api/files/content?"+q("session_id", rig.root, "path", p))
		assert.NotEqual(t, http.StatusOK, rec.Code, p)
		assert.NotContains(t, rec.Body.String(), "host secret")
		rec = rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.root, "path", p))
		assert.NotEqual(t, http.StatusOK, rec.Code, p)
		assert.NotContains(t, rec.Body.String(), "host secret")
	}
	rec := rig.get(t, "/api/files/tree?"+q("session_id", rig.root, "path", "/shared/link"))
	assert.NotEqual(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "host.txt")
}

func TestSharedWithoutSpawnTreeIsNotFound(t *testing.T) {
	rig := newSharedRig(t)
	for _, target := range []string{
		"/api/files/tree?" + q("session_id", rig.plain, "path", "/shared"),
		"/api/files/tree?" + q("session_id", rig.plain, "scope", "shared"),
		"/api/files/content?" + q("session_id", rig.plain, "path", "/shared/x.txt"),
		"/api/v1/workspace/file?" + q("session_id", rig.plain, "path", "/shared/x.txt"),
	} {
		rec := rig.get(t, target)
		assert.Contains(t, []int{http.StatusNotFound, http.StatusForbidden}, rec.Code, target)
	}
	rec := rig.get(t, "/api/files/search?"+q("session_id", rig.plain, "scope", "shared", "query", "x"))
	require.Equal(t, http.StatusOK, rec.Code)
	var sr FileSearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sr))
	assert.Empty(t, sr.Files)
	rec = rig.get(t, "/api/files/tree?"+q("session_id", rig.plain, "path", "/shared"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = rig.get(t, "/api/files/tree?"+q("session_id", "missing1", "path", "/shared"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Any file in /shared is readable from any chat of the same spawn tree (even
// if written via Bash without an explicit tool artifact), never from another
// tree or a plain chat without /shared.
func TestSharedWorkspaceFileAuthorization(t *testing.T) {
	rig := newSharedRig(t)
	shared := runnerpaths.SharedChatDir(rig.root)
	writeFile(t, filepath.Join(shared, "ok.txt"), "authorized")
	writeFile(t, filepath.Join(shared, "no.txt"), "written via bash")
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/shared/ok.txt"}))

	for _, id := range []string{rig.root, rig.mid, rig.leaf} {
		rec := rig.get(t, "/api/v1/workspace/file?"+q("session_id", id, "path", "/shared/ok.txt"))
		require.Equal(t, http.StatusOK, rec.Code, id+rec.Body.String())
		var resp WorkspaceFileResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "authorized", resp.Content)
		assert.Equal(t, "/shared/ok.txt", resp.Path)

		rec = rig.get(t, "/api/v1/workspace/file?"+q("session_id", id, "path", "/shared/no.txt"))
		require.Equal(t, http.StatusOK, rec.Code, id+rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "written via bash", resp.Content)
		assert.Equal(t, "/shared/no.txt", resp.Path)
	}
	// Another tree does not see this tree's /shared files.
	rec := rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.otherChild, "path", "/shared/ok.txt"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	// A plain chat without /shared is forbidden.
	rec = rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.plain, "path", "/shared/ok.txt"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// searchSpyFS answers searches like a runner that predates the shared scope
// (it ignores it) and records the requests.
type searchSpyFS struct {
	runnerfs.Local
	mu    sync.Mutex
	reqs  []runnerfs.SearchRequest
	reply []runnerfs.SearchHit
	// realistic answers like a current runner: by scope, at most Limit hits.
	realistic bool
}

func (f *searchSpyFS) Search(_ context.Context, req runnerfs.SearchRequest) ([]runnerfs.SearchHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if !f.realistic {
		return f.reply, nil
	}
	var out []runnerfs.SearchHit
	for _, h := range f.reply {
		if (h.Scope == "shared") == (req.Scope == "shared") && len(out) < req.Limit {
			out = append(out, h)
		}
	}
	return out, nil
}

func TestSharedSearchFiltersAndReservesLimit(t *testing.T) {
	rig := newSharedRig(t)
	spy := &searchSpyFS{reply: []runnerfs.SearchHit{
		{Path: "/shared/ok.txt", Name: "ok.txt", Scope: "shared"},
		{Path: "/tmp/root-tmp.txt", Name: "root-tmp.txt", Scope: "tmp"},
		{Path: "ws.txt", Name: "ws.txt", Scope: "workspace"},
		{Path: "/shared/mislabeled", Name: "m", Scope: "tmp"},
		{Path: "/etc/x", Name: "x", Scope: "shared"},
	}}
	rig.server.fsResolver = func(string) (runnerfs.FS, error) { return spy, nil }

	rec := rig.get(t, "/api/files/search?"+q("session_id", rig.leaf, "scope", "shared", "query", "x"))
	require.Equal(t, http.StatusOK, rec.Code)
	var sr FileSearchResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sr))
	require.Len(t, sr.Files, 1)
	assert.Equal(t, "/shared/ok.txt", sr.Files[0].Path)

	// A chat of a tree: a quarter of the limit is kept for /shared, and only
	// genuine shared hits are appended.
	spy.reqs = nil
	rec = rig.get(t, "/api/files/search?"+q("session_id", rig.leaf, "query", "x", "limit", "40"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, spy.reqs, 2)
	assert.Equal(t, 30, spy.reqs[0].Limit)
	assert.Equal(t, "", spy.reqs[0].Scope)
	assert.Equal(t, "shared", spy.reqs[1].Scope)
	assert.Equal(t, 40-5, spy.reqs[1].Limit)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sr))
	assert.Len(t, sr.Files, 5+1)

	// A chat outside any tree never asks for /shared and keeps the full limit.
	spy.reqs = nil
	rec = rig.get(t, "/api/files/search?"+q("session_id", rig.plain, "query", "x", "limit", "40"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, spy.reqs, 1)
	assert.Equal(t, 40, spy.reqs[0].Limit)
	assert.Equal(t, "", spy.reqs[0].Scope)
}

func TestSessionJSONHasSharedAndCapsDescendantArtifacts(t *testing.T) {
	rig := newSharedRig(t)
	flag := func(id string) ChatSession {
		rec := rig.get(t, "/api/sessions/"+id)
		require.Equal(t, http.StatusOK, rec.Code)
		var cs ChatSession
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))
		return cs
	}
	assert.True(t, flag(rig.root).HasShared, "has descendants")
	assert.True(t, flag(rig.leaf).HasShared, "is a subagent")
	assert.False(t, flag(rig.plain).HasShared)

	many := make([]string, maxArtifactsPerSubagent+7)
	for i := range many {
		many[i] = fmt.Sprintf("/tmp/f%d", i)
	}
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, many))
	require.NoError(t, rig.repo.AppendArtifacts(rig.mid, []string{"/tmp/m"}))
	got := flag(rig.root).SubagentArtifacts
	require.Len(t, got, 2)
	assert.False(t, got[0].Truncated)
	assert.True(t, got[1].Truncated)
	require.Len(t, got[1].Artifacts, maxArtifactsPerSubagent)
	assert.Equal(t, fmt.Sprintf("/tmp/f%d", len(many)-1), got[1].Artifacts[maxArtifactsPerSubagent-1])
}

func TestSharedExplicitOtherScopeIsNotShared(t *testing.T) {
	rig := newSharedRig(t)
	writeFile(t, filepath.Join(runnerpaths.SharedChatDir(rig.root), "mine.txt"), "shared content")
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/shared/mine.txt"}))
	for _, scope := range []string{"tmp", "session", "workspace"} {
		for _, target := range []string{
			"/api/files/content?" + q("session_id", rig.leaf, "path", "/shared/mine.txt", "scope", scope),
			"/api/v1/workspace/file?" + q("session_id", rig.leaf, "path", "/shared/mine.txt", "scope", scope),
			"/api/files/tree?" + q("session_id", rig.leaf, "path", "/shared", "scope", scope),
		} {
			rec := rig.get(t, target)
			assert.NotEqual(t, http.StatusOK, rec.Code, target)
			assert.NotContains(t, rec.Body.String(), "shared content")
		}
	}
}

// When the root chat of a subtree was deleted, the topmost chat left is the
// root, in the file API as in the sandbox mounts.
func TestSharedRootFallbackWhenRootDeleted(t *testing.T) {
	rig := newSharedRig(t)
	require.NoError(t, rig.repo.DeleteSessionRecord(rig.root))
	root, err := rig.server.sharedRoot(rig.leaf, mustMeta(t, rig, rig.leaf))
	require.NoError(t, err)
	assert.Equal(t, rig.mid, root)

	writeFile(t, filepath.Join(runnerpaths.SharedChatDir(rig.mid), "f.txt"), "from mid")
	rec := rig.get(t, "/api/files/content?"+q("session_id", rig.leaf, "path", "/shared/f.txt"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "from mid")
}

func mustMeta(t *testing.T, rig *sharedRig, id string) *dbmodels.Session {
	t.Helper()
	sess, err := rig.repo.GetSessionMeta(id)
	require.NoError(t, err)
	require.NotNil(t, sess)
	return sess
}

// The viewer opens a subagent's artifact with the subagent's session id, so the
// runner assignment check applies to the child: a child on a runner the user
// does not have is refused, however the parent's runner is assigned.
func TestSubagentArtifactDeniedOnUnassignedRunner(t *testing.T) {
	t.Setenv(runnerpaths.EnvDataDir, t.TempDir())
	srv, p := newAccessServer(t)
	user := bearer(t, p, "user-1")
	_ = doJSON(srv, http.MethodGet, "/api/runners", user, nil) // records the user
	require.NoError(t, srv.access.Assign("runner-a", "user-1", "admin"))

	require.NoError(t, srv.repo.SaveSession(&dbmodels.Session{ChatID: "par1", RunnerNodeID: "runner-a", RunDir: t.TempDir()}))
	require.NoError(t, srv.repo.SaveSession(&dbmodels.Session{ChatID: "kid1", ParentChatID: "par1", RunnerNodeID: "runner-b", RunDir: t.TempDir()}))
	writeFile(t, filepath.Join(runnerpaths.SessionTmpDir("kid1"), "a.txt"), "child data")
	require.NoError(t, srv.repo.AppendArtifacts("kid1", []string{"/tmp/a.txt"}))
	writeFile(t, filepath.Join(runnerpaths.SharedChatDir("par1"), "s.txt"), "shared data")
	require.NoError(t, srv.repo.AppendArtifacts("kid1", []string{"/shared/s.txt"}))

	for _, target := range []string{
		"/api/v1/workspace/file?" + q("session_id", "kid1", "path", "/tmp/a.txt"),
		"/api/v1/workspace/file?" + q("session_id", "kid1", "path", "/shared/s.txt"),
		"/api/files/tree?" + q("session_id", "kid1", "path", "/shared"),
		"/api/files/content?" + q("session_id", "kid1", "path", "/shared/s.txt"),
	} {
		rec := doJSON(srv, http.MethodGet, target, user, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, target)
		assert.NotContains(t, rec.Body.String(), "child data")
		assert.NotContains(t, rec.Body.String(), "shared data")
	}
	// The parent's own runner is fine.
	rec := doJSON(srv, http.MethodGet, "/api/files/content?"+q("session_id", "par1", "path", "/shared/s.txt"), user, nil)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestSharedSearchSmallLimits(t *testing.T) {
	rig := newSharedRig(t)
	spy := &searchSpyFS{realistic: true, reply: []runnerfs.SearchHit{
		{Path: "a.txt", Name: "a.txt", Scope: "workspace"},
		{Path: "b.txt", Name: "b.txt", Scope: "workspace"},
		{Path: "/shared/c.txt", Name: "c.txt", Scope: "shared"},
	}}
	rig.server.fsResolver = func(string) (runnerfs.FS, error) { return spy, nil }
	for limit, want := range map[int]struct{ calls, first, files int }{
		1: {1, 1, 1}, // nothing reserved: a 0 would mean the runner default
		2: {2, 1, 2},
		3: {2, 2, 3},
	} {
		spy.reqs = nil
		rec := rig.get(t, "/api/files/search?"+q("session_id", rig.leaf, "query", "x", "limit", fmt.Sprint(limit)))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Len(t, spy.reqs, want.calls, "limit %d", limit)
		assert.Equal(t, want.first, spy.reqs[0].Limit, "limit %d", limit)
		var sr FileSearchResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sr))
		assert.LessOrEqual(t, len(sr.Files), limit)
		assert.Len(t, sr.Files, want.files, "limit %d", limit)
	}
}

func TestChatHasSharedTeamBranch(t *testing.T) {
	rig := newSharedRig(t)
	rig.server.agents = []*agentspec.Agent{
		{Config: agentspec.AgentConfig{ID: "lead", Team: "crew"}},
		{Config: agentspec.AgentConfig{ID: "solo"}},
	}
	team := mustMeta(t, rig, rig.plain) // a root without children, current agent "lead"
	assert.True(t, rig.server.chatHasShared(team), "team root without children")

	team.CurrentAgent = "solo"
	assert.False(t, rig.server.chatHasShared(team), "root of an agent without a team")
	team.CurrentAgent = "ghost"
	assert.False(t, rig.server.chatHasShared(team), "agent not found")
	assert.False(t, rig.server.chatHasShared(nil))

	rig.server.agents = nil
	assert.False(t, rig.server.chatHasShared(mustMeta(t, rig, rig.plain)))
}

func TestResolvedNamespaceHonoursExplicitScope(t *testing.T) {
	h := runnerfs.Header{Path: "/shared/x"}
	assert.Equal(t, "shared", resolvedNamespace(h, ""))
	assert.Equal(t, "shared", resolvedNamespace(h, "shared"))
	for _, scope := range []string{"workspace", "tmp", "session"} {
		assert.Equal(t, "", resolvedNamespace(h, scope), scope)
	}
	assert.Equal(t, "tmp", resolvedNamespace(runnerfs.Header{Path: "/tmp/x"}, "workspace"))
	assert.Equal(t, "", resolvedNamespace(runnerfs.Header{Path: "src/x"}, ""))
}

func TestSessionJSONListsUnrecordedSharedFiles(t *testing.T) {
	rig := newSharedRig(t)
	shared := runnerpaths.SharedChatDir(rig.root)
	writeFile(t, filepath.Join(shared, "bs", "r1", "gemini.md"), "gemini view")
	writeFile(t, filepath.Join(shared, "bs", "r1", "claude.md"), "claude view")
	writeFile(t, filepath.Join(shared, "bs", "brief.md"), "brief")
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/shared/bs/r1/gemini.md"}))

	rec := rig.get(t, "/api/sessions/"+rig.root)
	require.Equal(t, http.StatusOK, rec.Code)
	var cs ChatSession
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cs))

	assert.True(t, cs.HasShared)
	// SubagentArtifacts keeps gemini's attribution for gemini.md.
	require.Len(t, cs.SubagentArtifacts, 1)
	assert.Equal(t, []string{"/shared/bs/r1/gemini.md"}, cs.SubagentArtifacts[0].Artifacts)
	// Unrecorded /shared files (brief.md, claude.md) are merged into Artifacts,
	// without duplicating gemini.md.
	assert.ElementsMatch(t, []string{"/shared/bs/brief.md", "/shared/bs/r1/claude.md"}, []string(cs.Artifacts))

	// A plain chat without /shared never lists /shared files.
	rec = rig.get(t, "/api/sessions/"+rig.plain)
	require.Equal(t, http.StatusOK, rec.Code)
	var plainCS ChatSession
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plainCS))
	assert.False(t, plainCS.HasShared)
	assert.Empty(t, plainCS.Artifacts)
}

func TestSyncSessionArtifactsOnCompletion(t *testing.T) {
	rig := newSharedRig(t)
	shared := runnerpaths.SharedChatDir(rig.root)
	writeFile(t, filepath.Join(shared, "r1", "gemini.md"), "gemini")
	writeFile(t, filepath.Join(shared, "r1", "claude.md"), "claude")
	require.NoError(t, rig.repo.AppendArtifacts(rig.leaf, []string{"/shared/r1/gemini.md"}))

	// Also write user files and internal runtime files in mid's /session and /tmp.
	midSessDir := runnerpaths.SessionDir(rig.mid)
	writeFile(t, filepath.Join(midSessDir, "notes.md"), "session note")
	writeFile(t, filepath.Join(midSessDir, "AW_AGENTS.md"), "contract")
	writeFile(t, filepath.Join(midSessDir, "messages.jsonl"), "{}")

	midTmpDir := runnerpaths.SessionTmpDir(rig.mid)
	writeFile(t, filepath.Join(midTmpDir, "scratch.txt"), "tmp scratch")
	writeFile(t, filepath.Join(midTmpDir, "claude-notes.md"), "user notes")
	writeFile(t, filepath.Join(midTmpDir, "claude-1000", "state.json"), "{}")
	writeFile(t, filepath.Join(midTmpDir, ".aw_agents.md"), "internal")
	writeFile(t, filepath.Join(midTmpDir, "asgard-ssh-agent.sock"), "")
	writeFile(t, filepath.Join(midTmpDir, "fakebash-outputs", "out.log"), "bash log")

	chRoot, _, cancelRoot := rig.server.eventHub.Subscribe(rig.root, 0)
	t.Cleanup(cancelRoot)
	chMid, _, cancelMid := rig.server.eventHub.Subscribe(rig.mid, 0)
	t.Cleanup(cancelMid)

	synced := rig.server.syncSessionArtifacts(context.Background(), rig.mid)
	want := []string{"/shared/r1/claude.md", "/session/notes.md", "/tmp/scratch.txt", "/tmp/claude-notes.md"}
	assert.ElementsMatch(t, want, synced)

	// Only unrecorded /shared, /session, and /tmp user files are appended to rig.mid.
	midSess, err := rig.repo.GetSessionMeta(rig.mid)
	require.NoError(t, err)
	assert.ElementsMatch(t, want, []string(midSess.Artifacts))

	next := func(ch <-chan SessionEvent, typ string) SessionEvent {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case ev := <-ch:
				if ev.Type == typ {
					return ev
				}
			case <-deadline:
				t.Fatalf("no %s event", typ)
			}
		}
	}

	// Both artifact (on mid) and subagent_artifact (on root) events fire.
	evMid := next(chMid, EventTypeArtifact)
	assert.ElementsMatch(t, want, evMid.Payload["artifacts"])
	evRoot := next(chRoot, EventTypeSubagentArtifact)
	assert.Equal(t, rig.mid, evRoot.Payload["id"])
	assert.ElementsMatch(t, want, evRoot.Payload["artifacts"])

	// Synced /session and /tmp files are openable via /api/v1/workspace/file.
	for _, p := range []string{"/session/notes.md", "/tmp/scratch.txt"} {
		rec := rig.get(t, "/api/v1/workspace/file?"+q("session_id", rig.mid, "path", p))
		assert.Equal(t, http.StatusOK, rec.Code, p+": "+rec.Body.String())
	}

	// Running syncSessionArtifacts again is a no-op (idempotent).
	assert.Empty(t, rig.server.syncSessionArtifacts(context.Background(), rig.mid))
	midSess, err = rig.repo.GetSessionMeta(rig.mid)
	require.NoError(t, err)
	assert.ElementsMatch(t, want, []string(midSess.Artifacts))
}
