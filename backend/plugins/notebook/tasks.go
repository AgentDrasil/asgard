package notebook

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSkipDirs lists directories excluded from task scanning by default.
var DefaultSkipDirs = []string{
	".git",
	".obsidian",
	".venv",
	".agents",
	"tmp",
	"99_Templates",
	"node_modules",
}

// TaskItem is a single open markdown todo item with its vault-relative
// location.
type TaskItem struct {
	RelPath string
	Line    int
	Text    string
}

// ParseTasksFromFile scans a markdown file line by line and returns every
// open task item (lines starting with "- [ ]") together with its 1-based
// line number and slash-separated path relative to baseDir.
func ParseTasksFromFile(filePath, baseDir string) ([]TaskItem, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(baseDir, filePath)
	if err != nil {
		return nil, err
	}
	relPath := filepath.ToSlash(rel)

	var tasks []TaskItem
	for idx, line := range strings.Split(string(content), "\n") {
		stripped := strings.TrimSpace(line)
		if !strings.HasPrefix(stripped, "- [ ]") {
			continue
		}
		tasks = append(tasks, TaskItem{
			RelPath: relPath,
			Line:    idx + 1,
			Text:    strings.TrimSpace(strings.TrimPrefix(stripped, "- [ ]")),
		})
	}
	return tasks, nil
}

// GetTodoTasks scans all markdown files under noteDir (skipping any path
// containing a skipDirs component, or DefaultSkipDirs when none are given)
// and formats the open tasks as `- [rel_path:line] task_text` lines. It
// returns "No pending tasks found." when nothing is pending.
func GetTodoTasks(noteDir string, skipDirs []string) (string, error) {
	if len(skipDirs) == 0 {
		skipDirs = DefaultSkipDirs
	}
	skip := make(map[string]struct{}, len(skipDirs))
	for _, dir := range skipDirs {
		skip[dir] = struct{}{}
	}

	var lines []string
	err := filepath.WalkDir(noteDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != noteDir {
				if _, ok := skip[d.Name()]; ok {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		tasks, err := ParseTasksFromFile(path, noteDir)
		if err != nil {
			// Skip unreadable files, mirroring the original script.
			return nil
		}
		for _, task := range tasks {
			lines = append(lines, fmt.Sprintf("- [%s:%d] %s", task.RelPath, task.Line, task.Text))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "No pending tasks found.", nil
	}
	return strings.Join(lines, "\n"), nil
}
