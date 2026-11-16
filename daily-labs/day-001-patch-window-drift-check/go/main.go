// patchcheck (Go edition): patch-window drift check.
//
//	patchcheck snapshot [-o FILE]
//	patchcheck compare [-t PCT] PRE POST
//	patchcheck fleet   [-t PCT] [-j WORKERS] DIR     # compares every <host>-pre.txt / <host>-post.txt in DIR concurrently
//
// Exit codes: 0 OK · 1 WARN · 2 CRIT (worst host, for fleet) · 3 error.
package main

import (
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
	fmt.Fprintf(os.Stderr, "%s patchcheck [%s] %s\n", time.Now().UTC().Format(time.RFC3339), level, fmt.Sprintf(format, args...))
}

func usage() int {
	fmt.Fprintln(os.Stderr, "usage: patchcheck snapshot [-o FILE] | compare [-t PCT] PRE POST | fleet [-t PCT] [-j N] DIR")
	return 3
}

func loadSnapshot(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseSnapshot(f, path)
}

func comparePair(pre, post string, pct int) (Report, error) {
	a, err := loadSnapshot(pre)
	if err != nil {
		return Report{}, err
	}
	b, err := loadSnapshot(post)
	if err != nil {
		return Report{}, err
	}
	return Compare(a, b, pct), nil
}

type fleetResult struct {
	host   string
	report Report
	err    error
}

// runFleet fans out comparisons to a bounded worker pool. This is the part Go is good at.
func runFleet(dir string, pct, workers int) ([]fleetResult, error) {
	pres, err := filepath.Glob(filepath.Join(dir, "*-pre.txt"))
	if err != nil {
		return nil, err
	}
	jobs := make(chan string)
	results := make(chan fleetResult)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pre := range jobs {
				host := strings.TrimSuffix(filepath.Base(pre), "-pre.txt")
				post := filepath.Join(dir, host+"-post.txt")
				rep, err := comparePair(pre, post, pct)
				results <- fleetResult{host, rep, err}
			}
		}()
	}
	go func() {
		for _, p := range pres {
			jobs <- p
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(results) }()

	var out []fleetResult
	for r := range results {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].host < out[j].host })
	return out, nil
}

func run(args []string) int {
	if len(args) < 1 {
		return usage()
	}
	switch args[0] {
	case "snapshot":
		fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
		out := fs.String("o", "", "output file")
		if err := fs.Parse(args[1:]); err != nil {
			return 3
		}
		text, err := TakeSnapshot()
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

	case "compare":
		fs := flag.NewFlagSet("compare", flag.ContinueOnError)
		pct := fs.Int("t", 90, "fs usage WARN threshold (%)")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 2 {
			return usage()
		}
		rep, err := comparePair(fs.Arg(0), fs.Arg(1), *pct)
		if err != nil {
			logf("ERROR", "%v", err)
			return 3
		}
		fmt.Print(rep.Text())
		return rep.ExitCode()

	case "fleet":
		fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
		pct := fs.Int("t", 90, "fs usage WARN threshold (%)")
		workers := fs.Int("j", 8, "parallel workers")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 || *workers < 1 {
			return usage()
		}
		results, err := runFleet(fs.Arg(0), *pct, *workers)
		if err != nil {
			logf("ERROR", "%v", err)
			return 3
		}
		worst := 0
		fmt.Printf("%-12s %-6s %4s %4s\n", "HOST", "RESULT", "CRIT", "WARN")
		for _, r := range results {
			if r.err != nil {
				fmt.Printf("%-12s %-6s %4s %4s  (%v)\n", r.host, "ERROR", "-", "-", r.err)
				worst = 3
				continue
			}
			fmt.Printf("%-12s %-6s %4d %4d\n", r.host, r.report.Result(), r.report.Crit, r.report.Warn)
			if c := r.report.ExitCode(); c > worst && worst != 3 {
				worst = c
			}
		}
		return worst
	}
	return usage()
}

func main() { os.Exit(run(os.Args[1:])) }
