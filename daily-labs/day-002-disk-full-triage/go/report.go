package main

import (
	"fmt"
	"sort"
	"strings"
)

// Finding is one CRIT/WARN line plus its indented details.
type Finding struct {
	Severity string   `json:"severity"`
	Kind     string   `json:"kind"` // "space" or "inodes"
	Mount    string   `json:"mount"`
	Message  string   `json:"message"`
	Details  []string `json:"details"`
}

// Report is the triage result for one host.
type Report struct {
	Host     string
	Findings []Finding
}

func (r Report) count(sev string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

// Result is OK, WARN or CRIT.
func (r Report) Result() string {
	switch {
	case r.count("CRIT") > 0:
		return "CRIT"
	case r.count("WARN") > 0:
		return "WARN"
	}
	return "OK"
}

// ExitCode follows the contract: 0 OK, 1 WARN, 2 CRIT.
func (r Report) ExitCode() int {
	return map[string]int{"OK": 0, "WARN": 1, "CRIT": 2}[r.Result()]
}

// Text renders the byte-for-byte contract report.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "disktriage report: %s\n", r.Host)
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%s %s\n", f.Severity, f.Message)
		for _, d := range f.Details {
			fmt.Fprintf(&b, "  %s\n", d)
		}
	}
	fmt.Fprintf(&b, "RESULT %s crit=%d warn=%d\n", r.Result(), r.count("CRIT"), r.count("WARN"))
	return b.String()
}

// Thresholds are the report knobs (-w, -c, -r).
type Thresholds struct {
	Warn, Crit, RetainDays int64
}

// ceilPct is df's formula: used / (used + avail), rounded up. Integers only.
func ceilPct(part, total int64) int64 {
	if total <= 0 {
		return 0
	}
	n := part * 100
	q := n / total
	if n%total != 0 {
		q++
	}
	return q
}

func mib(kb int64) int64 { return kb / 1024 }

// ownerMount is a longest-prefix match on whole path components: /database is NOT under /data.
func ownerMount(path string, mounts []string) string {
	best := ""
	for _, m := range mounts {
		if (m == "/" || path == m || strings.HasPrefix(path, m+"/")) && len(m) > len(best) {
			best = m
		}
	}
	return best
}

func severity(pct int64, t Thresholds) string {
	switch {
	case pct >= t.Crit:
		return "CRIT"
	case pct >= t.Warn:
		return "WARN"
	}
	return ""
}

// Analyse applies the contract rules to one snapshot.
func Analyse(s *Snapshot, t Thresholds) Report {
	mounts := make([]string, 0, len(s.FS))
	for _, f := range s.FS {
		mounts = append(mounts, f.Mount)
	}
	var out []Finding
	for _, fs := range s.FS {
		pct := ceilPct(fs.UsedKB, fs.UsedKB+fs.AvailKB)
		if sev := severity(pct, t); sev != "" {
			out = append(out, spaceFinding(s, fs, sev, pct, mounts, t))
		}
		ipct := ceilPct(fs.IUsed, fs.IUsed+fs.IFree) // vfat reports 0/0 inodes: 0%, no divide-by-zero
		if sev := severity(ipct, t); sev != "" {
			out = append(out, Finding{Severity: sev, Kind: "inodes", Mount: fs.Mount,
				Message: fmt.Sprintf("inodes %s %d%% used", fs.Mount, ipct), Details: []string{}})
		}
	}
	rank := map[string]int{"CRIT": 0, "WARN": 1}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.Severity] != rank[b.Severity] {
			return rank[a.Severity] < rank[b.Severity]
		}
		if a.Mount != b.Mount {
			return a.Mount < b.Mount // Go string compare is byte order, like LC_ALL=C
		}
		return a.Kind == "space" && b.Kind != "space"
	})
	return Report{Host: s.Host, Findings: out}
}

func spaceFinding(s *Snapshot, fs FS, sev string, pct int64, mounts []string, t Thresholds) Finding {
	mine := func(p string) bool { return ownerMount(p, mounts) == fs.Mount }
	total := fs.UsedKB + fs.AvailKB
	details := []string{}

	var top []DU
	for _, d := range s.DU {
		if mine(d.Path) {
			top = append(top, d)
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].KB != top[j].KB {
			return top[i].KB > top[j].KB
		}
		return top[i].Path < top[j].Path
	})
	if len(top) > 3 {
		top = top[:3]
	}
	for _, d := range top {
		details = append(details, fmt.Sprintf("top %d MiB %s", mib(d.KB), d.Path))
	}

	var held []Deleted
	for _, d := range s.Deleted {
		if mine(d.Path) {
			held = append(held, d)
		}
	}
	sort.Slice(held, func(i, j int) bool {
		if held[i].KB != held[j].KB {
			return held[i].KB > held[j].KB
		}
		return held[i].PID < held[j].PID
	})
	byInode := map[int64]int64{} // two processes holding one deleted file free it only once
	for _, d := range held {
		details = append(details, fmt.Sprintf("deleted %d MiB pid=%d %s %s", mib(d.KB), d.PID, d.Comm, d.Path))
		byInode[d.Inode] = d.KB
	}
	var deletedKB int64
	for _, kb := range byInode {
		deletedKB += kb
	}

	var rotN, rotatedKB int64
	for _, r := range s.Rotated {
		if mine(r.Path) && r.AgeDays >= t.RetainDays {
			rotN++
			rotatedKB += r.KB
		}
	}
	if rotN > 0 {
		details = append(details, fmt.Sprintf("rotated %d files %d MiB older than %dd", rotN, mib(rotatedKB), t.RetainDays))
	}

	freed := deletedKB + rotatedKB
	after := ceilPct(max(fs.UsedKB-freed, 0), total)
	if freed > 0 {
		details = append(details, fmt.Sprintf("after cleanup %d%% (deleted-open %d MiB + rotated %d MiB)", after, mib(deletedKB), mib(rotatedKB)))
	}
	if after >= t.Warn {
		details = append(details, fmt.Sprintf("needs capacity: still %d%% after cleanup (>= %d%%)", after, t.Warn))
	}
	return Finding{Severity: sev, Kind: "space", Mount: fs.Mount,
		Message: fmt.Sprintf("space %s %d%% used, %d MiB free", fs.Mount, pct, mib(fs.AvailKB)), Details: details}
}
