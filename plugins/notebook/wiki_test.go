package notebook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildWiki creates a small vault fixture and returns its 03_Wiki directory.
func buildWiki(t *testing.T) (vaultDir, wikiDir string) {
	t.Helper()

	vaultDir = t.TempDir()
	wikiDir = filepath.Join(vaultDir, "03_Wiki")
	writeWikiNote := func(rel, content string) {
		t.Helper()
		path := filepath.Join(wikiDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	writeWikiNote("Diabetes.md", `---
tags: [health, chronic]
summary: Blood sugar management.
---
Managing [[Insulin]] dosing. Related: [[Metformin|biguanide]].
`)
	writeWikiNote("Insulin.md", `---
tags: health, medication
---
See [[Diabetes]] and [[Diabetes#Hypoglycemia]] for context.
`)
	writeWikiNote("Metformin.md", `---
tags: "health, medication"
---
The [[diabetes|chronic condition]] page links here. #medication
`)
	writeWikiNote("Nutrition.md", `---
summary: Diet overview.
---
Balanced diets matter for #Health and general wellness.
`)
	writeWikiNote("unrelated.txt", "not a markdown file with [[Diabetes]] link\n")
	return vaultDir, wikiDir
}

func TestParseFrontmatter(t *testing.T) {
	t.Parallel()

	t.Run("valid frontmatter splits metadata and body", func(t *testing.T) {
		t.Parallel()

		meta, body := ParseFrontmatter("---\ntags: [health, diet]\nsummary: A note.\n---\n# Body\n")
		assert.Contains(t, meta, "tags")
		assert.Equal(t, "A note.", meta["summary"])
		assert.Equal(t, "\n# Body\n", body)
	})

	tests := []struct {
		name    string
		content string
	}{
		{"missing frontmatter returns full content", "# Just a body\n"},
		{"unclosed frontmatter falls back to body", "---\ntags: [lost]\n"},
		{"malformed yaml is tolerated", "---\ntags: [unclosed\n---\nbody\n"},
		{"non-mapping yaml is tolerated", "---\n- just\n- a list\n---\nbody\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			meta, body := ParseFrontmatter(tt.content)
			assert.Empty(t, meta)
			assert.Equal(t, tt.content, body)
		})
	}
}

func TestFindBacklinks(t *testing.T) {
	t.Parallel()

	_, wikiDir := buildWiki(t)

	results, err := FindBacklinks(wikiDir, "Diabetes")
	require.NoError(t, err)

	assert.Equal(t, []BacklinkResult{
		{Path: "03_Wiki/Insulin.md", Line: 4, Text: "See [[Diabetes]] and [[Diabetes#Hypoglycemia]] for context."},
		{Path: "03_Wiki/Metformin.md", Line: 4, Text: "The [[diabetes|chronic condition]] page links here. #medication"},
	}, results)
}

func TestFindBacklinksMatching(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		target    string
		wantPaths []string
	}{
		{"md suffix is stripped", "Diabetes.md", []string{"03_Wiki/Insulin.md", "03_Wiki/Metformin.md"}},
		{"subpath uses the stem", "concepts/Diabetes", []string{"03_Wiki/Insulin.md", "03_Wiki/Metformin.md"}},
		{"alias and anchor links match", "Insulin", []string{"03_Wiki/Diabetes.md"}},
		{"unknown target has no backlinks", "Nonexistent", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, wikiDir := buildWiki(t)
			results, err := FindBacklinks(wikiDir, tt.target)
			require.NoError(t, err)

			paths := make([]string, 0, len(results))
			for _, result := range results {
				paths = append(paths, result.Path)
			}
			if tt.wantPaths == nil {
				assert.Empty(t, paths)
				return
			}
			assert.ElementsMatch(t, tt.wantPaths, paths)
		})
	}
}

func TestFindBacklinksNoFalsePositives(t *testing.T) {
	t.Parallel()

	vaultDir := t.TempDir()
	wikiDir := filepath.Join(vaultDir, "03_Wiki")
	require.NoError(t, os.MkdirAll(wikiDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wikiDir, "page.md"), []byte(
		"[[Diabetes Mellitus]] and [[Diabetes2]] are different notes.\n"), 0o644))

	results, err := FindBacklinks(wikiDir, "Diabetes")
	require.NoError(t, err)
	assert.Empty(t, results, "longer note names must not match the target stem")
}

func TestFindBacklinksMissingWikiDir(t *testing.T) {
	t.Parallel()

	results, err := FindBacklinks(filepath.Join(t.TempDir(), "absent_wiki"), "Diabetes")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestFindTags(t *testing.T) {
	t.Parallel()

	_, wikiDir := buildWiki(t)

	results, err := FindTags(wikiDir, "health")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"03_Wiki/Diabetes.md",
		"03_Wiki/Insulin.md",
		"03_Wiki/Metformin.md",
		"03_Wiki/Nutrition.md",
	}, results, "frontmatter list, comma string and inline hashtag all match, without duplicates")
}

func TestFindTagsForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  string
		want []string
	}{
		{"leading hash is optional", "#health", []string{
			"03_Wiki/Diabetes.md", "03_Wiki/Insulin.md", "03_Wiki/Metformin.md", "03_Wiki/Nutrition.md",
		}},
		{"comma separated frontmatter string", "medication", []string{
			"03_Wiki/Insulin.md", "03_Wiki/Metformin.md",
		}},
		{"frontmatter list item", "chronic", []string{"03_Wiki/Diabetes.md"}},
		{"unknown tag", "missing-tag", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, wikiDir := buildWiki(t)
			results, err := FindTags(wikiDir, tt.tag)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, results)
		})
	}
}

func TestListAllTags(t *testing.T) {
	t.Parallel()

	_, wikiDir := buildWiki(t)

	tags, err := ListAllTags(wikiDir)
	require.NoError(t, err)

	assert.Equal(t, map[string]int{
		"health": 4,
		// The inline scanner also picks up wikilink anchors such as
		// [[Diabetes#Hypoglycemia]], mirroring the original script.
		"medication":   2,
		"chronic":      1,
		"hypoglycemia": 1,
	}, tags, "tags repeated within one note count once per note")
}

func TestSearchKeywords(t *testing.T) {
	t.Parallel()

	_, wikiDir := buildWiki(t)

	results, err := SearchKeywords(wikiDir, []string{"insulin", "biguanide"})
	require.NoError(t, err)

	assert.Equal(t, []SearchResult{
		{Path: "03_Wiki/Diabetes.md", Line: 5, Text: "Managing [[Insulin]] dosing. Related: [[Metformin|biguanide]]."},
	}, results)
}

func TestSearchKeywordsSynonymsCaseInsensitive(t *testing.T) {
	t.Parallel()

	vaultDir := t.TempDir()
	wikiDir := filepath.Join(vaultDir, "03_Wiki")
	require.NoError(t, os.MkdirAll(wikiDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wikiDir, "care.md"), []byte(
		"Take MEDICATIONS daily.\n糖尿病 management matters.\n"), 0o644))

	results, err := SearchKeywords(wikiDir, []string{"medications", "糖尿病"})
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "Take MEDICATIONS daily.", results[0].Text)
	assert.Equal(t, "糖尿病 management matters.", results[1].Text)
}

func TestSearchKeywordsNoTerms(t *testing.T) {
	t.Parallel()

	_, wikiDir := buildWiki(t)

	results, err := SearchKeywords(wikiDir, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}
