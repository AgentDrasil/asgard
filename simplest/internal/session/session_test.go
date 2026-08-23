package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func testUser(text string) *types.UserMessage {
	return &types.UserMessage{Content: types.TextOnly(text), Timestamp: 1000}
}

func testAssistant(text string) *types.AssistantMessage {
	return &types.AssistantMessage{
		Content: []types.AssistantContent{
			types.TextContent{Type: types.TypeText, Text: text},
		},
		API:        types.APIOpenAICompat,
		Provider:   "test",
		Model:      "test-model",
		Usage:      types.Usage{TotalTokens: 5},
		StopReason: types.StopStop,
		Timestamp:  2000,
	}
}

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	return New(dir), dir
}

func TestCreateDeferredFlushAndFormat(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf, err := mgr.Create("/home/user/proj", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sf.Path() == "" {
		t.Fatal("expected persisted path")
	}
	if _, err := sf.AppendMessage(testUser("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sf.Path()); !os.IsNotExist(err) {
		t.Fatalf("file must not exist before first assistant message")
	}
	if _, err := sf.AppendMessage(testAssistant("hi")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sf.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	var h Header
	if err := json.Unmarshal([]byte(lines[0]), &h); err != nil {
		t.Fatal(err)
	}
	if h.Type != TypeSession || h.Version != CurrentVersion || h.ID != sf.Header().ID || h.CWD != "/home/user/proj" {
		t.Fatalf("bad header: %+v", h)
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Type != TypeMessage || e.ParentID != nil || e.ID == "" {
		t.Fatalf("bad first entry: %+v", e)
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(e.Message, &msg); err != nil || msg.Role != "user" {
		t.Fatalf("bad message envelope: %s %v", msg.Role, err)
	}
	if strings.TrimSpace(string(msg.Content)) != `"hello"` {
		t.Fatalf("unexpected content: %s", msg.Content)
	}
	var e2 Entry
	if err := json.Unmarshal([]byte(lines[2]), &e2); err != nil {
		t.Fatal(err)
	}
	if e2.ParentID == nil || *e2.ParentID != e.ID {
		t.Fatalf("second entry parent not linked to first")
	}
	leafID, ok := sf.GetLeafID()
	if !ok || leafID != e2.ID {
		t.Fatalf("leaf should point at last entry, got %q ok=%v", leafID, ok)
	}
}

func TestAppendEntryFillsMetadata(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf := mgr.InMemory("/tmp/x")
	id1, err := sf.AppendEntry(&Entry{Type: TypeCustom, CustomType: "state", Data: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := sf.AppendEntry(&Entry{Type: TypeCustom, CustomType: "state"})
	if err != nil {
		t.Fatal(err)
	}
	e := sf.GetEntry(id2)
	if e.ParentID == nil || *e.ParentID != id1 {
		t.Fatalf("expected parent linkage %q -> %q", id2, id1)
	}
	if e.Timestamp == "" {
		t.Fatal("expected timestamp to be filled")
	}
	ctx, err := sf.BuildContext("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Messages) != 0 {
		t.Fatalf("custom entries must not enter context, got %d messages", len(ctx.Messages))
	}
}

func TestOpenRoundTripWithContext(t *testing.T) {
	mgr, _ := newTestManager(t)
	dir := t.TempDir()

	sf, err := mgr.Create(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := sf.Path()
	if _, err := sf.AppendMessage(testUser("q")); err != nil {
		t.Fatal(err)
	}
	am := testAssistant("")
	am.Content = []types.AssistantContent{
		types.ThinkingContent{Type: types.TypeThinking, Thinking: "hmm"},
		types.TextContent{Type: types.TypeText, Text: "a"},
		types.ToolCall{Type: types.TypeToolCall, ID: "call1", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)},
	}
	am.StopReason = types.StopToolUse
	if _, err := sf.AppendMessage(am); err != nil {
		t.Fatal(err)
	}

	reopened, err := mgr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := reopened.BuildContext("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(ctx.Messages))
	}
	got, ok := ctx.Messages[1].(*types.AssistantMessage)
	if !ok {
		t.Fatalf("want assistant, got %T", ctx.Messages[1])
	}
	if len(got.Content) != 3 ||
		got.Content[0].BlockType() != types.TypeThinking ||
		got.Content[2].BlockType() != types.TypeToolCall {
		t.Fatalf("blocks not decoded: %+v", got.Content)
	}
	tc, ok := got.Content[2].(types.ToolCall)
	if !ok || tc.Name != "read" || tc.ID != "call1" {
		t.Fatalf("tool call not decoded: %#v", got.Content[2])
	}
	if ctx.Model == nil || ctx.Model.Provider != "test" || ctx.Model.ModelID != "test-model" {
		t.Fatalf("model not derived from assistant message: %+v", ctx.Model)
	}

	trBlocks, _ := types.MarshalBlocks([]types.AssistantContent{
		types.TextContent{Type: types.TypeText, Text: "data"},
	})
	tr := &types.ToolResultMessage{
		ToolCallID: "call1",
		ToolName:   "read",
		Content:    trBlocks,
		Timestamp:  3000,
	}
	if _, err := reopened.AppendMessage(tr); err != nil {
		t.Fatal(err)
	}
	ctx, err = reopened.BuildContext("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Messages) != 3 {
		t.Fatalf("want 3 messages after tool result, got %d", len(ctx.Messages))
	}
	if _, ok := ctx.Messages[2].(*types.ToolResultMessage); !ok {
		t.Fatalf("want toolResult, got %T", ctx.Messages[2])
	}
}

func TestBranchingTree(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf := mgr.InMemory("/p")
	u1, _ := sf.AppendMessage(testUser("one"))
	a1, _ := sf.AppendMessage(testAssistant("r1"))
	u2, _ := sf.AppendMessage(testUser("two"))

	if err := sf.Branch(a1); err != nil {
		t.Fatal(err)
	}
	u3, _ := sf.AppendMessage(testUser("fork"))
	e := sf.GetEntry(u3)
	if e.ParentID == nil || *e.ParentID != a1 {
		t.Fatalf("fork should be child of a1, got parent %+v", e.ParentID)
	}
	branch := sf.GetBranch("")
	if len(branch) != 3 || branch[0].ID != u1 || branch[1].ID != a1 || branch[2].ID != u3 {
		t.Fatalf("branch path wrong: %+v", ids(branch))
	}
	ctx, _ := sf.BuildContext("")
	if len(ctx.Messages) != 3 {
		t.Fatalf("context should follow fork path, got %d messages", len(ctx.Messages))
	}
	first, _ := ctx.Messages[0].(*types.UserMessage)
	if text := types.StringContentOf(mustDecode(t, first.Content)); text != "one" {
		t.Fatalf("first context message = %q", text)
	}
	last, _ := ctx.Messages[2].(*types.UserMessage)
	if text := types.StringContentOf(mustDecode(t, last.Content)); text != "fork" {
		t.Fatalf("last context message = %q", text)
	}

	if err := sf.Branch("nonexistent"); err == nil {
		t.Fatal("expected error branching to unknown entry")
	}
	sf.ResetLeaf()
	if _, ok := sf.GetLeafID(); ok {
		t.Fatal("leaf should be cleared after ResetLeaf")
	}
	rooted, _ := sf.AppendMessage(testUser("new-root"))
	if sf.GetEntry(rooted).ParentID != nil {
		t.Fatal("entry after ResetLeaf must be a root")
	}
	_ = u2
}

func TestCompactionContext(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf := mgr.InMemory("/p")
	if _, err := sf.AppendMessage(testUser("old-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.AppendMessage(testAssistant("old-2")); err != nil {
		t.Fatal(err)
	}
	u2, err := sf.AppendMessage(testUser("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sf.AppendMessage(testAssistant("kept-reply")); err != nil {
		t.Fatal(err)
	}

	// firstKeptEntryID = u2: everything before u2 is replaced by the summary.
	if _, err := sf.AppendCompaction("summary of old stuff", u2, 1234, nil, false); err != nil {
		t.Fatal(err)
	}

	ctx, err := sf.BuildContext("")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Messages) != 3 {
		t.Fatalf("want [summary kept kept-reply], got %d messages", len(ctx.Messages))
	}
	sum, ok := ctx.Messages[0].(*types.UserMessage)
	if !ok {
		t.Fatalf("compaction summary should project to user message, got %T", ctx.Messages[0])
	}
	text := types.StringContentOf(mustDecode(t, sum.Content))
	want := CompactionSummaryPrefix + "summary of old stuff" + CompactionSummarySuffix
	if text != want {
		t.Fatalf("summary framing mismatch:\n%q\n%q", text, want)
	}
	mid := types.StringContentOf(mustDecode(t, ctx.Messages[1].(*types.UserMessage).Content))
	if mid != "kept" {
		t.Fatalf("kept user lost, got %q", mid)
	}
	lastMsg, ok := ctx.Messages[2].(*types.AssistantMessage)
	if !ok || types.StringContentOf(lastMsg.Content) != "kept-reply" {
		t.Fatalf("post-compaction entries missing: %+v", ctx.Messages[2])
	}
}

func TestContextSettingsFromEntries(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf := mgr.InMemory("/p")
	if _, err := sf.AppendThinkingLevelChange(types.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.AppendModelChange("gemini", "gemini-pro"); err != nil {
		t.Fatal(err)
	}
	ctx, err := sf.BuildContext("")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ThinkingLevel != types.ThinkingHigh {
		t.Fatalf("thinking level = %q", ctx.ThinkingLevel)
	}
	if ctx.Model == nil || ctx.Model.Provider != "gemini" || ctx.Model.ModelID != "gemini-pro" {
		t.Fatalf("model ref wrong: %+v", ctx.Model)
	}
}

func TestCustomAndBranchSummariesInContext(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf := mgr.InMemory("/p")
	if _, err := sf.AppendCustom("ext-state", json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	blocks, _ := types.MarshalBlocks(nil)
	if _, err := sf.AppendCustomMessage("note", blocks, true, nil); err != nil {
		t.Fatal(err)
	}
	ctx, _ := sf.BuildContext("")
	if len(ctx.Messages) != 1 {
		t.Fatalf("custom_message should enter context once, got %d", len(ctx.Messages))
	}

	sf2 := mgr.InMemory("/p")
	if _, err := sf2.AppendMessage(testUser("abandoned")); err != nil {
		t.Fatal(err)
	}
	target := sf2.Entries()[len(sf2.Entries())-1].ID
	root := (*string)(nil)
	if _, err := sf2.AppendBranchSummary(root, "we abandoned it", nil, false); err != nil {
		t.Fatal(err)
	}
	_ = target
	ctx2, _ := sf2.BuildContext("")
	if len(ctx2.Messages) != 1 {
		t.Fatalf("want only branch summary in fresh root, got %d", len(ctx2.Messages))
	}
	text := types.StringContentOf(mustDecode(t, ctx2.Messages[0].(*types.UserMessage).Content))
	want := BranchSummaryPrefix + "we abandoned it" + BranchSummarySuffix
	if text != want {
		t.Fatalf("branch summary framing mismatch:\n%q\n%q", text, want)
	}
}

func TestLabelsAndSessionInfo(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf := mgr.InMemory("/p")
	u1, err := sf.AppendMessage(testUser("labeled"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sf.AppendLabel(u1, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if got := sf.Label(u1); got != "checkpoint" {
		t.Fatalf("label = %q", got)
	}
	if _, err := sf.AppendLabel(u1, ""); err != nil {
		t.Fatal(err)
	}
	if got := sf.Label(u1); got != "" {
		t.Fatalf("label should be cleared, got %q", got)
	}
	if name := sf.SessionName(); name != "" {
		t.Fatalf("name = %q", name)
	}
	if _, err := sf.AppendSessionInfo("my session"); err != nil {
		t.Fatal(err)
	}
	if name := sf.SessionName(); name != "my session" {
		t.Fatalf("name = %q", name)
	}
	if _, err := sf.AppendSessionInfo(""); err != nil {
		t.Fatal(err)
	}
	if name := sf.SessionName(); name != "" {
		t.Fatalf("empty session_info should clear name, got %q", name)
	}
}

func TestMalformedAndInvalidFiles(t *testing.T) {
	mgr, base := newTestManager(t)

	// Garbage prefix lines are skipped; valid header after them wins.
	okPath := filepath.Join(base, "ok.jsonl")
	_ = os.WriteFile(okPath, []byte(
		"{not json\n"+
			`{"type":"session","version":3,"id":"abc","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/w"}`+"\n"+
			"garbage line\n"+
			`{"type":"session_info","id":"e1","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","name":"n"}`+"\n"), 0o644)
	h, entries, err := LoadFile(okPath)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || h.ID != "abc" || len(entries) != 1 || entries[0].Name != "n" {
		t.Fatalf("load with garbage lines failed: h=%+v entries=%+v", h, entries)
	}
	if sf, err := mgr.Open(okPath); err != nil || sf.Header().ID != "abc" {
		t.Fatalf("open with garbage lines failed: %v", err)
	}

	// First parseable line is not a header => treated as empty by LoadFile.
	badPath := filepath.Join(base, "bad.jsonl")
	_ = os.WriteFile(badPath, []byte("{\"type\":\"message\"}\n"), 0o644)
	if h, entries, _ := LoadFile(badPath); h != nil || entries != nil {
		t.Fatal("headerless file must load as empty")
	}
	if _, err := mgr.Open(badPath); err == nil {
		t.Fatal("opening non-empty invalid file must fail")
	}

	// Empty file initializes a new session bound to that path.
	emptyPath := filepath.Join(base, "empty.jsonl")
	_ = os.WriteFile(emptyPath, []byte(""), 0o644)
	sf, err := mgr.Open(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sf.AppendMessage(testAssistant("flush now")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(emptyPath)
	if !strings.HasPrefix(string(data), `{"type":"session"`) {
		t.Fatalf("empty file should gain header, got: %s", data)
	}
}

func TestFindMostRecentAndList(t *testing.T) {
	mgr, base := newTestManager(t)
	dirA := filepath.Join(base, "sessions", EncodeCwd("/proj/a"))
	_ = os.MkdirAll(dirA, 0o755)

	sfA1, _ := mgr.Create("/proj/a", nil)
	if _, err := sfA1.AppendMessage(testUser("a-one")); err != nil {
		t.Fatal(err)
	}
	// Deferred flush means no assistant message => nothing on disk yet.
	if _, err := os.Stat(sfA1.Path()); !os.IsNotExist(err) {
		t.Fatalf("sfA1 should not be flushed yet")
	}
	if err := sfA1.Flush(); err != nil {
		t.Fatal(err)
	}
	sfA2, _ := mgr.Create("/proj/a", nil)
	if _, err := sfA2.AppendMessage(testUser("a-two")); err != nil {
		t.Fatal(err)
	}
	if _, err := sfA2.AppendMessage(testAssistant("reply")); err != nil {
		t.Fatal(err)
	}
	sfB, _ := mgr.Create("/proj/b", nil)
	if _, err := sfB.AppendMessage(testAssistant("b")); err != nil {
		t.Fatal(err)
	}

	recent, err := FindMostRecent(dirA, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	if recent != sfA2.Path() {
		t.Fatalf("most recent = %s, want %s", recent, sfA2.Path())
	}

	list, err := List(dirA)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d sessions", len(list))
	}
	if list[0].FirstMessage != "a-two" && list[1].FirstMessage != "a-two" {
		t.Fatalf("first messages: %+v", list)
	}

	cont, err := mgr.ContinueRecent("/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	if cont.Path() != sfA2.Path() {
		t.Fatalf("ContinueRecent picked %s", cont.Path())
	}
	fresh, err := mgr.ContinueRecent("/proj/none")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.GetLeafID(); ok {
		t.Fatal("fresh ContinueRecent session should have no entries")
	}
}

func TestEncodeCwd(t *testing.T) {
	cases := map[string]string{
		"/home/user/proj":  "--home-user-proj--",
		"/with:colon/path": "--with-colon-path--",
		"/":                "----",
		"//double//slash":  "--double-slash--",
	}
	for in, want := range cases {
		if got := EncodeCwd(in); got != want {
			t.Errorf("EncodeCwd(%q) = %q, want %q", in, got, want)
		}
	}
	if got := EncodeCwd("relative/path"); !strings.HasPrefix(got, "--") || !strings.HasSuffix(got, "-relative-path--") {
		t.Errorf("relative cwd not resolved+encoded deterministically: %q", got)
	}
}

func TestAssertValidSessionID(t *testing.T) {
	valid := []string{"abc", "a-b_c.d9", "1"}
	invalid := []string{"", "-lead", "trail-", "has space", "sl/ash"}
	for _, id := range valid {
		if err := AssertValidSessionID(id); err != nil {
			t.Errorf("%q should be valid: %v", id, err)
		}
	}
	for _, id := range invalid {
		if err := AssertValidSessionID(id); err == nil {
			t.Errorf("%q should be invalid", id)
		}
	}
	if _, err := (&Manager{}).Create("/p", &CreateOptions{ID: "-bad"}); err == nil {
		t.Error("Create must reject invalid explicit id")
	}
}

func TestAppendPersistenceErrorAndRollback(t *testing.T) {
	mgr, _ := newTestManager(t)
	sf, err := mgr.Create("/home/user/proj", nil)
	if err != nil {
		t.Fatal(err)
	}
	u1, err := sf.AppendMessage(testUser("hello"))
	if err != nil {
		t.Fatal(err)
	}

	// Make the session file path unwriteable by creating a read-only directory at its path.
	if err := os.Mkdir(sf.Path(), 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(sf.Path()) }()

	// Attempting to append assistant message should fail when persisting.
	id, err := sf.AppendMessage(testAssistant("hi"))
	if err == nil {
		t.Fatal("expected persistence error when writing to invalid file path")
	}
	if id != "" {
		t.Fatalf("expected empty id on failure, got %q", id)
	}

	// Verify in-memory state is rolled back.
	entries := sf.Entries()
	if len(entries) != 1 || entries[0].ID != u1 {
		t.Fatalf("entries not rolled back: %+v", entries)
	}
	leaf, ok := sf.GetLeafID()
	if !ok || leaf != u1 {
		t.Fatalf("leaf should be %s, got %s (ok=%v)", u1, leaf, ok)
	}
}

func TestAppendAfterFlushedPersistenceErrorAndRollback(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping read-only permission test: root bypasses file permission bits")
	}

	mgr, _ := newTestManager(t)
	sf, err := mgr.Create("/home/user/proj", nil)
	if err != nil {
		t.Fatal(err)
	}
	u1, err := sf.AppendMessage(testUser("hello"))
	if err != nil {
		t.Fatal(err)
	}
	a1, err := sf.AppendMessage(testAssistant("hi"))
	if err != nil {
		t.Fatal(err)
	}

	// Make file read-only to cause append error.
	if err := os.Chmod(sf.Path(), 0o444); err != nil {
		t.Fatal(err)
	}

	id, err := sf.AppendMessage(testUser("follow up"))
	if err == nil {
		t.Fatal("expected error on read-only file append")
	}
	if id != "" {
		t.Fatalf("expected empty id on failure, got %q", id)
	}

	// Verify in-memory state is rolled back.
	entries := sf.Entries()
	if len(entries) != 2 || entries[0].ID != u1 || entries[1].ID != a1 {
		t.Fatalf("entries not rolled back: %+v", entries)
	}
	leaf, ok := sf.GetLeafID()
	if !ok || leaf != a1 {
		t.Fatalf("leaf should be %s, got %s (ok=%v)", a1, leaf, ok)
	}

	// Restore write permissions and verify subsequent append succeeds.
	if err := os.Chmod(sf.Path(), 0o644); err != nil {
		t.Fatal(err)
	}
	u2, err := sf.AppendMessage(testUser("retry"))
	if err != nil {
		t.Fatalf("append after restoring permissions failed: %v", err)
	}
	if len(sf.Entries()) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(sf.Entries()))
	}
	leaf, _ = sf.GetLeafID()
	if leaf != u2 {
		t.Fatalf("leaf should be %s, got %s", u2, leaf)
	}
}

func ids(entries []*Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func mustDecode(t *testing.T, raw json.RawMessage) []types.AssistantContent {
	t.Helper()
	blocks, err := types.DecodeUserContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}
