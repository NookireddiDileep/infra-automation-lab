// Package main: certscan, a TLS certificate expiry audit (inventory v1 -> report).
package main

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	isoRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]Z$`)
	srcRe = regexp.MustCompile(`^(file|endpoint):.`)
)

const (
	isoLayout = "2006-01-02T15:04:05Z"
	day       = 86400
)

// ErrInput marks bad inventories or arguments (exit code 3).
var ErrInput = errors.New("input error")

func inputErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInput, fmt.Sprintf(format, a...))
}

// Cert is one inventory row. NotAfter stays ISO text: it sorts and compares correctly.
type Cert struct{ Source, NotAfter, Subject string }

// Report is the result of auditing one host.
type Report struct {
	Host           string
	Lines          []string
	Crit, Warn, OK int
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
	fmt.Fprintf(&b, "certscan report: %s\n", r.Host)
	for _, l := range r.Lines {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "RESULT %s crit=%d warn=%d ok=%d\n", r.Result(), r.Crit, r.Warn, r.OK)
	return b.String()
}

func toEpoch(ts string) (int64, error) {
	if !isoRe.MatchString(ts) {
		return 0, inputErr("invalid timestamp: %s", ts)
	}
	t, err := time.Parse(isoLayout, ts)
	if err != nil {
		return 0, inputErr("invalid timestamp: %s", ts)
	}
	return t.Unix(), nil
}

// ParseInventory reads inventory v1 text.
func ParseInventory(text string) (string, []Cert, error) {
	host := ""
	var certs []Cert
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "# host=") {
			host = strings.TrimPrefix(line, "# host=")
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 || !srcRe.MatchString(f[0]) || f[2] == "" || !isoRe.MatchString(f[1]) {
			return "", nil, inputErr("invalid line %d", i+1)
		}
		if _, err := toEpoch(f[1]); err != nil {
			return "", nil, err
		}
		certs = append(certs, Cert{f[0], f[1], f[2]})
	}
	if host == "" {
		return "", nil, inputErr("missing '# host=' header")
	}
	if len(certs) == 0 {
		return "", nil, inputErr("no certificates")
	}
	return host, certs, nil
}

// floorDiv divides rounding toward negative infinity (Go's / truncates toward zero).
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Evaluate applies the contract rules to one host's certificates.
func Evaluate(host string, certs []Cert, now string, warnDays, critDays int64) (Report, error) {
	if critDays < 0 || critDays >= warnDays {
		return Report{}, inputErr("--crit-days must be lower than --warn-days")
	}
	nowE, err := toEpoch(now)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Host: host}
	for _, c := range certs {
		na, err := toEpoch(c.NotAfter)
		if err != nil {
			return Report{}, err
		}
		d := floorDiv(na-nowE, day)
		var sev, msg string
		switch {
		case d < 0:
			sev, msg = "CRIT", fmt.Sprintf("expired %dd ago", -d)
		case d <= critDays:
			sev, msg = "CRIT", fmt.Sprintf("expires in %dd", d)
		case d <= warnDays:
			sev, msg = "WARN", fmt.Sprintf("expires in %dd", d)
		default:
			rep.OK++
			continue
		}
		rep.Lines = append(rep.Lines, fmt.Sprintf("%s %s %s (%s) %s", sev, c.Source, msg, c.NotAfter[:10], c.Subject))
		if sev == "CRIT" {
			rep.Crit++
		} else {
			rep.Warn++
		}
	}
	// renewed on disk but the running service still serves the old certificate
	for _, e := range certs {
		if !strings.HasPrefix(e.Source, "endpoint:") {
			continue
		}
		for _, f := range certs {
			if strings.HasPrefix(f.Source, "file:") && f.Subject == e.Subject && e.NotAfter < f.NotAfter {
				rep.Lines = append(rep.Lines, fmt.Sprintf("WARN reload-needed %s endpoint serves %s but file has %s",
					e.Subject, e.NotAfter[:10], f.NotAfter[:10]))
				rep.Warn++
			}
		}
	}
	sort.Strings(rep.Lines) // byte order, like LC_ALL=C sort
	return rep, nil
}
