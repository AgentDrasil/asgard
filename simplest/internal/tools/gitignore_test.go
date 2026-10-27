package tools

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func walkerSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b.txt", "b\n")
	writeFile(t, dir, "sub/c.txt", "c\n")
	writeFile(t, dir, "sub/d.txt", "d\n")
	return dir
}

func TestWalkerCancelInsideCallback(t *testing.T) {
	t.Parallel()
	dir := walkerSetup(t)

	var visited []string
	ctx, cancel := context.WithCancel(context.Background())
	err := walker(ctx, dir, func(absPath, rel string, d fs.DirEntry) error {
		visited = append(visited, rel)
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"a.txt"}, visited, "walk should stop right after the first file")
}

func TestWalkerPreCanceledContext(t *testing.T) {
	t.Parallel()
	dir := walkerSetup(t)

	var visited []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walker(ctx, dir, func(absPath, rel string, d fs.DirEntry) error {
		visited = append(visited, rel)
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, visited, "pre-canceled context should visit nothing")
}

func TestWalkWithGitignoreCancelPropagates(t *testing.T) {
	t.Parallel()
	dir := walkerSetup(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := walkWithGitignore(ctx, dir, func(absPath, rel string, isDir bool) error {
		t.Error("callback should not run with a pre-canceled context")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestWalkerWithoutCancelVisitsAll(t *testing.T) {
	t.Parallel()
	dir := walkerSetup(t)

	var visited []string
	err := walker(context.Background(), dir, func(absPath, rel string, d fs.DirEntry) error {
		visited = append(visited, rel)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt", "b.txt", "sub/c.txt", "sub/d.txt"}, visited)
}

func TestWalkerErrStopWalkStillAborts(t *testing.T) {
	t.Parallel()
	dir := walkerSetup(t)

	var visited []string
	err := walkWithGitignore(context.Background(), dir, func(absPath, rel string, isDir bool) error {
		visited = append(visited, rel)
		if len(visited) == 2 {
			return errStopWalk
		}
		return nil
	})
	require.True(t, errors.Is(err, errStopWalk))
	assert.Equal(t, []string{"a.txt", "b.txt"}, visited)
}
