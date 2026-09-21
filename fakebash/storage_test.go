package fakebash

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorage_StreamAppendAndRead(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	cmdText := "echo 'hello world'"
	sf, err := storage.CreateFile(cmdText)
	require.NoError(t, err)
	require.NotEmpty(t, sf.ID())
	require.True(t, strings.HasPrefix(sf.ID(), "c-"))

	err = sf.Append([]byte("chunk 1: starting...\n"))
	require.NoError(t, err)

	err = sf.Append([]byte("chunk 2: processing...\n"))
	require.NoError(t, err)

	err = sf.Finish(0)
	require.NoError(t, err)

	// Appending to finished file should error
	err = sf.Append([]byte("chunk 3: should fail\n"))
	assert.Error(t, err)

	content, err := storage.Read(sf.ID())
	require.NoError(t, err)

	strContent := string(content)
	assert.Contains(t, strContent, fmt.Sprintf("# CMD: %s", cmdText))
	assert.Contains(t, strContent, "chunk 1: starting...\n")
	assert.Contains(t, strContent, "chunk 2: processing...\n")
	assert.Contains(t, strContent, "# EXIT: 0")

	// Read via "latest"
	latestContent, err := storage.Read("latest")
	require.NoError(t, err)
	assert.Equal(t, content, latestContent)

	// Read Range
	subRange, err := storage.ReadRange(sf.ID(), 0, 10)
	require.NoError(t, err)
	assert.Equal(t, content[:10], subRange)
}

func TestStorage_ConcurrentSave(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	concurrency := 50
	var wg sync.WaitGroup
	wg.Add(concurrency)

	cmdIDs := make([]string, concurrency)
	for i := 0; i < concurrency; i++ {
		idx := i
		go func() {
			defer wg.Done()
			sf, cErr := storage.CreateFile(fmt.Sprintf("cmd-%d", idx))
			if cErr != nil {
				t.Errorf("CreateFile failed for %d: %v", idx, cErr)
				return
			}
			cmdIDs[idx] = sf.ID()

			for j := 0; j < 5; j++ {
				_ = sf.Append([]byte(fmt.Sprintf("worker %d line %d\n", idx, j)))
			}
			_ = sf.Finish(0)
		}()
	}

	wg.Wait()

	// Verify 50 unique log files exist (plus latest.log symlink)
	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)

	logCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") && entry.Name() != LatestSymlink {
			logCount++
		}
	}
	assert.Equal(t, concurrency, logCount)

	// Verify all cmdIDs are distinct
	seenIDs := make(map[string]bool)
	for _, id := range cmdIDs {
		require.NotEmpty(t, id)
		assert.False(t, seenIDs[id], "duplicate cmd ID: %s", id)
		seenIDs[id] = true
	}

	// Verify latest.log points to a valid file
	latestPath, err := storage.GetPath("latest")
	require.NoError(t, err)
	assert.FileExists(t, latestPath)
}

func TestStorage_LatestAtomicUpdate(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	sf1, err := storage.CreateFile("first-cmd")
	require.NoError(t, err)
	_ = sf1.Append([]byte("output 1\n"))
	_ = sf1.Finish(0)

	latestPath1, err := storage.GetPath("latest")
	require.NoError(t, err)
	assert.Equal(t, sf1.Path(), latestPath1)

	sf2, err := storage.CreateFile("second-cmd")
	require.NoError(t, err)
	_ = sf2.Append([]byte("output 2\n"))
	_ = sf2.Finish(1)

	latestPath2, err := storage.GetPath("latest")
	require.NoError(t, err)
	assert.Equal(t, sf2.Path(), latestPath2)
	assert.NotEqual(t, latestPath1, latestPath2)
}

func TestStorage_GetHeadAndTail_FileBased(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	sf, err := storage.CreateFile("large-cmd")
	require.NoError(t, err)

	// Write ~50KB of content
	totalLines := 1000
	for i := 0; i < totalLines; i++ {
		_ = sf.Append([]byte(fmt.Sprintf("Line %04d: some verbose log data for testing head and tail extraction.\n", i)))
	}
	_ = sf.Finish(0)

	headBytes := 1024
	tailBytes := 1024
	head, tail, total, lines, err := storage.GetHeadAndTail(sf.ID(), headBytes, tailBytes)
	require.NoError(t, err)

	assert.Equal(t, headBytes, len(head))
	assert.Equal(t, tailBytes, len(tail))
	assert.Greater(t, total, int64(45000))
	// totalLines + 1 (header line) + 2 (finish footer lines "\n# EXIT: 0\n")
	assert.GreaterOrEqual(t, lines, totalLines)

	// Verify head starts with the header
	assert.True(t, strings.HasPrefix(string(head), "# CMD: large-cmd"))
	// Verify tail ends with the finish line
	assert.True(t, strings.HasSuffix(string(tail), "# EXIT: 0\n"))
}

func TestStorage_SecurityPathTraversal(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	maliciousIDs := []string{
		"../../etc/passwd",
		"../foo",
		"/etc/shadow",
		"c-12345/../../bar",
		"c-123;rm -rf /",
	}

	for _, id := range maliciousIDs {
		_, err := storage.GetPath(id)
		assert.Error(t, err, "expected error for malicious ID: %s", id)

		_, err = storage.Read(id)
		assert.Error(t, err, "expected error for reading malicious ID: %s", id)
	}
}
