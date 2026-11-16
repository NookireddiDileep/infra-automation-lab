package main

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const header = "# patchcheck snapshot v1"

// Snapshot is the parsed form of a v1 snapshot file.
type Snapshot struct {
	Host   string
	Kernel string
	Items  map[string]bool // "mount / xfs", "port tcp/22", "service sshd.service"
	FsUse  map[string]int  // mountpoint -> used %
}

// Finding is one line of the report.
type Finding struct{ Severity, Message string }

// Report is the result of comparing a pre and a post snapshot.
type Report struct {
	Host       string
	Findings   []Finding
	Crit, Warn int
}

// Result returns OK, WARN or CRIT.
func (r Report) Result() string {
	switch {
	case r.Crit > 0:
		return "CRIT"
	case r.Warn > 0:
		return "WARN"
	}
	return "OK"
}

// ExitCode maps the result to the shared contract: 0 OK, 1 WARN, 2 CRIT.
func (r Report) ExitCode() int {
	return map[string]int{"OK": 0, "WARN": 1, "CRIT": 2}[r.Result()]
}

// Text renders the report exactly like the Bash/Python/Ansible versions.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "patchcheck report: %s\n", r.Host)
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%s %s\n", f.Severity, f.Message)
	}
	fmt.Fprintf(&b, "RESULT %s crit=%d warn=%d\n", r.Result(), r.Crit, r.Warn)
	return b.String()
}

// ParseSnapshot reads a v1 snapshot.
func ParseSnapshot(rd io.Reader, name string) (*Snapshot, error) {
	s := &Snapshot{Host: "unknown", Items: map[string]bool{}, FsUse: map[string]int{}}
	sc := bufio.NewScanner(rd)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			if strings.TrimSpace(line) != header {
				return nil, fmt.Errorf("%s is not a v1 snapshot", name)
			}
			first = false
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "#":
			for _, tok := range f[1:] {
				if v, ok := strings.CutPrefix(tok, "host="); ok {
					s.Host = v
				}
			}
		case "kernel":
			if len(f) > 1 && s.Kernel == "" {
				s.Kernel = f[1]
			}
		case "mount", "port", "service":
			s.Items[strings.TrimSpace(line)] = true
		case "fsuse":
			if len(f) == 3 {
				pct, err := strconv.Atoi(f[2])
				if err != nil {
					return nil, fmt.Errorf("%s: bad fsuse line %q", name, line)
				}
				s.FsUse[f[1]] = pct
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if first {
		return nil, fmt.Errorf("%s is empty", name)
	}
	return s, nil
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out) // byte order, same as LC_ALL=C sort and Python sorted()
	return out
}

// Compare produces the drift report.
func Compare(pre, post *Snapshot, warnPct int) Report {
	r := Report{Host: post.Host}
	if pre.Kernel != "" && post.Kernel != "" {
		msg := fmt.Sprintf("kernel %s -> %s", pre.Kernel, post.Kernel)
		if pre.Kernel == post.Kernel {
			msg = "kernel unchanged " + post.Kernel
		}
		r.Findings = append(r.Findings, Finding{"INFO", msg})
	}
	missing, added := diff(pre.Items, post.Items), diff(post.Items, pre.Items)
	for _, m := range missing {
		r.Findings = append(r.Findings, Finding{"CRIT", "missing " + m})
	}
	for _, n := range added {
		r.Findings = append(r.Findings, Finding{"WARN", "new " + n})
	}
	var hot []string
	for mp, pct := range post.FsUse {
		if pct >= warnPct {
			hot = append(hot, fmt.Sprintf("%s %d", mp, pct))
		}
	}
	sort.Strings(hot)
	for _, h := range hot {
		i := strings.LastIndex(h, " ")
		r.Findings = append(r.Findings, Finding{"WARN", fmt.Sprintf("fs %s at %s%% (>= %d%%)", h[:i], h[i+1:], warnPct)})
	}
	r.Crit, r.Warn = len(missing), len(added)+len(hot)
	return r
}
