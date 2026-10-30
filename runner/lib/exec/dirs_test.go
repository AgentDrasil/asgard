package exec

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestResolveDirRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name    string
		ref     *runnerpb.DirRef
		want    string
		wantErr string
	}{
		{"nil", nil, "", ""},
		{"empty", &runnerpb.DirRef{}, "", ""},
		{"tmp root", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}, runnerpaths.SessionTmpDir("c1"), ""},
		{"tmp rel", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP, RelOrPath: "a/b"}, filepath.Join(runnerpaths.SessionTmpDir("c1"), "a/b"), ""},
		{"session rel", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_SESSION, RelOrPath: "x"}, filepath.Join(runnerpaths.SessionDir("c1"), "x"), ""},
		{"absolute", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "/work/../proj"}, "/proj", ""},
		{"relative absolute", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "proj"}, "", "not absolute"},
		{"tmp escape", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP, RelOrPath: "../other"}, "", "escapes"},
		{"unspecified with path", &runnerpb.DirRef{RelOrPath: "/x"}, "", "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveDirRef("c1", tt.ref)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("bad chat id", func(t *testing.T) {
		_, err := ResolveDirRef("../x", &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP})
		assert.ErrorContains(t, err, "invalid chat id")
	})
}

func TestDirRefFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.Nil(t, DirRefFor("c1", ""))
	assert.Equal(t, &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_TMP}, DirRefFor("c1", runnerpaths.SessionTmpDir("c1")))
	assert.Equal(t, &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_SESSION, RelOrPath: "d"},
		DirRefFor("c1", filepath.Join(runnerpaths.SessionDir("c1"), "d")))
	assert.Equal(t, &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "/work"}, DirRefFor("c1", "/work"))
	// Another chat's tmp dir is just an absolute path.
	other := runnerpaths.SessionTmpDir("c2")
	assert.Equal(t, &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: other}, DirRefFor("c1", other))
}
