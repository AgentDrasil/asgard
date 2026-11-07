package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// quotaThreshold is the remaining fraction of one quota window at or below
// which an account is no longer used.
type quotaThreshold struct {
	Window string
	Value  float64
}

// quotaPreset is a ready-made quota_profiles entry of the aw config. The
// names and semantics mirror agentwrapper/config.QuotaProfile; the tool is
// kept free of that dependency tree.
type quotaPreset struct {
	Name       string
	Desc       string
	Thresholds []quotaThreshold
	Unlimited  bool
}

var quotaPresets = []quotaPreset{
	{
		Name:       "pro20",
		Desc:       "$20 plan: stop below 10% of the 5h window or 5% of the weekly one",
		Thresholds: []quotaThreshold{{"5h", 0.10}, {"weekly", 0.05}},
	},
	{
		Name:       "max200",
		Desc:       "$200 plan: stop below 3% of the 5h window or 2% of the weekly one",
		Thresholds: []quotaThreshold{{"5h", 0.03}, {"weekly", 0.02}},
	},
	{
		Name:      "ppr",
		Desc:      "pay-per-request account: always usable",
		Unlimited: true,
	},
}

func quotaPresetByName(name string) (quotaPreset, bool) {
	for _, p := range quotaPresets {
		if p.Name == name {
			return p, true
		}
	}
	return quotaPreset{}, false
}

func quotaPresetNames() string {
	names := make([]string, 0, len(quotaPresets))
	for _, p := range quotaPresets {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// parseQuotaProfiles parses a comma-separated list of preset names, dropping
// duplicates and keeping the given order. An empty list is valid and means
// "generate no profiles".
func parseQuotaProfiles(s string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if _, ok := quotaPresetByName(name); !ok {
			return nil, fmt.Errorf("invalid quota profile %q: supported profiles are %s", name, quotaPresetNames())
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// QuotaProfileDefs returns the presets selected in QuotaProfiles, for the aw
// config template.
func (c *ConfigData) QuotaProfileDefs() []quotaPreset {
	var defs []quotaPreset
	for _, name := range c.QuotaProfiles {
		if p, ok := quotaPresetByName(name); ok {
			defs = append(defs, p)
		}
	}
	return defs
}

// PromptQuotaProfiles asks which quota profiles to generate into the aw
// config. An empty answer, or end of input, generates none.
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
	_, _ = fmt.Fprintf(w, "A profile sets, per quota window, when an account is too low to use. Without any profile\nthe fixed rule applies (automatic selection needs more than 10%% remaining).\n")
	for _, p := range quotaPresets {
		_, _ = fmt.Fprintf(w, "  %s%s%s - %s\n", colorYellow, p.Name, colorReset, p.Desc)
	}
	for {
		_, _ = fmt.Fprintf(w, "%sProfiles to generate (comma separated, empty for none):%s ", colorBold, colorReset)
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) == "" {
			return nil
		}
		names, perr := parseQuotaProfiles(line)
		if perr != nil {
			_, _ = fmt.Fprintf(w, "%s%v%s\n", colorRed, perr, colorReset)
			if err != nil {
				return nil
			}
			continue
		}
		return names
	}
}

// printQuotaBindingHint tells how to bind runner accounts to the generated
// profiles; bindings live next to the credentials on the runner, not in etcd.
func printQuotaBindingHint(data *ConfigData) {
	if len(data.QuotaProfiles) == 0 {
		return
	}
	fmt.Printf("     Bind each account to a quota profile (%s%s%s), at login or later:\n", colorYellow, strings.Join(data.QuotaProfiles, ", "), colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw <agy|claude> login <alias> --profile <name>%s\n", colorCyan, data.InitialRunner, colorReset)
	fmt.Printf("     %sdocker exec -it asgard-%s aw <agy|claude> profile <alias> <name>%s\n", colorCyan, data.InitialRunner, colorReset)
	fmt.Printf("     Unbound accounts use the %sdefault%s profile if you define one, otherwise the fixed 10%% rule.\n\n", colorYellow, colorReset)
}
