// Package main: timesync, a chrony clock-drift audit (inventory v1 -> report).
package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxStratum = 5
	minSources = 3
	nsPerMs    = 1_000_000
)

var (
	reLeapKey    = regexp.MustCompile(`^Leap status *:`)
	reStratumKey = regexp.MustCompile(`^Stratum *:`)
	reSystimeKey = regexp.MustCompile(`^System time *:`)
	reLeap       = regexp.MustCompile(`^Leap status *: *(.+)$`)
	reStratum    = regexp.MustCompile(`^Stratum *: *([0-9]+)$`)
	reSystime    = regexp.MustCompile(`^System time *: *([0-9]+)\.([0-9]{9}) seconds (fast|slow) of NTP time$`)
	reSource     = regexp.MustCompile(`^[=^]([*+?x~-]) +([^ ]+) +([0-9]+) +([0-9]+) +([0-7]+) +[^ ]+( .*)?$`)
	reEquals     = regexp.MustCompile(`^=+$`)
	reZeros      = regexp.MustCompile(`^0+$`)
)

// ErrInput marks bad inventories or arguments (exit code 3).
var ErrInput = errors.New("input error")

func inputErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInput, fmt.Sprintf(format, a...))
}

// Source is one `chronyc sources` row; Reach is the octal text chronyc prints.
type Source struct{ State, Name, Reach string }

// Inventory is the parsed form of inventory v1. OffsetNs is absolute integer nanoseconds (no floats).
type Inventory struct {
	Host, Leap, Direction string
	Stratum               int
	OffsetNs              int64
	Sources               []Source
}

// Report is the result of auditing one host.
type Report struct {
	Host       string
	Lines      []string
	Crit, Warn int
	Sources    int
}

func (r Report) Result() string {
	switch {
	case r.Crit > 0:
		return "CRIT"
	case r.Warn > 0:
		return "WARN"
	}
	return "OK"
}

func (r Report) ExitCode() int { return map[string]int{"OK": 0, "WARN": 1, "CRIT": 2}[r.Result()] }

func (r Report) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "timesync report: %s\n", r.Host)
	for _, l := range r.Lines {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "RESULT %s crit=%d warn=%d sources=%d\n", r.Result(), r.Crit, r.Warn, r.Sources)
	return b.String()
}

// ParseInventory reads inventory v1 text.
func ParseInventory(text string) (Inventory, error) {
	var inv Inventory
	var haveStratum, haveOffset bool
	section := ""
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		switch {
		case strings.HasPrefix(line, "# host="):
			inv.Host = strings.TrimPrefix(line, "# host=")
			continue
		case line == "# chronyc tracking":
			section = "tracking"
			continue
		case line == "# chronyc sources":
			section = "sources"
			continue
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		}
		switch section {
		case "tracking":
			switch {
			case reLeapKey.MatchString(line):
				m := reLeap.FindStringSubmatch(line)
				if m == nil {
					return inv, inputErr("invalid line %d", n)
				}
				inv.Leap = m[1]
			case reStratumKey.MatchString(line):
				m := reStratum.FindStringSubmatch(line)
				if m == nil {
					return inv, inputErr("invalid line %d", n)
				}
				inv.Stratum, _ = strconv.Atoi(m[1])
				haveStratum = true
			case reSystimeKey.MatchString(line):
				m := reSystime.FindStringSubmatch(line)
				if m == nil {
					return inv, inputErr("invalid line %d", n)
				}
				sec, _ := strconv.ParseInt(m[1], 10, 64)
				frac, _ := strconv.ParseInt(m[2], 10, 64)
				inv.OffsetNs = sec*1_000_000_000 + frac
				inv.Direction = m[3]
				haveOffset = true
			}
		case "sources":
			if strings.HasPrefix(line, "MS ") || reEquals.MatchString(line) {
				continue
			}
			m := reSource.FindStringSubmatch(line)
			if m == nil {
				return inv, inputErr("invalid line %d", n)
			}
			inv.Sources = append(inv.Sources, Source{m[1], m[2], m[5]})
		default:
			return inv, inputErr("invalid line %d", n)
		}
	}
	if inv.Host == "" {
		return inv, inputErr("missing '# host=' header")
	}
	if inv.Leap == "" || !haveStratum || !haveOffset {
		return inv, inputErr("missing tracking fields")
	}
	if len(inv.Sources) == 0 {
		return inv, inputErr("no sources")
	}
	return inv, nil
}

// fmtMs truncates to microseconds (never rounds), so every language prints the same digits.
func fmtMs(ns int64) string {
	us := ns / 1000
	return fmt.Sprintf("%d.%03dms", us/1000, us%1000)
}

// Evaluate applies the contract rules to one host.
func Evaluate(inv Inventory, warnMs, critMs int64) (Report, error) {
	if warnMs < 0 || critMs <= warnMs {
		return Report{}, inputErr("--crit-ms must be higher than --warn-ms")
	}
	var lines []string
	selected := 0
	for _, s := range inv.Sources {
		if s.State == "*" {
			selected++
		}
		if s.State == "x" {
			lines = append(lines, fmt.Sprintf("WARN source %s falseticker", s.Name))
		}
		if reZeros.MatchString(s.Reach) {
			lines = append(lines, fmt.Sprintf("WARN source %s unreachable (reach 0)", s.Name))
		}
	}
	if inv.Leap == "Not synchronised" {
		// chrony still prints a stale offset and stratum 0 here; neither means anything
		lines = append(lines, fmt.Sprintf("CRIT unsynchronised (leap status: %s)", inv.Leap))
	} else {
		switch {
		case inv.OffsetNs > critMs*nsPerMs:
			lines = append(lines, fmt.Sprintf("CRIT offset %s %s (crit %dms)", fmtMs(inv.OffsetNs), inv.Direction, critMs))
		case inv.OffsetNs > warnMs*nsPerMs:
			lines = append(lines, fmt.Sprintf("WARN offset %s %s (warn %dms)", fmtMs(inv.OffsetNs), inv.Direction, warnMs))
		}
		if inv.Stratum > maxStratum {
			lines = append(lines, fmt.Sprintf("WARN stratum %d (max %d)", inv.Stratum, maxStratum))
		}
	}
	if selected == 0 {
		lines = append(lines, "WARN no selected source")
	}
	if len(inv.Sources) < minSources {
		lines = append(lines, fmt.Sprintf("WARN sources %d (need at least %d to out-vote a falseticker)", len(inv.Sources), minSources))
	}
	rep := Report{Host: inv.Host, Sources: len(inv.Sources)}
	for _, l := range lines {
		if strings.HasPrefix(l, "CRIT") {
			rep.Crit++
		} else {
			rep.Warn++
		}
	}
	sort.Strings(lines) // byte order, like LC_ALL=C sort
	rep.Lines = lines
	return rep, nil
}
