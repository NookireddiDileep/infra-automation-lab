package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func load(path, what string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, errNoRead)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON (%v)", what, err)
	}
	return v, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	if len(args) > 0 && args[0] == "--json" {
		asJSON, args = true, args[1:]
	}
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: upgradegate [--json] CLUSTEROPERATORS_JSON MCPS_JSON")
		return 3
	}
	// the two files are independent: read and parse them concurrently
	var docs [2]any
	var errs [2]error
	var wg sync.WaitGroup
	for i, what := range []string{"clusteroperators", "mcps"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			docs[i], errs[i] = load(args[i], what)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			fmt.Fprintln(stderr, "upgradegate [ERROR]", err)
			return 3
		}
	}
	res, err := Check(docs[0], docs[1])
	if err != nil {
		fmt.Fprintln(stderr, "upgradegate [ERROR]", err)
		return 3
	}
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(args[0]), ".json"), ".clusteroperators")
	report, verdict := Render(name, res)
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"name": name, "result": verdict, "rows": res.Rows, "operators": res.Operators, "pools": res.Pools}, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		fmt.Fprint(stdout, report)
	}
	return map[string]int{"OK": 0, "WARN": 1, "CRIT": 2}[verdict]
}
