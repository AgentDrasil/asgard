package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleSubdirs_TableDriven(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create test directory structure:
	// tmpDir/
	//   ├── dir_a/
	//   ├── dir_b/
	//   │   └── nested_dir/
	//   ├── .hidden_dir/
	//   └── file.txt
	err := os.MkdirAll(filepath.Join(tmpDir, "dir_a"), 0755)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(tmpDir, "dir_b", "nested_dir"), 0755)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(tmpDir, ".hidden_dir"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("hello"), 0644)
	require.NoError(t, err)

	srv := &Server{}

	tests := []struct {
		name            string
		dirParam        string
		expectedStatus  int
		expectedSubdirs []string
		expectError     bool
	}{
		{
			name:            "valid top-level directory",
			dirParam:        tmpDir,
			expectedStatus:  http.StatusOK,
			expectedSubdirs: []string{"dir_a", "dir_b"},
			expectError:     false,
		},
		{
			name:            "valid nested directory",
			dirParam:        filepath.Join(tmpDir, "dir_b"),
			expectedStatus:  http.StatusOK,
			expectedSubdirs: []string{"nested_dir"},
			expectError:     false,
		},
		{
			name:            "leaf directory with no subdirs",
			dirParam:        filepath.Join(tmpDir, "dir_a"),
			expectedStatus:  http.StatusOK,
			expectedSubdirs: []string{},
			expectError:     false,
		},
		{
			name:           "missing dir param",
			dirParam:       "",
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name:           "non-existent directory",
			dirParam:       filepath.Join(tmpDir, "does_not_exist"),
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
		{
			name:           "file path instead of directory",
			dirParam:       filepath.Join(tmpDir, "file.txt"),
			expectedStatus: http.StatusBadRequest,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqURL := "/api/subdirs"
			if tt.dirParam != "" {
				reqURL += "?dir=" + tt.dirParam
			}
			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			w := httptest.NewRecorder()

			srv.handleSubdirs(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			if tt.expectError {
				var errResp map[string]string
				err := json.Unmarshal(w.Body.Bytes(), &errResp)
				require.NoError(t, err)
				assert.Contains(t, errResp, "error")
			} else {
				var resp SubdirsResponse
				err := json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedSubdirs, resp.Subdirs)
			}
		})
	}
}
