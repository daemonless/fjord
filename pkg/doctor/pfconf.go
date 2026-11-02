package doctor

import "strings"

// pfInsert adds to a pf.conf the rules it lacks, before its first filter
// rule (pass/block/match/antispoof/anchor) or at the end when there are
// none: pf rejects the whole file when a translation rule follows a filter
// rule ("Rules must be in order"). The second value is what was added, so a
// file that has every line comes back unchanged with nothing to report.
// The Go twin of pfInsertSnippet, for fjord doing the edit itself.
func pfInsert(conf string, rules []string) (string, []string) {
	have := map[string]bool{}
	for _, l := range strings.Split(conf, "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var miss []string
	for _, r := range rules {
		if !have[r] {
			miss = append(miss, r)
		}
	}
	if len(miss) == 0 {
		return conf, nil
	}
	block := strings.Join(miss, "\n") + "\n"
	lines := strings.Split(conf, "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) == 0 || !strings.HasPrefix(l, f[0]) {
			continue
		}
		switch f[0] {
		case "pass", "block", "match", "antispoof", "anchor":
			return strings.Join(lines[:i], "\n") + "\n" + block + strings.Join(lines[i:], "\n"), miss
		}
	}
	if conf != "" && !strings.HasSuffix(conf, "\n") {
		conf += "\n"
	}
	return conf + block, miss
}
