// podtriage: classify unhealthy pods from saved `oc get pods -A -o json` output.
//
// Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error. Contract: ../README.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func reportName(path string) string {
	base := filepath.Base(path)
	for _, suf := range []string{".pods.json", ".json"} {
		if strings.HasSuffix(base, suf) {
			return strings.TrimSuffix(base, suf)
		}
	}
	return base
}

func run(args []string) int {
	fs := flag.NewFlagSet("podtriage", flag.ContinueOnError)
	restartWarn := fs.Int("r", 5, "WARN at this restartCount")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: podtriage [-r N] PODS_JSON") }
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("podtriage [ERROR] ")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fs.Usage()
		return 3
	}
	if *restartWarn < 1 {
		log.Print("-r must be >= 1")
		return 3
	}
	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return 3
	}
	text, code, err := triage(data, reportName(path), *restartWarn)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return 3
	}
	fmt.Print(text)
	return code
}

func main() { os.Exit(run(os.Args[1:])) }
