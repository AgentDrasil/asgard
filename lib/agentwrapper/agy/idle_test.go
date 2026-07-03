package agy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsIdle(t *testing.T) {
	oldDir := statuslineDir
	statuslineDir = t.TempDir()
	t.Cleanup(func() {
		statuslineDir = oldDir
	})

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "fully idle",
			payload: `{"agent_state": "idle", "task_count": 0, "subagents": []}`,
			want:    true,
		},
		{
			name:    "non-idle state",
			payload: `{"agent_state": "thinking", "task_count": 0, "subagents": []}`,
			want:    false,
		},
		{
			name:    "task count greater than zero",
			payload: `{"agent_state": "idle", "task_count": 1, "subagents": []}`,
			want:    false,
		},
		{
			name:    "subagents not idle",
			payload: `{"agent_state": "idle", "task_count": 0, "subagents": [{"name": "sub1", "status": "running"}]}`,
			want:    false,
		},
		{
			name:    "subagents all idle",
			payload: `{"agent_state": "idle", "task_count": 0, "subagents": [{"name": "sub1", "status": "idle"}]}`,
			want:    true,
		},
		{
			name:    "case insensitive state and subagent status",
			payload: `{"agent_state": "IDLE", "task_count": 0, "subagents": [{"name": "sub1", "status": "IDLE"}]}`,
			want:    true,
		},
		{
			name:    "invalid json",
			payload: `{invalid}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := uuid.NewString()
			filePath := filepath.Join(statuslineDir, sessionID+".json")

			err := os.WriteFile(filePath, []byte(tt.payload), 0644)
			require.NoError(t, err)

			got := isIdle(sessionID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsIdle_NoFile(t *testing.T) {
	oldDir := statuslineDir
	statuslineDir = t.TempDir()
	t.Cleanup(func() {
		statuslineDir = oldDir
	})
	assert.False(t, isIdle(uuid.NewString()))
	assert.False(t, isIdle(""))
}

func TestPollUntilIdle(t *testing.T) {
	oldDir := statuslineDir
	statuslineDir = t.TempDir()
	t.Cleanup(func() {
		statuslineDir = oldDir
	})

	t.Run("success", func(t *testing.T) {
		sessionID := uuid.NewString()
		filePath := filepath.Join(statuslineDir, sessionID+".json")

		// Write non-idle payload first
		err := os.WriteFile(filePath, []byte(`{"agent_state": "thinking"}`), 0644)
		require.NoError(t, err)

		// After 100ms, write idle payload
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = os.WriteFile(filePath, []byte(`{"agent_state": "idle"}`), 0644)
		}()

		done := make(chan error, 1)
		timedOut, err := pollUntilIdle(context.Background(), sessionID, done, 1*time.Second)
		require.NoError(t, err)
		assert.False(t, timedOut)
	})

	t.Run("timeout", func(t *testing.T) {
		sessionID := uuid.NewString()
		filePath := filepath.Join(statuslineDir, sessionID+".json")

		err := os.WriteFile(filePath, []byte(`{"agent_state": "thinking"}`), 0644)
		require.NoError(t, err)

		done := make(chan error, 1)
		timedOut, err := pollUntilIdle(context.Background(), sessionID, done, 100*time.Millisecond)
		require.NoError(t, err)
		assert.True(t, timedOut)
	})

	t.Run("context cancelled", func(t *testing.T) {
		sessionID := uuid.NewString()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		done := make(chan error, 1)
		_, err := pollUntilIdle(ctx, sessionID, done, 1*time.Second)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("unexpected exit", func(t *testing.T) {
		sessionID := uuid.NewString()

		done := make(chan error, 1)
		done <- fmt.Errorf("process crashed")

		_, err := pollUntilIdle(context.Background(), sessionID, done, 1*time.Second)
		assert.ErrorContains(t, err, "agy exited unexpectedly: process crashed")
	})
}
