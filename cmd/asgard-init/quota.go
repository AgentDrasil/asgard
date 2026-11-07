package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// quotaThreshold is the remaining fraction of one quota window (a limit name
// as `aw usage` reports it, or a glob / "*") at or below which an account is
// no longer used.
type quotaThreshold struct {
	Window string
	Value  float64
}

// quotaProfile is one quota_profiles entry of the aw config. The semantics
// mirror agentwrapper/config.QuotaProfile; the tool is kept free of that
// dependency tree. Nothing about plans or windows is built in: plans differ
// per CLI and size, and some have no 5h or weekly window at all.
type quotaProfile struct {
	Name       string
	Thresholds []quotaThreshold
	Unlimited  bool
}

// quotaProfileNameRe mirrors accounts.ValidateProfile of agentwrapper.
var quotaProfileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

const quotaSpecSyntax = `<name>:<window>=<ratio>[,<window>=<ratio>...]  or  <name>:unlimited`

// parseQuotaProfile parses "<name>:<window>=<ratio>,..." or
// "<name>:unlimited". A window is a limit name (e.g. 5h, weekly,
// "weekly:sonnet only", TOKENS_LIMIT), a glob such as weekly*, or "*" for any
// window; the ratio is the remaining fraction in [0, 1].
func parseQuotaProfile(spec string) (quotaProfile, error) {
	name, rest, ok := strings.Cut(spec, ":")
	name, rest = strings.TrimSpace(name), strings.TrimSpace(rest)
	if !ok || rest == "" {
		return quotaProfile{}, fmt.Errorf("invalid quota profile %q: want %s", spec, quotaSpecSyntax)
	}
	if !quotaProfileNameRe.MatchString(name) {
		return quotaProfile{}, fmt.Errorf("invalid quota profile name %q: must match %s", name, quotaProfileNameRe)
	}
	p := quotaProfile{Name: name}
	if rest == "unlimited" {
		p.Unlimited = true
		return p, nil
	}
	seen := make(map[string]bool)
	for _, pair := range strings.Split(rest, ",") {
		i := strings.LastIndex(pair, "=")
		if i < 0 {
			return quotaProfile{}, fmt.Errorf("invalid threshold %q in profile %q: want <window>=<ratio>", strings.TrimSpace(pair), name)
		}
		window, raw := strings.TrimSpace(pair[:i]), strings.TrimSpace(pair[i+1:])
		if window == "" {
			return quotaProfile{}, fmt.Errorf("empty window in profile %q", name)
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || value > 1 {
			return quotaProfile{}, fmt.Errorf("invalid ratio %q for window %q in profile %q: want a number from 0 to 1", raw, window, name)
		}
		if seen[window] {
			return quotaProfile{}, fmt.Errorf("window %q appears twice in profile %q", window, name)
		}
		seen[window] = true
		p.Thresholds = append(p.Thresholds, quotaThreshold{window, value})
	}
	return p, nil
}

// normalizeQuotaProfiles validates the specs and returns them in canonical
// form. A profile name may be given only once.
func normalizeQuotaProfiles(specs []string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, spec := range specs {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		p, err := parseQuotaProfile(spec)
		if err != nil {
			return nil, err
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("quota profile %q is defined twice", p.Name)
		}
		seen[p.Name] = true
		out = append(out, p.spec())
	}
	return out, nil
}

// spec is the canonical text form of the profile (what parseQuotaProfile
// reads back).
func (p quotaProfile) spec() string {
	if p.Unlimited {
		return p.Name + ":unlimited"
	}
	pairs := make([]string, 0, len(p.Thresholds))
	for _, t := range p.Thresholds {
		pairs = append(pairs, t.Window+"="+formatRatio(t.Value))
	}
	return p.Name + ":" + strings.Join(pairs, ",")
}

func formatRatio(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// quoteYAML always double-quotes a key: windows such as "*" or
// "weekly:sonnet only" are not valid bare keys, and names like "20" or "on"
// must stay strings.
func quoteYAML(s string) string { return strconv.Quote(s) }

// yamlLines renders the profile as a quota_profiles entry; indent is the
// entry's own indentation and its fields sit one level deeper.
func (p quotaProfile) yamlLines(indent string) []string {
	lines := []string{indent + quoteYAML(p.Name) + ":"}
	inner := indent + indent
	if p.Unlimited {
		return append(lines, inner+"unlimited: true")
	}
	parts := make([]string, 0, len(p.Thresholds))
	for _, t := range p.Thresholds {
		parts = append(parts, quoteYAML(t.Window)+": "+formatRatio(t.Value))
	}
	return append(lines, inner+"thresholds: { "+strings.Join(parts, ", ")+" }")
}

// quotaProfileDefs parses the specs held by ConfigData; they were validated
// when collected, so a bad one is skipped.
func quotaProfileDefs(specs []string) []quotaProfile {
	var defs []quotaProfile
	for _, s := range specs {
		if p, err := parseQuotaProfile(s); err == nil {
			defs = append(defs, p)
		}
	}
	return defs
}

const quotaProfilesHeader = `# Quota policy profiles: per quota window, the remaining fraction at or below
# which an account is no longer used. A window is a limit name as
# ` + "`aw usage --cli <cli>`" + ` reports it (limits[].name), a glob such as "weekly*", or "*"
# for any window; windows a plan does not have are ignored, and a window without
# a threshold only counts when exhausted. Bind accounts on the runner with
# ` + "`aw <agy|claude> login <alias> --profile <name>`" + ` or
# ` + "`aw <agy|claude> profile <alias> <name>`" + `. Accounts without a binding use the
# ` + "`default`" + ` profile when defined; with no profile at all the fixed rule applies
# (automatic selection needs more than 10% remaining).`

// QuotaProfilesBlock renders the commented quota_profiles section for the aw
// config template.
func (c *ConfigData) QuotaProfilesBlock() string {
	lines := []string{quotaProfilesHeader, "quota_profiles:"}
	for _, p := range quotaProfileDefs(c.QuotaProfiles) {
		lines = append(lines, p.yamlLines("  ")...)
	}
	return strings.Join(lines, "\n")
}

var (
	quotaSectionRe = regexp.MustCompile(`^quota_profiles:\s*(#.*)?$`)
	quotaEntryRe   = regexp.MustCompile(`^("[^"]*"|'[^']*'|[^\s:]+):(\s|$)`)
)

// mergeQuotaProfiles adds the profiles to the aw config text. Profiles the
// config already defines are left exactly as they are (reported in skipped),
// as is everything else in the file: new entries go right below an existing
// quota_profiles section, or in a new section at the end.
func mergeQuotaProfiles(existing string, profiles []quotaProfile) (updated string, added, skipped []string, err error) {
	lines := strings.Split(existing, "\n")

	header := -1
	for i, l := range lines {
		if quotaSectionRe.MatchString(l) {
			header = i
			break
		}
		if strings.HasPrefix(l, "quota_profiles:") {
			return "", nil, nil, fmt.Errorf("quota_profiles is written inline in the aw config; add the profiles by hand")
		}
	}

	indent := "  "
	have := make(map[string]bool)
	if header >= 0 {
		first := true
		for _, l := range lines[header+1:] {
			trimmed := strings.TrimSpace(l)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			lead := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			if lead == "" {
				break // next top-level key
			}
			if first {
				indent, first = lead, false
			}
			if lead == indent {
				if m := quotaEntryRe.FindStringSubmatch(trimmed); m != nil {
					have[strings.Trim(m[1], `"'`)] = true
				}
			}
		}
	}

	var entries []string
	for _, p := range profiles {
		if have[p.Name] {
			skipped = append(skipped, p.Name)
			continue
		}
		added = append(added, p.Name)
		entries = append(entries, p.yamlLines(indent)...)
	}
	if len(added) == 0 {
		return existing, nil, skipped, nil
	}

	if header >= 0 {
		out := append([]string{}, lines[:header+1]...)
		out = append(out, entries...)
		out = append(out, lines[header+1:]...)
		return strings.Join(out, "\n"), added, skipped, nil
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimRight(existing, "\n"))
	sb.WriteString("\n\n" + quotaProfilesHeader + "\nquota_profiles:\n")
	sb.WriteString(strings.Join(entries, "\n") + "\n")
	return sb.String(), added, skipped, nil
}

// PromptQuotaProfiles interactively collects quota profiles, one at a time,
// until an empty profile name. It returns canonical specs.
func PromptQuotaProfiles(r io.Reader, w io.Writer) []string {
	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r != nil {
		reader = bufio.NewReader(r)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}

	_, _ = fmt.Fprintf(w, "\n%s--- quota profiles ---%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "A profile says, per quota window, how little remaining quota an account may have before it\nis no longer used. Plans differ per CLI and size, so name them yourself (e.g. %sclaude-20%s,\n%sagy-50%s) and give one threshold per window the plan has.\n", colorYellow, colorReset, colorYellow, colorReset)
	_, _ = fmt.Fprintf(w, "  • Window names are what the CLI reports: %sdocker exec -it asgard-<runner> aw usage --cli <cli>%s,\n    see limits[].name (claude: 5h, weekly, weekly:<model>; agy: 5h, weekly; other providers\n    name their own, e.g. TOKENS_LIMIT). Use * for any window.\n", colorCyan, colorReset)
	_, _ = fmt.Fprintf(w, "  • Thresholds are remaining fractions: %s5h=0.10,weekly=0.05%s stops below 10%% of the 5h window or\n    5%% of the weekly one. A plan without a 5h window just leaves it out.\n", colorYellow, colorReset)
	_, _ = fmt.Fprintf(w, "  • Type %sunlimited%s for pay-per-request accounts. Without any profile the fixed rule applies\n    (automatic selection needs more than 10%% remaining).\n", colorYellow, colorReset)

	var specs []string
	taken := make(map[string]bool)
	for {
		_, _ = fmt.Fprintf(w, "\n%sProfile name (empty to finish):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		name := strings.TrimSpace(line)
		if name == "" {
			return specs
		}
		if !quotaProfileNameRe.MatchString(name) {
			_, _ = fmt.Fprintf(w, "%sinvalid quota profile name %q: must match %s%s\n", colorRed, name, quotaProfileNameRe, colorReset)
			if err != nil {
				return specs
			}
			continue
		}
		if taken[name] {
			_, _ = fmt.Fprintf(w, "%sprofile %q was already entered%s\n", colorRed, name, colorReset)
			if err != nil {
				return specs
			}
			continue
		}

		for {
			_, _ = fmt.Fprintf(w, "%sThresholds for %s (<window>=<ratio>,... or unlimited):%s ", colorBold, name, colorReset)
			line, err = reader.ReadString('\n')
			text := strings.TrimSpace(line)
			if text == "" {
				if err != nil {
					return specs
				}
				continue
			}
			p, perr := parseQuotaProfile(name + ":" + text)
			if perr == nil {
				specs = append(specs, p.spec())
				taken[name] = true
				break
			}
			_, _ = fmt.Fprintf(w, "%s%v%s\n", colorRed, perr, colorReset)
			if err != nil {
				return specs
			}
		}
	}
}

// printQuotaBindingHint tells how to bind runner accounts to the generated
// profiles; bindings live next to the credentials on the runner, not in etcd.
func printQuotaBindingHint(data *ConfigData) {
	defs := quotaProfileDefs(data.QuotaProfiles)
	if len(defs) == 0 {
		return
	}
	names := make([]string, 0, len(defs))
	for _, p := range defs {
		names = append(names, p.Name)
	}
	fmt.Printf("     Bind each account to a quota profile (%s%s%s), at login or later:\n", colorYellow, strings.Join(names, ", "), colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw <agy|claude> login <alias> --profile <name>%s\n", colorCyan, data.InitialRunner, colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw <agy|claude> profile <alias> <name>%s\n", colorCyan, data.InitialRunner, colorReset)
	fmt.Printf("     Unbound accounts use the %sdefault%s profile if you define one, otherwise the fixed 10%% rule.\n\n", colorYellow, colorReset)
}

// extractBareQuotaProfiles detects `--quota-profiles` given without a value
// (last argument, or followed by another flag), which selects the interactive
// update of an existing deployment. The bare flag is removed from the args.
// A valued `--quota-profiles x` is left for the flag parser to reject.
func extractBareQuotaProfiles(args []string) ([]string, bool) {
	var out []string
	bare := false
	for i, a := range args {
		if (a == "--quota-profiles" || a == "-quota-profiles") && (i+1 == len(args) || strings.HasPrefix(args[i+1], "-")) {
			bare = true
			continue
		}
		out = append(out, a)
	}
	return out, bare
}

// runQuotaProfilesUpdate interactively adds quota profiles to the aw config
// of an existing deployment, without regenerating anything else.
func runQuotaProfilesUpdate(targetDir string, r io.Reader, w io.Writer) error {
	cfgPath := filepath.Join(targetDir, "etcd-tree", "aw", "config.yaml")
	existing, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no Asgard deployment found in %s (missing etcd-tree/aw/config.yaml); run asgard-init first", targetDir)
		}
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}

	specs := PromptQuotaProfiles(r, w)
	if len(specs) == 0 {
		_, _ = fmt.Fprintf(w, "\nNo profiles entered; %s left unchanged.\n", cfgPath)
		return nil
	}
	updated, added, skipped, err := mergeQuotaProfiles(string(existing), quotaProfileDefs(specs))
	if err != nil {
		return err
	}
	for _, name := range skipped {
		_, _ = fmt.Fprintf(w, "%sProfile %q is already defined in the aw config; left unchanged.%s\n", colorYellow, name, colorReset)
	}
	if len(added) == 0 {
		return nil
	}
	if err := os.WriteFile(cfgPath, []byte(updated), 0644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	_, _ = fmt.Fprintf(w, "\n%s✔ Added quota profiles to %s:%s %s\n", colorGreen+colorBold, cfgPath, colorReset, strings.Join(added, ", "))
	_, _ = fmt.Fprintf(w, "\nNext steps:\n  1. Publish the config to etcd:  %sjust publish%s\n", colorCyan+colorBold, colorReset)
	_, _ = fmt.Fprintf(w, "  2. Bind each account on its runner:\n")
	runner := "<runner>"
	if entries, err := os.ReadDir(filepath.Join(targetDir, "runners")); err == nil {
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
		if len(dirs) == 1 {
			runner = dirs[0]
		}
	}
	_, _ = fmt.Fprintf(w, "     %sdocker exec -it asgard-%s aw <agy|claude> profile <alias> <name>%s\n", colorCyan, runner, colorReset)
	return nil
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, "; ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
