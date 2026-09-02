package workflow

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

func TestExtractArtifactPaths(t *testing.T) {
	tmpDir := t.TempDir()
	runDir := t.TempDir()

	planPath := filepath.Join(tmpDir, "plan", "plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(planPath), 0o755))
	require.NoError(t, os.WriteFile(planPath, []byte("plan"), 0o644))

	reviewPath := filepath.Join(tmpDir, "plan", "review_feedback.md")
	require.NoError(t, os.WriteFile(reviewPath, []byte("feedback"), 0o644))

	srcPath := filepath.Join(runDir, "main.go")
	require.NoError(t, os.WriteFile(srcPath, []byte("code"), 0o644))

	raw := "Please review Plan (${tmp_dir}/plan/plan.md) and Review Feedback (${tmp_dir}/plan/review_feedback.md)."
	interpolated := "Please review Plan (" + planPath + ") and Review Feedback (" + reviewPath + "). Also see " + srcPath + "."

	// ${tmp_dir} references resolve even when interpolation already ran, and
	// absolute paths under run_dir are picked up from the interpolated text.
	got := ExtractArtifactPaths(raw, interpolated, tmpDir, runDir)
	assert.Equal(t, []string{planPath, reviewPath, srcPath}, got)

	// Missing files are dropped; duplicates collapse; paths outside the
	// tmp/run dirs are ignored.
	got = ExtractArtifactPaths(
		"see ${tmp_dir}/missing.txt and ${tmp_dir}/plan/plan.md and ${tmp_dir}/plan/plan.md",
		"see /etc/passwd too",
		tmpDir, runDir,
	)
	assert.Equal(t, []string{planPath}, got)
}

func TestExtractArtifactPathsWithSessionDir(t *testing.T) {
	tmpDir := t.TempDir()
	runDir := t.TempDir()
	sessionDir := t.TempDir()

	reportPath := filepath.Join(sessionDir, "report.md")
	require.NoError(t, os.WriteFile(reportPath, []byte("report"), 0o644))

	raw := "See report (${session_dir}/report.md) and tmp (${tmp_dir}/x.md)."
	interpolated := "See report (" + reportPath + ") and tmp (" + filepath.Join(tmpDir, "x.md") + ")."

	got := ExtractArtifactPathsInSession(raw, interpolated, tmpDir, runDir, sessionDir)
	assert.Equal(t, []string{reportPath}, got)

	// Absolute session path in interpolated text is also picked up.
	got2 := ExtractArtifactPathsInSession("", "also "+reportPath, tmpDir, runDir, sessionDir)
	assert.Equal(t, []string{reportPath}, got2)
}

func TestViewerArtifactPath(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "sess")
	require.NoError(t, os.MkdirAll(tmpDir, 0o755))

	assert.Equal(t, "/tmp/plan/plan.md", ViewerArtifactPath(filepath.Join(tmpDir, "plan", "plan.md"), tmpDir))
	assert.Equal(t, "/tmp/decision.txt", ViewerArtifactPath(filepath.Join(tmpDir, "decision.txt"), tmpDir))
	assert.Equal(t, "/tmp/plan/step-4.description.md", ViewerArtifactPath(".tmp/plan/step-4.description.md", tmpDir))
	assert.Equal(t, "/tmp/intend.md", ViewerArtifactPath("/tmp/intend.md", tmpDir))

	outside := filepath.Join(t.TempDir(), "report.md")
	assert.Equal(t, outside, ViewerArtifactPath(outside, tmpDir))
}

func TestArtifactViewerPathsSorted(t *testing.T) {
	tmpDir := t.TempDir()
	got := ArtifactViewerPaths(map[string]string{
		"b.txt": filepath.Join(tmpDir, "b.txt"),
		"a.txt": filepath.Join(tmpDir, "a.txt"),
	}, tmpDir)
	assert.Equal(t, []string{"/tmp/a.txt", "/tmp/b.txt"}, got)
	assert.Nil(t, ArtifactViewerPaths(nil, tmpDir))
}

func TestHumanSuspensionCarriesArtifacts(t *testing.T) {
	engine, store, rec := newTestEngine(t)
	defn, err := workflowspec.ParseDefinition([]byte(humanLoopYAML))
	require.NoError(t, err)

	runDir := t.TempDir()

	var evMu sync.Mutex
	var events []WorkflowEvent
	rc := RunContext{
		SessionID: "chat-art",
		RunID:     "runart",
		RunDir:    runDir,
		EmitEvent: func(ev WorkflowEvent) {
			evMu.Lock()
			events = append(events, ev)
			evMu.Unlock()
		},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = engine.Execute(context.Background(), defn, rc)
	}()

	waitFor(t, func() bool {
		return store.get("runart") != nil && store.get("runart").Status == PersistStatusWaitingHuman
	}, "run should reach WAITING_HUMAN")

	// The suspension request exposes prompt-referenced artifacts as
	// viewer-facing /tmp paths.
	reqs := rec.all()
	require.Len(t, reqs, 1)
	assert.Equal(t, []string{"/tmp/prep.txt"}, reqs[0].Artifacts)

	// The suspended event carries the same artifacts for live streaming.
	evMu.Lock()
	var suspended WorkflowEvent
	found := false
	for _, ev := range events {
		if ev.Type == EventWorkflowSuspended {
			suspended = ev
			found = true
		}
	}
	evMu.Unlock()
	require.True(t, found, "no suspended event captured")
	assert.Equal(t, []string{"/tmp/prep.txt"}, suspended.Artifacts)
	assert.NotEmpty(t, suspended.MessageID)

	_, _, err = engine.Resume(context.Background(), "runart", "Approve")
	require.NoError(t, err)

	<-done
}

func TestDefaultTmpDirUnderHomeTmp(t *testing.T) {
	dir := DefaultTmpDir("sess-1")
	require.True(t, filepath.IsAbs(dir))
	assert.Contains(t, dir, filepath.Join("tmp", "sess-1"))
}

func TestDefaultSessionDirUnderHomeSession(t *testing.T) {
	dir := DefaultSessionDir("sess-1")
	require.True(t, filepath.IsAbs(dir))
	assert.Contains(t, dir, filepath.Join("session", "sess-1"))
}

func TestViewerArtifactPathInSession(t *testing.T) {
	tmpDir := filepath.Join(t.TempDir(), "tmpsess")
	sessionDir := filepath.Join(t.TempDir(), "sess")
	require.NoError(t, os.MkdirAll(tmpDir, 0o755))
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))

	// tmp dir still wins for paths under it
	assert.Equal(t, "/tmp/plan/plan.md", ViewerArtifactPathInSession(filepath.Join(tmpDir, "plan", "plan.md"), tmpDir, sessionDir))
	// session dir paths map to /session/<rel>
	assert.Equal(t, "/session/report.md", ViewerArtifactPathInSession(filepath.Join(sessionDir, "report.md"), tmpDir, sessionDir))
	assert.Equal(t, "/session/notes/deep.md", ViewerArtifactPathInSession(filepath.Join(sessionDir, "notes", "deep.md"), tmpDir, sessionDir))
	// Literal session forms normalize
	assert.Equal(t, "/session/x.md", ViewerArtifactPathInSession(".session/x.md", tmpDir, sessionDir))
	assert.Equal(t, "/session", ViewerArtifactPathInSession(".session", tmpDir, sessionDir))
	assert.Equal(t, "/session/y.md", ViewerArtifactPathInSession("/session/y.md", tmpDir, sessionDir))
	// Outside paths pass through
	outside := filepath.Join(t.TempDir(), "report.md")
	assert.Equal(t, outside, ViewerArtifactPathInSession(outside, tmpDir, sessionDir))
	// ArtifactViewerPathsInSession (output is sorted by host path pre-mapping)
	got := ArtifactViewerPathsInSession(map[string]string{
		"r.md": filepath.Join(sessionDir, "r.md"),
		"t.md": filepath.Join(tmpDir, "t.md"),
	}, tmpDir, sessionDir)
	assert.ElementsMatch(t, []string{"/session/r.md", "/tmp/t.md"}, got)
}
