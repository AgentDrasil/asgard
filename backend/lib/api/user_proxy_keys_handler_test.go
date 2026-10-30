package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func newTestServerForProxyKeys(t *testing.T) (*Server, *http.ServeMux) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))

	conf := &config.Config{
		Providers: []string{"agy"},
	}

	srv, err := New(conf, testDB)
	require.NoError(t, err)

	return srv, srv.buildMuxLocked()
}

func TestUserProxyKeys_API_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("POST /api/user/proxy-keys validation", func(t *testing.T) {
		t.Parallel()
		_, mux := newTestServerForProxyKeys(t)

		tests := []struct {
			name       string
			body       map[string]string
			wantStatus int
		}{
			{"missing key_name", map[string]string{"value": "val"}, http.StatusBadRequest},
			{"missing value", map[string]string{"key_name": "key"}, http.StatusBadRequest},
			{"empty strings", map[string]string{"key_name": "", "value": ""}, http.StatusBadRequest},
			{"invalid format hyphen", map[string]string{"key_name": "gemini-key", "value": "val"}, http.StatusBadRequest},
			{"invalid format starts with digit", map[string]string{"key_name": "1KEY", "value": "val"}, http.StatusBadRequest},
			{"reserved key PATH", map[string]string{"key_name": "PATH", "value": "val"}, http.StatusBadRequest},
			{"reserved key HTTP_PROXY", map[string]string{"key_name": "HTTP_PROXY", "value": "val"}, http.StatusBadRequest},
			{"valid key", map[string]string{"key_name": "GEMINI_API_KEY", "value": "test-key-123"}, http.StatusOK},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				b, _ := json.Marshal(tt.body)
				req := httptest.NewRequest(http.MethodPost, "/api/user/proxy-keys", bytes.NewReader(b))
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)

				assert.Equal(t, tt.wantStatus, rec.Code)
			})
		}
	})

	t.Run("Full CRUD lifecycle with default user", func(t *testing.T) {
		t.Parallel()
		srv, mux := newTestServerForProxyKeys(t)

		// 1. Initially empty
		reqGet := httptest.NewRequest(http.MethodGet, "/api/user/proxy-keys", nil)
		recGet := httptest.NewRecorder()
		mux.ServeHTTP(recGet, reqGet)
		require.Equal(t, http.StatusOK, recGet.Code)

		var metas []dbmodels.UserProxyKeyMeta
		require.NoError(t, json.Unmarshal(recGet.Body.Bytes(), &metas))
		assert.Empty(t, metas)

		// 2. Create key
		createBody, _ := json.Marshal(map[string]string{
			"key_name": "gemini_key",
			"value":    "  secret-gemini-val  \r\n",
		})
		reqPost := httptest.NewRequest(http.MethodPost, "/api/user/proxy-keys", bytes.NewReader(createBody))
		recPost := httptest.NewRecorder()
		mux.ServeHTTP(recPost, reqPost)
		require.Equal(t, http.StatusOK, recPost.Code)

		// 3. List should return metadata
		recGet2 := httptest.NewRecorder()
		mux.ServeHTTP(recGet2, reqGet)
		require.Equal(t, http.StatusOK, recGet2.Code)

		metas = nil
		require.NoError(t, json.Unmarshal(recGet2.Body.Bytes(), &metas))
		require.Len(t, metas, 1)
		assert.Equal(t, "gemini_key", metas[0].KeyName)
		assert.False(t, metas[0].UpdatedAt.IsZero())

		// Plaintext must NOT leak in metadata response
		assert.NotContains(t, recGet2.Body.String(), "secret-gemini-val")

		// 4. Verify in DB that it is encrypted with leading whitespace preserved and trailing \r\n trimmed
		decrypted, err := srv.UserProxyKeyRepo().GetAllDecryptedKeys(context.Background(), "default")
		require.NoError(t, err)
		assert.Equal(t, "  secret-gemini-val  ", decrypted["gemini_key"])

		// 5. Delete key
		reqDel := httptest.NewRequest(http.MethodDelete, "/api/user/proxy-keys/gemini_key", nil)
		recDel := httptest.NewRecorder()
		mux.ServeHTTP(recDel, reqDel)
		require.Equal(t, http.StatusOK, recDel.Code)

		// 6. List should be empty again
		recGet3 := httptest.NewRecorder()
		mux.ServeHTTP(recGet3, reqGet)
		require.Equal(t, http.StatusOK, recGet3.Code)
		metas = nil
		require.NoError(t, json.Unmarshal(recGet3.Body.Bytes(), &metas))
		assert.Empty(t, metas)
	})

	t.Run("Authenticated user isolation", func(t *testing.T) {
		t.Parallel()
		_, mux := newTestServerForProxyKeys(t)

		user1Ctx := auth.WithUser(context.Background(), &auth.User{Subject: "user-alice"})
		user2Ctx := auth.WithUser(context.Background(), &auth.User{Subject: "user-bob"})

		// Save key for Alice
		bAlice, _ := json.Marshal(map[string]string{
			"key_name": "ALICE_KEY",
			"value":    "alice-secret",
		})
		reqAlice := httptest.NewRequest(http.MethodPost, "/api/user/proxy-keys", bytes.NewReader(bAlice)).WithContext(user1Ctx)
		recAlice := httptest.NewRecorder()
		mux.ServeHTTP(recAlice, reqAlice)
		require.Equal(t, http.StatusOK, recAlice.Code)

		// Bob should NOT see Alice's key
		reqBob := httptest.NewRequest(http.MethodGet, "/api/user/proxy-keys", nil).WithContext(user2Ctx)
		recBob := httptest.NewRecorder()
		mux.ServeHTTP(recBob, reqBob)
		require.Equal(t, http.StatusOK, recBob.Code)

		var bobMetas []dbmodels.UserProxyKeyMeta
		require.NoError(t, json.Unmarshal(recBob.Body.Bytes(), &bobMetas))
		assert.Empty(t, bobMetas)

		// Alice should see her key
		reqAliceGet := httptest.NewRequest(http.MethodGet, "/api/user/proxy-keys", nil).WithContext(user1Ctx)
		recAliceGet := httptest.NewRecorder()
		mux.ServeHTTP(recAliceGet, reqAliceGet)
		require.Equal(t, http.StatusOK, recAliceGet.Code)

		var aliceMetas []dbmodels.UserProxyKeyMeta
		require.NoError(t, json.Unmarshal(recAliceGet.Body.Bytes(), &aliceMetas))
		require.Len(t, aliceMetas, 1)
		assert.Equal(t, "ALICE_KEY", aliceMetas[0].KeyName)
	})
}
