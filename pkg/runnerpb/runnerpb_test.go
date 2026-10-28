package runnerpb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestProtocolVersion(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, ProtocolVersion)
}

func TestBackendMessage_StartRunRoundTrip(t *testing.T) {
	t.Parallel()

	in := &BackendMessage{
		Seq: 7,
		Payload: &BackendMessage_StartRun{StartRun: &StartRun{
			RunId:       "r1",
			ChatId:      "c1",
			AgentId:     "a1",
			AgentSource: AgentSource_AGENT_SOURCE_RUNNER,
			AgentHash:   "abc",
			Candidates:  []*Target{{Cli: "claude", Model: "opus"}},
			Prompt:      "hi",
			Sessions:    map[string]string{"claude": "s1"},
			StatusScope: &StatusScope{NodeId: "n", RunToken: "t", Headless: true},
		}},
	}
	b, err := proto.Marshal(in)
	require.NoError(t, err)

	out := &BackendMessage{}
	require.NoError(t, proto.Unmarshal(b, out))
	assert.True(t, proto.Equal(in, out))
	assert.Equal(t, "r1", out.GetStartRun().GetRunId())
	assert.Equal(t, "s1", out.GetStartRun().GetSessions()["claude"])
}

func TestRunnerMessage_RunResultNoQuota(t *testing.T) {
	t.Parallel()

	in := &RunnerMessage{Seq: 2, Payload: &RunnerMessage_RunResult{RunResult: &RunResult{
		RunId:   "r1",
		NoQuota: &NoQuota{AgentId: "a", Targets: []*TargetQuota{{Cli: "c", Model: "m", Remaining: 0.1, Enabled: true}}},
	}}}
	b, err := proto.Marshal(in)
	require.NoError(t, err)

	out := &RunnerMessage{}
	require.NoError(t, proto.Unmarshal(b, out))
	assert.InDelta(t, 0.1, out.GetRunResult().GetNoQuota().GetTargets()[0].GetRemaining(), 1e-9)
}

func TestRunnerMessage_RegisterRoundTrip(t *testing.T) {
	t.Parallel()

	in := &RunnerMessage{Payload: &RunnerMessage_Register{Register: &Register{
		NodeId:               "n1",
		ProtocolVersion:      ProtocolVersion,
		RunnerBootId:         "boot",
		ActiveRuns:           []*RunRef{{RunId: "a", Attempt: 2}},
		CompletedUnackedRuns: []*RunRef{{RunId: "b", ChatId: "c"}},
		LastAckedBackendSeq:  3,
	}}}
	b, err := proto.Marshal(in)
	require.NoError(t, err)

	out := &RunnerMessage{}
	require.NoError(t, proto.Unmarshal(b, out))
	assert.Zero(t, out.GetSeq())
	assert.Equal(t, "boot", out.GetRegister().GetRunnerBootId())
	assert.Equal(t, "b", out.GetRegister().GetCompletedUnackedRuns()[0].GetRunId())
}

func TestRunCommandFrame_ExitArtifactsRoundTrip(t *testing.T) {
	t.Parallel()

	req := &RunCommandRequest{
		ChatId:     "c",
		Command:    "echo hi",
		OutputFile: "out.txt",
		Tmp:        &DirRef{Scope: FileScope_FILE_SCOPE_TMP},
		RunDir:     &DirRef{Scope: FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "/workspace"},
	}
	b, err := proto.Marshal(req)
	require.NoError(t, err)
	gotReq := &RunCommandRequest{}
	require.NoError(t, proto.Unmarshal(b, gotReq))
	assert.True(t, proto.Equal(req, gotReq))
	assert.Nil(t, gotReq.Sandbox)

	frame := &RunCommandFrame{Payload: &RunCommandFrame_Exit{Exit: &RunCommandExit{
		Artifacts: []*Artifact{{ViewerPath: "/tmp/out.txt", Name: "out.txt", AbsPath: "/home/x/tmp/c/out.txt"}},
	}}}
	b, err = proto.Marshal(frame)
	require.NoError(t, err)
	got := &RunCommandFrame{}
	require.NoError(t, proto.Unmarshal(b, got))
	assert.Equal(t, "out.txt", got.GetExit().GetArtifacts()[0].GetName())
}

func TestStartRun_OverlayAndAttempt(t *testing.T) {
	t.Parallel()

	in := &StartRun{RunId: "r2", Attempt: 2, RunDirs: []string{"/w"}, MountDirsReadwrite: []string{"/rw"}, RunDir: &DirRef{Scope: FileScope_FILE_SCOPE_SESSION, RelOrPath: "x"}}
	b, err := proto.Marshal(in)
	require.NoError(t, err)
	out := &StartRun{}
	require.NoError(t, proto.Unmarshal(b, out))
	assert.True(t, proto.Equal(in, out))
	assert.EqualValues(t, 2, out.GetAttempt())
}

func TestRegister_LastSeenBackendBootID(t *testing.T) {
	t.Parallel()

	in := &Register{LastSeenBackendBootId: "bb", LastAckedBackendSeq: 9}
	b, err := proto.Marshal(in)
	require.NoError(t, err)
	out := &Register{}
	require.NoError(t, proto.Unmarshal(b, out))
	assert.Equal(t, "bb", out.GetLastSeenBackendBootId())
}

func TestRunOutputAndEventRoundTrip(t *testing.T) {
	t.Parallel()

	msgs := []*RunnerMessage{
		{Seq: 1, Payload: &RunnerMessage_RunEvent{RunEvent: &RunEvent{RunId: "r", EventIndex: 4}}},
		{Seq: 2, Payload: &RunnerMessage_RunOutput{RunOutput: &RunOutputChunk{RunId: "r", Offset: 10, Data: []byte("x")}}},
		{Seq: 3, Payload: &RunnerMessage_RunResult{RunResult: &RunResult{RunId: "r", OutputSize: 11, ChatId: "c", Attempt: 2, WorkflowNodeId: "n", RunToken: "t"}}},
	}
	for _, in := range msgs {
		b, err := proto.Marshal(in)
		require.NoError(t, err)
		out := &RunnerMessage{}
		require.NoError(t, proto.Unmarshal(b, out))
		assert.True(t, proto.Equal(in, out))
	}
}

func TestCleanupAndRequestID(t *testing.T) {
	t.Parallel()

	in := &CleanupRequest{RequestId: "q1", OrphanTmpSweeps: []*OrphanTmpSweep{{Dir: &DirRef{Scope: FileScope_FILE_SCOPE_SESSION, RelOrPath: "workflows"}, ChatId: "c"}}}
	b, err := proto.Marshal(in)
	require.NoError(t, err)
	out := &CleanupRequest{}
	require.NoError(t, proto.Unmarshal(b, out))
	assert.True(t, proto.Equal(in, out))
	assert.Equal(t, "q1", out.GetRequestId())
	assert.Equal(t, "c", (&TtydOpen{ChatId: "c"}).GetChatId())
}

func TestRunCommand_ResumeOffsetsAndStreamData(t *testing.T) {
	t.Parallel()

	req := &RunCommandRequest{RequestId: "q", ResumeStdoutOffset: 5, ResumeStderrOffset: 7}
	b, err := proto.Marshal(req)
	require.NoError(t, err)
	gotReq := &RunCommandRequest{}
	require.NoError(t, proto.Unmarshal(b, gotReq))
	assert.EqualValues(t, 7, gotReq.GetResumeStderrOffset())
	assert.EqualValues(t, 5, gotReq.GetResumeStdoutOffset())

	frame := &RunCommandFrame{Payload: &RunCommandFrame_Stdout{Stdout: &StreamData{Offset: 5, Data: []byte("hi")}}}
	b, err = proto.Marshal(frame)
	require.NoError(t, err)
	got := &RunCommandFrame{}
	require.NoError(t, proto.Unmarshal(b, got))
	assert.EqualValues(t, 5, got.GetStdout().GetOffset())
}

func TestGitDiffFileAndPaging(t *testing.T) {
	t.Parallel()

	f := &GitDiffFile{NewPath: "a", ContentTruncated: true}
	b, err := proto.Marshal(f)
	require.NoError(t, err)
	got := &GitDiffFile{}
	require.NoError(t, proto.Unmarshal(b, got))
	assert.True(t, got.GetContentTruncated())

	r := &TreeRequest{Limit: 10, PageToken: "p"}
	b, err = proto.Marshal(r)
	require.NoError(t, err)
	gr := &TreeRequest{}
	require.NoError(t, proto.Unmarshal(b, gr))
	assert.Equal(t, "p", gr.GetPageToken())
	assert.EqualValues(t, 10, gr.GetLimit())
}

func TestAgentSourceDense(t *testing.T) {
	t.Parallel()
	assert.EqualValues(t, 1, AgentSource_AGENT_SOURCE_RUNNER)
	assert.True(t, (&GitDiffFile{HunksTruncated: true}).GetHunksTruncated())
	assert.True(t, (&RegisterResult{RejectReasonTruncated: true}).GetRejectReasonTruncated())
}
