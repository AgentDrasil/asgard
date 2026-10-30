package exec

import (
	"fmt"
	"path/filepath"
	"strings"

	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// ResolveDirRef maps a DirRef onto the runner's own layout: TMP and SESSION
// are the chat's tmp and session dirs (rel_or_path is relative to them),
// ABSOLUTE is a runner-absolute path. A nil or empty ref resolves to "".
func ResolveDirRef(chatID string, ref *runnerpb.DirRef) (string, error) {
	if ref == nil || (ref.GetScope() == runnerpb.FileScope_FILE_SCOPE_UNSPECIFIED && ref.GetRelOrPath() == "") {
		return "", nil
	}
	rel := ref.GetRelOrPath()
	switch ref.GetScope() {
	case runnerpb.FileScope_FILE_SCOPE_TMP, runnerpb.FileScope_FILE_SCOPE_SESSION:
		if chatID == "" || !filepath.IsLocal(chatID) {
			return "", fmt.Errorf("invalid chat id %q for %s dir", chatID, ref.GetScope())
		}
		base := runnerpaths.SessionTmpDir(chatID)
		if ref.GetScope() == runnerpb.FileScope_FILE_SCOPE_SESSION {
			base = runnerpaths.SessionDir(chatID)
		}
		if rel == "" {
			return base, nil
		}
		if !filepath.IsLocal(rel) {
			return "", fmt.Errorf("dir %q escapes the %s dir", rel, ref.GetScope())
		}
		return filepath.Join(base, rel), nil
	case runnerpb.FileScope_FILE_SCOPE_ABSOLUTE:
		if !filepath.IsAbs(rel) {
			return "", fmt.Errorf("dir %q is not absolute", rel)
		}
		return filepath.Clean(rel), nil
	default:
		return "", fmt.Errorf("unsupported dir scope %s", ref.GetScope())
	}
}

// DirRefFor is the inverse of ResolveDirRef for an absolute runner path: a
// path inside the chat's tmp or session dir becomes a TMP/SESSION ref.
func DirRefFor(chatID, abs string) *runnerpb.DirRef {
	if abs == "" {
		return nil
	}
	if chatID != "" && filepath.IsLocal(chatID) {
		for _, c := range []struct {
			scope runnerpb.FileScope
			base  string
		}{
			{runnerpb.FileScope_FILE_SCOPE_TMP, runnerpaths.SessionTmpDir(chatID)},
			{runnerpb.FileScope_FILE_SCOPE_SESSION, runnerpaths.SessionDir(chatID)},
		} {
			if abs == c.base {
				return &runnerpb.DirRef{Scope: c.scope}
			}
			if rel, ok := strings.CutPrefix(abs, c.base+string(filepath.Separator)); ok {
				return &runnerpb.DirRef{Scope: c.scope, RelOrPath: rel}
			}
		}
	}
	return &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: abs}
}
