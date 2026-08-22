package notebook

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultJournalBatchSize is the number of journal notes aggregated per
// absorption group, mirroring the original Python pipeline.
const DefaultJournalBatchSize = 7

// PlanGroups deterministically splits pending files into fan-out groups:
// journal notes (any path containing a Journal component) are sorted and
// batched journalBatchSize at a time (defaulting to DefaultJournalBatchSize
// when non-positive), while every other entity file forms a single-file
// group appended afterwards in input order.
func PlanGroups(pendingFiles []string, journalBatchSize int) [][]string {
	if len(pendingFiles) == 0 {
		return nil
	}
	if journalBatchSize <= 0 {
		journalBatchSize = DefaultJournalBatchSize
	}

	var journalFiles, otherFiles []string
	for _, file := range pendingFiles {
		if hasJournalComponent(file) {
			journalFiles = append(journalFiles, file)
		} else {
			otherFiles = append(otherFiles, file)
		}
	}
	sort.Strings(journalFiles)

	var groups [][]string
	for start := 0; start < len(journalFiles); start += journalBatchSize {
		end := min(start+journalBatchSize, len(journalFiles))
		groups = append(groups, journalFiles[start:end])
	}
	for _, file := range otherFiles {
		groups = append(groups, []string{file})
	}
	return groups
}

// WriteFanoutItemsFile atomically writes groups as a JSON Lines file: one
// JSON array of file paths per line, ready for fan-out consumption.
func WriteFanoutItemsFile(groups [][]string, outputPath string) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, group := range groups {
		if err := encoder.Encode(group); err != nil {
			return err
		}
	}
	return writeFileAtomic(outputPath, buf.Bytes(), 0o644)
}

// hasJournalComponent reports whether any path component equals "Journal".
func hasJournalComponent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "Journal" {
			return true
		}
	}
	return false
}
