// Package prompt builds the system prompt (base text plus assembly API)
// and loads project context files (AGENTS.md / CLAUDE.md
// hierarchy walking from cwd to filesystem root).
package prompt

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// ContextFile is one loaded instruction file.
type ContextFile struct {
	Path    string
	Content string
}

// contextFileCandidates is the per-directory precedence order: the first
// existing file wins.
var contextFileCandidates = []string{
	"AGENTS.override.md",
	"AGENTS.md",
	"AGENTS.MD",
	"CLAUDE.md",
	"CLAUDE.MD",
}

func stripBOM(b []byte) string {
	return string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
}

func loadContextFileFromDir(dir string) *ContextFile {
	for _, name := range contextFileCandidates {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return &ContextFile{Path: p, Content: stripBOM(data)}
	}
	return nil
}

// LoadProjectContextFiles collects instruction files: the global one from
// agentDir first, then one per directory walking from cwd up to the root,
// ordered root-most first (nearest directory last). Missing files are skipped;
// read errors are ignored on a best-effort basis. The git-worktree
// shadowing rule is not implemented (linked-worktree setups only).
func LoadProjectContextFiles(cwd, agentDir string) []ContextFile {
	resolvedCwd, err := filepath.Abs(cwd)
	if err != nil {
		resolvedCwd = cwd
	}
	var files []ContextFile
	seen := map[string]bool{}

	add := func(cf *ContextFile) bool {
		if cf == nil || seen[cf.Path] {
			return false
		}
		seen[cf.Path] = true
		files = append(files, *cf)
		return true
	}

	add(loadContextFileFromDir(agentDir))

	var ancestors []ContextFile
	dir := resolvedCwd
	for {
		if cf := loadContextFileFromDir(dir); cf != nil && !seen[cf.Path] {
			seen[cf.Path] = true
			ancestors = append(ancestors, *cf)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Reverse: root-most first, nearest to cwd last.
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}
	files = append(files, ancestors...)
	return files
}

// Options configures BuildSystemPrompt.
type Options struct {
	// CustomPrompt replaces the entire default body (context files, cwd line,
	// and append text are still applied around it).
	CustomPrompt string
	// SelectedTools lists the active tool names. Defaults to read/bash/edit/write.
	SelectedTools []string
	// ToolSnippets maps tool name to its one-line "Available tools" entry;
	// tools without a snippet are omitted from the list.
	ToolSnippets map[string]string
	// PromptGuidelines are extra guideline bullets (deduplicated).
	PromptGuidelines []string
	// AppendSystemPrompt is appended verbatim after the main body.
	AppendSystemPrompt string
	// CWD is shown in the trailing "Current working directory" line.
	CWD string
	// ContextFiles overrides loading; when nil, callers usually pass
	// LoadProjectContextFiles output explicitly.
	ContextFiles []ContextFile
	// Documentation paths shown in the prompt. These cannot be resolved
	// from an installed package, so they default to repo-relative names.
	ReadmePath   string
	DocsPath     string
	ExamplesPath string
}

func hasTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

const baseIdentity = `You are an expert coding assistant operating inside an agent harness. You help users by reading files, executing commands, editing code, and writing new files.`

// BuildSystemPrompt assembles the system prompt: identity, available tools,
// guidelines, append text, project context files, and cwd.
func BuildSystemPrompt(opts Options) string {
	promptCwd := strings.ReplaceAll(opts.CWD, "\\", "/")
	appendSection := ""
	if opts.AppendSystemPrompt != "" {
		appendSection = "\n\n" + opts.AppendSystemPrompt
	}
	contextFiles := opts.ContextFiles
	tools := opts.SelectedTools
	if tools == nil {
		tools = []string{"read", "bash", "edit", "write"}
	}

	var b strings.Builder

	if opts.CustomPrompt != "" {
		b.WriteString(opts.CustomPrompt)
		b.WriteString(appendSection)
		b.WriteString(projectContextSection(contextFiles))
		b.WriteString("\nCurrent working directory: " + promptCwd + "\n")
		return b.String()
	}

	visibleTools := make([]string, 0, len(tools))
	for _, name := range tools {
		if snippet := opts.ToolSnippets[name]; snippet != "" {
			visibleTools = append(visibleTools, "- "+name+": "+snippet)
		}
	}
	toolsList := "(none)"
	if len(visibleTools) > 0 {
		toolsList = strings.Join(visibleTools, "\n")
	}

	guidelinesSet := map[string]bool{}
	var guidelinesList []string
	addGuideline := func(g string) {
		if !guidelinesSet[g] {
			guidelinesSet[g] = true
			guidelinesList = append(guidelinesList, g)
		}
	}
	hasBash := hasTool(tools, "bash")
	hasGrep := hasTool(tools, "grep")
	hasFind := hasTool(tools, "find")
	hasLs := hasTool(tools, "ls")
	if hasBash && !hasGrep && !hasFind && !hasLs {
		addGuideline("Use bash for file operations like ls, rg, find")
	}
	for _, g := range opts.PromptGuidelines {
		normalized := strings.TrimSpace(g)
		if normalized != "" {
			addGuideline(normalized)
		}
	}
	addGuideline("Be concise in your responses")
	addGuideline("Show file paths clearly when working with files")

	guidelines := make([]string, len(guidelinesList))
	for i, g := range guidelinesList {
		guidelines[i] = "- " + g
	}

	b.WriteString(baseIdentity + `

Available tools:
` + toolsList + `

In addition to the tools above, you may have access to other custom tools depending on the project.

Guidelines:
` + strings.Join(guidelines, "\n") + ``)

	b.WriteString(appendSection)
	b.WriteString(projectContextSection(contextFiles))
	b.WriteString("\nCurrent working directory: " + promptCwd)

	return b.String()
}

func projectContextSection(files []ContextFile) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<project_context>\n\nProject-specific instructions and guidelines:\n\n")
	for _, f := range files {
		b.WriteString("<project_instructions path=\"" + f.Path + "\">\n" + f.Content + "\n</project_instructions>\n\n")
	}
	b.WriteString("</project_context>\n")
	return b.String()
}
