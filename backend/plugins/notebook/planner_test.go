package notebook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func journalFiles(count int) []string {
	files := make([]string, 0, count)
	for i := 0; i < count; i++ {
		files = append(files, filepath.Join("01_Raw", "Journal", fmt.Sprintf("2025-08-%02d.md", i+1)))
	}
	return files
}

func TestPlanGroups(t *testing.T) {
	t.Parallel()

	entityA := filepath.Join("01_Raw", "Entities", "person-a.md")
	entityB := filepath.Join("01_Raw", "Entities", "person-b.md")

	tests := []struct {
		name             string
		pending          []string
		journalBatchSize int
		want             [][]string
	}{
		{
			name:             "no pending files",
			pending:          nil,
			journalBatchSize: 7,
			want:             nil,
		},
		{
			name:             "three journals form a single group",
			pending:          journalFiles(3),
			journalBatchSize: 7,
			want:             [][]string{journalFiles(3)},
		},
		{
			name:             "seven journals fill exactly one batch",
			pending:          journalFiles(7),
			journalBatchSize: 7,
			want:             [][]string{journalFiles(7)},
		},
		{
			name:             "fifteen journals split into 7/7/1 batches",
			pending:          journalFiles(15),
			journalBatchSize: 7,
			want: [][]string{
				journalFiles(7)[0:7],
				journalFiles(15)[7:14],
				journalFiles(15)[14:15],
			},
		},
		{
			name:             "custom batch size of three",
			pending:          journalFiles(4),
			journalBatchSize: 3,
			want: [][]string{
				journalFiles(4)[0:3],
				journalFiles(4)[3:4],
			},
		},
		{
			name:             "non-positive batch size falls back to default",
			pending:          journalFiles(8),
			journalBatchSize: 0,
			want: [][]string{
				journalFiles(8)[0:7],
				journalFiles(8)[7:8],
			},
		},
		{
			name:             "mixed journals and entities",
			pending:          append(journalFiles(8), entityA, entityB),
			journalBatchSize: 7,
			want: [][]string{
				journalFiles(8)[0:7],
				journalFiles(8)[7:8],
				{entityA},
				{entityB},
			},
		},
		{
			name:             "entities keep input order as single-file groups",
			pending:          []string{entityB, entityA},
			journalBatchSize: 7,
			want:             [][]string{{entityB}, {entityA}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := PlanGroups(tt.pending, tt.journalBatchSize)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPlanGroupsSortsJournalsDeterministically(t *testing.T) {
	t.Parallel()

	unsorted := []string{
		filepath.Join("01_Raw", "Journal", "2025-08-03.md"),
		filepath.Join("01_Raw", "Journal", "2025-08-01.md"),
		filepath.Join("01_Raw", "Journal", "2025-08-02.md"),
	}

	got := PlanGroups(unsorted, 7)
	require.Len(t, got, 1)
	assert.Equal(t, []string{
		filepath.Join("01_Raw", "Journal", "2025-08-01.md"),
		filepath.Join("01_Raw", "Journal", "2025-08-02.md"),
		filepath.Join("01_Raw", "Journal", "2025-08-03.md"),
	}, got[0])
}

func TestPlanGroupsJournalPathComponent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{"01_Raw/Journal/2025-08-01.md", true},
		{"/vault/01_Raw/Journal/note.md", true},
		{"Journal/2025-08-01.md", true},
		{"01_Raw/Notes/journal-2025.md", false},
		{"01_Raw/Journals/2025.md", false},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, hasJournalComponent(tt.path), tt.path)
	}
}

func TestWriteFanoutItemsFile(t *testing.T) {
	t.Parallel()

	groups := [][]string{
		{"01_Raw/Journal/a.md", "01_Raw/Journal/b.md"},
		{"01_Raw/Entities/person.md"},
	}
	outputPath := filepath.Join(t.TempDir(), "tmp", "absorb_items.jsonl")

	require.NoError(t, WriteFanoutItemsFile(groups, outputPath))

	raw, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Equal(t,
		`["01_Raw/Journal/a.md","01_Raw/Journal/b.md"]`+"\n"+
			`["01_Raw/Entities/person.md"]`+"\n",
		string(raw))

	// The written file parses back into the original groups.
	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	var restored [][]string
	for _, line := range splitLines(string(content)) {
		var group []string
		require.NoError(t, json.Unmarshal([]byte(line), &group))
		restored = append(restored, group)
	}
	assert.Equal(t, groups, restored)

	// Atomic writing leaves no temporary files behind.
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(outputPath), ".*.tmp-*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)
}

func TestWriteFanoutItemsFileEmpty(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "nested", "dir", "items.jsonl")
	require.NoError(t, WriteFanoutItemsFile(nil, outputPath))

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestWriteFanoutItemsFileOverwritesAtomically(t *testing.T) {
	t.Parallel()

	outputPath := filepath.Join(t.TempDir(), "items.jsonl")
	require.NoError(t, os.WriteFile(outputPath, []byte("stale content"), 0o644))

	require.NoError(t, WriteFanoutItemsFile([][]string{{"a.md"}}, outputPath))

	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Equal(t, `["a.md"]`+"\n", string(data))
}

// splitLines splits s into non-empty lines.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
