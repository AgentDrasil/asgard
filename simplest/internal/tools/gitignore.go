package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ignoreRule is one compiled .gitignore pattern line.
type ignoreRule struct {
	negated  bool
	dirOnly  bool
	anchored bool
	re       *regexp.Regexp
}

// matches reports whether the rule matches rel (relative to the .gitignore dir).
func (r *ignoreRule) matches(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	if r.anchored {
		return r.re.MatchString(rel)
	}
	base := rel
	if idx := strings.LastIndex(rel, "/"); idx >= 0 {
		base = rel[idx+1:]
	}
	return r.re.MatchString(base)
}

// compileGitignore parses the contents of a .gitignore file located in the
// directory at relative path base ("" for the walk root).
func compileGitignore(content string) []*ignoreRule {
	var rules []*ignoreRule
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := &ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		line = strings.TrimPrefix(line, "/")
		rule.anchored = strings.Contains(line, "/")
		re, err := globToRegexp(line)
		if err != nil {
			continue
		}
		rule.re = re
		rules = append(rules, rule)
	}
	return rules
}

// ignoreList is a compiled .gitignore plus the relative dir it lives in.
type ignoreList struct {
	base  string // "" for walk root, otherwise "a/b" style with no trailing slash
	rules []*ignoreRule
}

// ignoreStack tracks active .gitignore files during a walk, innermost last.
type ignoreStack []ignoreList

// ignored decides whether rel (slash-separated, relative to walk root) is
// ignored. The innermost matching rule wins, mirroring git's precedence.
func (s ignoreStack) ignored(rel string, isDir bool) bool {
	for i := len(s) - 1; i >= 0; i-- {
		list := s[i]
		relToBase := rel
		if list.base != "" {
			prefix := list.base + "/"
			if !strings.HasPrefix(rel, prefix) {
				continue
			}
			relToBase = strings.TrimPrefix(rel, prefix)
		}
		for j := len(list.rules) - 1; j >= 0; j-- {
			if list.rules[j].matches(relToBase, isDir) {
				return !list.rules[j].negated
			}
		}
	}
	return false
}

// walker performs a gitignore-aware depth-first walk of root. The ".git"
// directory is always skipped. Hidden files are included (matching ripgrep's
// --hidden flag). Directories are pruned when ignored, matching git semantics
// where negation cannot re-include inside an excluded directory. Entries are
// visited in sorted order for determinism. When root is a file, fn runs once
// for it.
func walker(root string, fn func(absPath, rel string, d fs.DirEntry) error) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	var stack ignoreStack
	if content, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
		stack = append(stack, ignoreList{rules: compileGitignore(string(content))})
	}
	var visit func(dirAbs, dirRel string, curStack ignoreStack) error
	visit = func(dirAbs, dirRel string, curStack ignoreStack) error {
		entries, err := os.ReadDir(dirAbs)
		if err != nil {
			return nil // unreadable directories are skipped
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			name := e.Name()
			childRel := name
			if dirRel != "" {
				childRel = dirRel + "/" + name
			}
			childAbs := filepath.Join(dirAbs, name)
			if e.IsDir() {
				if name == ".git" {
					continue
				}
				if curStack.ignored(childRel, true) {
					continue
				}
				next := curStack
				if content, err := os.ReadFile(filepath.Join(childAbs, ".gitignore")); err == nil {
					next = append(append(ignoreStack{}, curStack...), ignoreList{base: childRel, rules: compileGitignore(string(content))})
				}
				if err := visit(childAbs, childRel, next); err != nil {
					return err
				}
			} else if e.Type().IsRegular() {
				if curStack.ignored(childRel, false) {
					continue
				}
				if err := fn(childAbs, childRel, e); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(root, "", stack)
}

// errStopWalk aborts a walk early when the caller has collected enough.
var errStopWalk = errors.New("stop walk")

// walkWithGitignore runs the gitignore-aware walker, stopping early when fn
// returns errStopWalk.
func walkWithGitignore(root string, fn func(absPath, rel string, isDir bool) error) error {
	return walker(root, func(absPath, rel string, d fs.DirEntry) error {
		return fn(absPath, rel, d.IsDir())
	})
}
