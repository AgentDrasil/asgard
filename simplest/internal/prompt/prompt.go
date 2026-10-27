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

// gitPaths holds the git metadata discovered by findGitPaths.
type gitPaths struct {
	repoDir      string
	commonGitDir string
}

// Port of pi 005af57d8: findGitPaths by walking up from cwd. Handles both
// regular git repos (.git is a directory) and linked worktrees (.git is a
// file pointing at <main>/.git/worktrees/<name>).
func findGitPaths(cwd string) *gitPaths {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		dir = cwd
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(gitPath)
				if err != nil {
					return nil
				}
				content := strings.TrimSpace(string(data))
				if gitdir, ok := strings.CutPrefix(content, "gitdir: "); ok {
					gitDir := gitdir
					if !filepath.IsAbs(gitDir) {
						gitDir = filepath.Join(dir, gitDir)
					}
					if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
						return nil
					}
					commonGitDir := gitDir
					if cd, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
						rel := strings.TrimSpace(string(cd))
						if !filepath.IsAbs(rel) {
							rel = filepath.Join(gitDir, rel)
						}
						commonGitDir = rel
					}
					return &gitPaths{repoDir: dir, commonGitDir: commonGitDir}
				}
				// Malformed .git file: keep walking up.
			} else if info.IsDir() {
				if _, err := os.Stat(filepath.Join(gitPath, "HEAD")); err != nil {
					return nil
				}
				return &gitPaths{repoDir: dir, commonGitDir: gitPath}
			}
			// Neither file nor dir: keep walking up.
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// canonicalizePath resolves symlinks, falling back to the raw path when the
// target does not exist (or resolution fails).
func canonicalizePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// Port of pi 005af57d8: findShadowedContextFile detects when cwd sits inside a
// nested linked worktree whose main repo root carries a context file of the
// same name. Returns the canonicalized main-repo file path that must be
// shadowed, or "" when nothing is shadowed.
func findShadowedContextFile(cwd string) string {
	paths := findGitPaths(cwd)
	if paths == nil {
		return ""
	}
	commonGitDir := canonicalizePath(paths.commonGitDir)
	worktreeRoot := canonicalizePath(paths.repoDir)
	mainRepoRoot := filepath.Dir(commonGitDir)
	// False for an ordinary repo, where the two are the same dir, and for a
	// sibling worktree (`git worktree add ../feat`), whose main repo is not an
	// ancestor.
	if !strings.HasPrefix(worktreeRoot, mainRepoRoot+string(os.PathSeparator)) {
		return ""
	}
	// dirname of the common git dir is the main worktree root only when that
	// dir is itself checked out from the same repo. Excludes bare layouts
	// (`proj/.bare` + `proj/main`) and submodules.
	if canonicalizePath(filepath.Join(mainRepoRoot, ".git")) != commonGitDir {
		return ""
	}
	worktreeContextFile := loadContextFileFromDir(worktreeRoot)
	if worktreeContextFile == nil {
		return ""
	}
	return filepath.Join(mainRepoRoot, filepath.Base(worktreeContextFile.Path))
}

// LoadProjectContextFiles collects instruction files: the global one from
// agentDir first, then one per directory walking from cwd up to the root,
// ordered root-most first (nearest directory last). Missing files are skipped;
// read errors are ignored on a best-effort basis. When cwd is inside a nested
// linked worktree, a same-named context file at the main repo root is
// shadowed by the worktree's own copy and skipped.
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

	shadowed := findShadowedContextFile(resolvedCwd)

	var ancestors []ContextFile
	dir := resolvedCwd
	for {
		if cf := loadContextFileFromDir(dir); cf != nil && !seen[cf.Path] {
			// Port of pi f1b2e77f5: compare the directory's real path plus the
			// file's basename, not the file's real path — a worktree AGENTS.md
			// symlinked to the main repo's file must still load exactly once
			// (the worktree copy), while the main-repo copy is shadowed.
			isShadowed := shadowed != "" &&
				filepath.Join(canonicalizePath(dir), filepath.Base(cf.Path)) == shadowed
			if !isShadowed {
				seen[cf.Path] = true
				ancestors = append(ancestors, *cf)
			}
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
	// CustomPrompt replaces the default identity paragraph; the tools list,
	// guidelines, context files, and cwd line are still assembled around it.
	CustomPrompt string
	// SelectedTools lists the active tool names. Defaults to read/bash/edit/write.
	SelectedTools []string
	// ToolSnippets maps tool name to its one-line "Available tools" entry;
	// tools without a snippet are listed by name only.
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
	// Documentation paths rendered in a "Documentation" section of the
	// prompt: ReadmePath as "Main documentation", DocsPath as "Additional
	// docs", and ExamplesPath as "Examples". Empty paths are omitted; when
	// all three are empty the whole section is omitted.
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

	identity := baseIdentity
	if custom := strings.TrimSpace(opts.CustomPrompt); custom != "" {
		identity = custom
	}

	visibleTools := make([]string, 0, len(tools))
	for _, name := range tools {
		if snippet := opts.ToolSnippets[name]; snippet != "" {
			visibleTools = append(visibleTools, "- "+name+": "+snippet)
		} else {
			visibleTools = append(visibleTools, "- "+name)
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

	b.WriteString(identity + "\n\nAvailable tools:\n" + toolsList + "\n\nGuidelines:\n" + strings.Join(guidelines, "\n"))

	// Port of pi 005af57d8: render the documentation section from configured paths.
	if docs := documentationSection(opts); docs != "" {
		b.WriteString(docs)
	}

	b.WriteString(appendSection)
	b.WriteString(projectContextSection(contextFiles))
	b.WriteString("\nCurrent working directory: " + promptCwd)

	return b.String()
}

func documentationSection(opts Options) string {
	if opts.ReadmePath == "" && opts.DocsPath == "" && opts.ExamplesPath == "" {
		return ""
	}
	var lines []string
	if opts.ReadmePath != "" {
		lines = append(lines, "- Main documentation: "+opts.ReadmePath)
	}
	if opts.DocsPath != "" {
		lines = append(lines, "- Additional docs: "+opts.DocsPath)
	}
	if opts.ExamplesPath != "" {
		lines = append(lines, "- Examples: "+opts.ExamplesPath)
	}
	return "\n\nDocumentation (read only when the user asks about the harness itself, its SDK, or its tools):\n" +
		strings.Join(lines, "\n")
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
