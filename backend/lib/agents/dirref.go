package agents

import (
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// DirRefFor maps a directory onto a DirRef: the chat's own tmp and session
// dirs become TMP/SESSION refs (the runner maps them onto its own layout),
// everything else is passed as an absolute runner path. The backend never
// sends its own host paths for per-chat dirs. Empty dir is nil.
func DirRefFor(chatID, dir string) *runnerpb.DirRef {
	if dir == "" {
		return nil
	}
	if chatID != "" {
		for _, c := range []struct {
			scope runnerpb.FileScope
			base  string
		}{
			{runnerpb.FileScope_FILE_SCOPE_TMP, GetSessionTmpBaseDir(chatID)},
			{runnerpb.FileScope_FILE_SCOPE_SESSION, GetSessionScopedBaseDir("session", chatID)},
		} {
			if dir == c.base {
				return &runnerpb.DirRef{Scope: c.scope}
			}
			if rel, ok := strings.CutPrefix(dir, c.base+string(filepath.Separator)); ok {
				return &runnerpb.DirRef{Scope: c.scope, RelOrPath: rel}
			}
		}
	}
	return &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: dir}
}
