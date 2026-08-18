package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/workflow"
)

// drainListener non-blockingly drains everything currently buffered in ch.
func drainListener(t *testing.T, ch <-chan workflow.AgentStatusUpdate) []workflow.AgentStatusUpdate {
	t.Helper()
	var out []workflow.AgentStatusUpdate
	for {
		select {
		case u := <-ch:
			out = append(out, u)
		default:
			return out
		}
	}
}

func postStatusUpdate(t *testing.T, s *Server, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/agent-status", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAgentStatus(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestAddStatusListenerMatchFiltering(t *testing.T) {
	s := &Server{}

	// Two parallel workflow node invocations on the same chat, plus one
	// unfiltered (single-agent) listener.
	chA, cancelA := s.AddStatusListener("chat-1", func(u workflow.AgentStatusUpdate) bool {
		return u.RunToken == "token-a"
	})
	defer cancelA()
	chB, cancelB := s.AddStatusListener("chat-1", func(u workflow.AgentStatusUpdate) bool {
		return u.RunToken == "token-b"
	})
	defer cancelB()
	chAll, cancelAll := s.AddStatusListener("chat-1", nil)
	defer cancelAll()

	postStatusUpdate(t, s, `{"chat_id":"chat-1","node_id":"node-a","run_token":"token-a","content":"from a"}`)
	postStatusUpdate(t, s, `{"chat_id":"chat-1","node_id":"node-b","run_token":"token-b","content":"from b"}`)

	gotA := drainListener(t, chA)
	require.Len(t, gotA, 1)
	assert.Equal(t, "node-a", gotA[0].NodeID)
	assert.Equal(t, "token-a", gotA[0].RunToken)
	assert.Equal(t, "from a", gotA[0].Content)

	gotB := drainListener(t, chB)
	require.Len(t, gotB, 1)
	assert.Equal(t, "node-b", gotB[0].NodeID)
	assert.Equal(t, "from b", gotB[0].Content)

	gotAll := drainListener(t, chAll)
	assert.Len(t, gotAll, 2)
}

func TestAddStatusListenerIsolationAfterCancel(t *testing.T) {
	s := &Server{}

	chA, cancelA := s.AddStatusListener("chat-2", func(u workflow.AgentStatusUpdate) bool {
		return u.RunToken == "token-a"
	})
	cancelA()

	// Updates posted after cancel must not be delivered to the cancelled
	// listener even if its match would accept them.
	postStatusUpdate(t, s, `{"chat_id":"chat-2","run_token":"token-a","content":"late"}`)
	assert.Empty(t, drainListener(t, chA))

	// A nil-match listener still receives everything.
	chAll, cancelAll := s.AddStatusListener("chat-2", nil)
	defer cancelAll()
	postStatusUpdate(t, s, `{"chat_id":"chat-2","run_token":"token-a","content":"ok"}`)
	got := drainListener(t, chAll)
	require.Len(t, got, 1)
	assert.Equal(t, "ok", got[0].Content)
}

// TestStatusListenerConcurrentRegisterCancelAndPost stresses the fan-out path
// against concurrent listener registration/cancellation. It regress-guards the
// send-on-closed-channel panic: cancel must never close a channel that an
// in-flight fan-out may still be sending to. Run with -race.
func TestStatusListenerConcurrentRegisterCancelAndPost(t *testing.T) {
	s := &Server{}
	chatID := "chat-race"
	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				postStatusUpdate(t, s, `{"chat_id":"`+chatID+`","run_token":"tok","content":"x"}`)
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, cancel := s.AddStatusListener(chatID, func(u workflow.AgentStatusUpdate) bool {
					return u.RunToken == "tok"
				})
				cancel()
			}
		}()
	}

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestStatusListener_BufferCapacityAndDrop(t *testing.T) {
	s := &Server{}
	chatID := "chat-buffer-test"
	ch, cancel := s.AddStatusListener(chatID, nil)
	defer cancel()

	assert.Equal(t, 256, cap(ch), "statusListener channel capacity should be 256")

	// Send 256 items to fill the buffer completely
	for i := 0; i < 256; i++ {
		postStatusUpdate(t, s, `{"chat_id":"`+chatID+`","content":"msg"}`)
	}
	assert.Equal(t, 256, len(ch))

	// 257th item should not block and should be dropped
	postStatusUpdate(t, s, `{"chat_id":"`+chatID+`","content":"overflow"}`)
	assert.Equal(t, 256, len(ch))
}
