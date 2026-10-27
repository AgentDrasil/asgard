package tools

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaturatingFloatToInt(t *testing.T) {
	t.Parallel()

	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1

	tests := []struct {
		name string
		in   float64
		want int
	}{
		{"normal", 3, 3},
		{"fractional truncates", 2.9, 2},
		{"negative preserved", -5.7, -5},
		{"zero", 0, 0},
		{"huge positive", 1e300, maxInt},
		{"huge negative", -1e300, minInt},
		{"positive inf", math.Inf(1), maxInt},
		{"negative inf", math.Inf(-1), minInt},
		{"nan", math.NaN(), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, saturatingFloatToInt(tt.in))
		})
	}
}

func TestReadToolRangeGuards(t *testing.T) {
	t.Parallel()

	content := "one\ntwo\nthree\nfour\n"

	t.Run("negative limit returns zero lines without panic", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res, err := execTool(t, NewReadTool(dir), `{"path":"f.txt","limit":-5}`)
		require.NoError(t, err)
		out := textOf(t, res)
		assert.Empty(t, out)
		assert.NotContains(t, out, "one")
		assert.NotContains(t, out, "four")
		assert.NotContains(t, out, "more lines in file")
	})

	t.Run("zero limit returns zero lines without hint", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res, err := execTool(t, NewReadTool(dir), `{"path":"f.txt","limit":0}`)
		require.NoError(t, err)
		out := textOf(t, res)
		assert.Empty(t, out)
		assert.NotContains(t, out, "one")
		assert.NotContains(t, out, "four")
		assert.NotContains(t, out, "more lines in file")
	})

	t.Run("huge limit returns whole file without panic", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res := mustExec(t, NewReadTool(dir), `{"path":"f.txt","limit":1e300}`)
		out := textOf(t, res)
		for _, line := range []string{"one", "two", "three", "four"} {
			assert.Contains(t, out, line)
		}
		assert.NotContains(t, out, "more lines in file")
	})

	t.Run("huge offset returns beyond EOF error without panic", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		_, err := execTool(t, NewReadTool(dir), `{"path":"f.txt","offset":1e300}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "beyond end of file")
	})

	t.Run("negative offset starts at line 1", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res := mustExec(t, NewReadTool(dir), `{"path":"f.txt","offset":-10}`)
		out := textOf(t, res)
		assert.True(t, strings.HasPrefix(out, "one\n"), "output = %q", out)
		assert.Contains(t, out, "four")
	})

	t.Run("fractional offset below 1 starts at line 1", func(t *testing.T) {
		t.Parallel()
		for _, off := range []string{"0.5", "0.9"} {
			dir := t.TempDir()
			writeFile(t, dir, "f.txt", content)
			res := mustExec(t, NewReadTool(dir), `{"path":"f.txt","offset":`+off+`}`)
			out := textOf(t, res)
			assert.True(t, strings.HasPrefix(out, "one\n"), "offset %s: output = %q", off, out)
		}
	})

	t.Run("fractional offset below 1 with limit", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res := mustExec(t, NewReadTool(dir), `{"path":"f.txt","offset":0.5,"limit":2}`)
		out := textOf(t, res)
		assert.True(t, strings.HasPrefix(out, "one\ntwo\n"), "output = %q", out)
		assert.Contains(t, out, "more lines in file")
	})

	t.Run("fractional offset and limit truncate", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res := mustExec(t, NewReadTool(dir), `{"path":"f.txt","offset":2.9,"limit":1.9}`)
		out := textOf(t, res)
		assert.True(t, strings.HasPrefix(out, "two\n"), "output = %q", out)
		assert.NotContains(t, out, "three")
		assert.Contains(t, out, "more lines in file")
	})

	t.Run("valid offset and limit window unchanged", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "f.txt", content)
		res := mustExec(t, NewReadTool(dir), `{"path":"f.txt","offset":2,"limit":2}`)
		out := textOf(t, res)
		want := "two\nthree\n\n[2 more lines in file. Use offset=4 to continue.]"
		assert.Equal(t, want, out)
	})
}
