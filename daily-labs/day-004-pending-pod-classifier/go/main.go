// pendingtriage: root-cause Pending pods from saved `oc get pods -A -o json` output.
//
// Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/runtime error. Contract: ../README.md.
package main

import (
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
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("pendingtriage [ERROR] ")
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: pendingtriage PODS_JSON")
		return 3
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("%s: %v", path, err)
		return 3
	}
	text, code, err := triage(data, reportName(path))
	if err != nil {
		log.Printf("%s: %v", path, err)
		return 3
	}
	fmt.Print(text)
	return code
}

func main() { os.Exit(run(os.Args[1:])) }
