package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"text/tabwriter"
	"time"
)

func auditFile(path, now string, warn, crit int64) (Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Report{}, inputErr("cannot read %s", path)
	}
	host, certs, err := ParseInventory(string(b))
	if err != nil {
		return Report{}, err
	}
	return Evaluate(host, certs, now, warn, crit)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "certscan:", err)
	return 3
}

func run(args []string) int {
	if len(args) < 1 || (args[0] != "check" && args[0] != "fleet") {
		fmt.Fprintln(os.Stderr, "usage: certscan check|fleet [--now ISO] [--warn-days N] [--crit-days N] [-j N] PATH")
		return 3
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	now := fs.String("now", time.Now().UTC().Format(isoLayout), "ISO-8601 UTC")
	warn := fs.Int64("warn-days", 30, "WARN threshold")
	crit := fs.Int64("crit-days", 14, "CRIT threshold")
	jobs := fs.Int("j", 4, "fleet: parallel workers")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
		return 3
	}
	if args[0] == "check" {
		rep, err := auditFile(fs.Arg(0), *now, *warn, *crit)
		if err != nil {
			return fail(err)
		}
		fmt.Print(rep.Render())
		return rep.ExitCode()
	}
	return fleet(fs.Arg(0), *now, *warn, *crit, *jobs)
}

// fleet audits every *.certs file in a directory with a worker pool and prints a summary table.
func fleet(dir, now string, warn, crit int64, jobs int) int {
	files, _ := filepath.Glob(filepath.Join(dir, "*.certs"))
	if len(files) == 0 {
		return fail(inputErr("no *.certs files in %s", dir))
	}
	type result struct {
		rep Report
		err error
	}
	results := make([]result, len(files))
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < max(jobs, 1); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				results[i].rep, results[i].err = auditFile(files[i], now, warn, crit)
			}
		}()
	}
	for i := range files {
		work <- i
	}
	close(work)
	wg.Wait()

	sort.Slice(results, func(a, b int) bool { return results[a].rep.Host < results[b].rep.Host })
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tRESULT\tCRIT\tWARN\tOK")
	worst := 0
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintln(os.Stderr, "certscan:", r.err)
			return 3
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\n", r.rep.Host, r.rep.Result(), r.rep.Crit, r.rep.Warn, r.rep.OK)
		worst = max(worst, r.rep.ExitCode())
	}
	tw.Flush()
	return worst
}

func main() { os.Exit(run(os.Args[1:])) }
