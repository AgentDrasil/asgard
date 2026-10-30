package dbmodels

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
)

// With runners the backend's own ~/asgard/data/{tmp,sessions} stay empty:
// migrating legacy rows, deleting a record and runner-bound session handling
// create or remove nothing there.
func TestBackendCreatesNoLocalChatDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testDB := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(testDB))

	// A pre-offload workflow row (dag_spec/input stored in the database) used
	// to be backfilled into the backend's session dir.
	require.NoError(t, testDB.Exec("ALTER TABLE workflow_runs ADD COLUMN dag_spec TEXT").Error)
	require.NoError(t, testDB.Exec("ALTER TABLE workflow_runs ADD COLUMN input TEXT").Error)
	require.NoError(t, testDB.Exec("INSERT INTO workflow_runs (run_id, session_id, dag_spec, input) VALUES ('r1', 'chat-legacy', 'name: x', 'in')").Error)
	require.NoError(t, AutoMigrate(testDB))

	repo := NewSessionRepository(testDB)
	require.NoError(t, repo.UpsertSession(&Session{ChatID: "chat-legacy", RunnerNodeID: "r1"}))

	// A local dir of the same chat (e.g. a co-located runner's) survives the
	// record deletion.
	local := runnerpaths.SessionDir("chat-legacy")
	require.NoError(t, os.MkdirAll(local, 0o755))
	require.NoError(t, repo.DeleteSessionRecord("chat-legacy"))
	assert.DirExists(t, local)

	for _, d := range []string{runnerpaths.SessionTmpDir("chat-legacy"), filepath.Join(local, "workflows"), runnerpaths.CABundleDir("chat-legacy")} {
		assert.NoDirExists(t, d)
	}
	// The legacy columns are left untouched (no destructive migration).
	var spec string
	require.NoError(t, testDB.Raw("SELECT dag_spec FROM workflow_runs WHERE run_id = 'r1'").Scan(&spec).Error)
	assert.Equal(t, "name: x", spec)
}
