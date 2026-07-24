package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleTTYDMissingSession(t *testing.T) {
	srv := &Server{}

	req := httptest.NewRequest("GET", "/api/ttyd/", nil)
	rec := httptest.NewRecorder()

	srv.handleTTYD(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 when ttydManager is nil, got %d", rec.Code)
	}
}
