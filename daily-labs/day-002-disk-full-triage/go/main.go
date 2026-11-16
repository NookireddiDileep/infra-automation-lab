// disktriage (Go edition): disk-full triage.
//
//	disktriage snapshot [-o FILE] [-m DU_MIN_PCT] [-l LOGDIR]
//	disktriage report   [-w PCT] [-c PCT] [-r DAYS] [-json] SNAPSHOT
//	disktriage fleet    [-w PCT] [-c PCT] [-r DAYS] [-j WORKERS] DIR   # every *.snap in DIR, concurrently
//
// Exit codes: 0 OK · 1 WARN · 2 CRIT (worst host, for fleet) · 3 error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func logf(level, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s disktriage [%s] %s\n", time.Now().UTC().Format(time.RFC3339), level, fmt.Sprintf(format, args...))
}

func usage() int {
	fmt.Fprintln(os.Stderr, "usage: disktriage snapshot [-o FILE] [-m PCT] [-l DIR] | report [-w PCT] [-c PCT] [-r DAYS] [-json] SNAPSHOT | fleet [-j N] DIR")
	return 3
}

func loadReport(path string, t Thresholds) (Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return Report{}, err
	}
	defer f.Close()
	s, err := ParseSnapshot(f, path)
	if err != nil {
		return Report{}, err
	}
	return Analyse(s, t), nil
}

type fleetResult struct {
	file   string
	report Report
	err    error
}

// runFleet analyses every snapshot in dir with a bounded worker pool.
func runFleet(dir string, t Thresholds, workers int) ([]fleetResult, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.snap"))
	if err != nil {
		return nil, err
	}
	jobs := make(chan string)
	results := make(chan fleetResult)
	var wg sync.WaitGroup
	for i := 0; i < max(workers, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				rep, err := loadReport(f, t)
				results <- fleetResult{f, rep, err}
			}
		}()
	}
	go func() {
		for _, f := range files {
			jobs <- f
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(results) }()
	var out []fleetResult
	for r := range results {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file < out[j].file })
	return out, nil
}

func thresholdFlags(fs *flag.FlagSet) *Thresholds {
	t := &Thresholds{}
	fs.Int64Var(&t.Warn, "w", 85, "warn at this % used")
	fs.Int64Var(&t.Crit, "c", 95, "crit at this % used")
	fs.Int64Var(&t.RetainDays, "r", 7, "rotated logs at least this many days old are cleanup candidates")
	return t
}

func run(args []string) int {
	if len(args) < 1 {
		return usage()
	}
	switch args[0] {
	case "snapshot":
		fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
		out := fs.String("o", "", "output file")
		duMin := fs.Int64("m", 80, "run du only on filesystems >= this %")
		logdir := fs.String("l", "/var/log", "where to look for rotated logs")
		if fs.Parse(args[1:]) != nil {
			return 3
		}
		text, err := TakeSnapshot(*duMin, *logdir)
		if err != nil {
			logf("ERROR", "%v", err)
			return 3
		}
		if *out == "" {
			fmt.Print(text)
			return 0
		}
		if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
			logf("ERROR", "%v", err)
			return 3
		}
		logf("INFO", "snapshot written to %s", *out)
		return 0

	case "report":
		fs := flag.NewFlagSet("report", flag.ContinueOnError)
		t := thresholdFlags(fs)
		asJSON := fs.Bool("json", false, "JSON output")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 1 {
			return usage()
		}
		rep, err := loadReport(fs.Arg(0), *t)
		if err != nil {
			logf("ERROR", "%v", err)
			return 3
		}
		if *asJSON {
			b, _ := json.MarshalIndent(map[string]any{
				"host": rep.Host, "result": rep.Result(), "crit": rep.count("CRIT"), "warn": rep.count("WARN"),
				"findings": append([]Finding{}, rep.Findings...),
			}, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Print(rep.Text())
		}
		return rep.ExitCode()

	case "fleet":
		fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
		t := thresholdFlags(fs)
		workers := fs.Int("j", 8, "parallel workers")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 1 {
			return usage()
		}
		res, err := runFleet(fs.Arg(0), *t, *workers)
		if err != nil || len(res) == 0 {
			logf("ERROR", "no snapshots in %s: %v", fs.Arg(0), err)
			return 3
		}
		worst := 0
		fmt.Printf("%-12s %-6s %4s %4s  %s\n", "HOST", "RESULT", "CRIT", "WARN", "WORST")
		for _, r := range res {
			if r.err != nil {
				logf("ERROR", "%v", r.err)
				fmt.Printf("%-12s %-6s\n", strings.TrimSuffix(filepath.Base(r.file), ".snap"), "ERROR")
				worst = 3
				continue
			}
			first := "-"
			if len(r.report.Findings) > 0 {
				first = r.report.Findings[0].Message
			}
			fmt.Printf("%-12s %-6s %4d %4d  %s\n", r.report.Host, r.report.Result(), r.report.count("CRIT"), r.report.count("WARN"), first)
			if c := r.report.ExitCode(); c > worst && worst != 3 {
				worst = c
			}
		}
		return worst

	case "-h", "--help", "help":
		return usage()
	}
	logf("ERROR", "unknown subcommand: %s", args[0])
	return 3
}

func main() { os.Exit(run(os.Args[1:])) }
