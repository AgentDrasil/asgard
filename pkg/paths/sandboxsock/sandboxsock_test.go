package sandboxsock_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock/socktest"
)

func TestNewRejectsNonUnixAddress(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "http://127.0.0.1:8081")
	_, err := sandboxsock.New(0)
	require.ErrorContains(t, err, "only unix://")
}

func TestClientTalksOverUnixSocket(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(r.Method + " " + r.URL.Path + " " + string(b)))
	}))

	c, err := sandboxsock.New(0)
	require.NoError(t, err)

	resp, err := c.PostJSON(context.Background(), sandboxsock.PathAskUser, map[string]string{"a": "b"})
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, `POST /ask-user {"a":"b"}`, string(body))

	resp, err = c.Get(context.Background(), sandboxsock.PathFindPeer)
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "GET /find-peer ", string(body))
}

func TestDefaultURL(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "")
	c, err := sandboxsock.New(0)
	require.NoError(t, err)
	require.NotNil(t, c)
}
