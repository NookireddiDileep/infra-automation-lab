// nodepressure: node pressure & eviction report (Node conditions + kubelet Evicted events).
//
//	nodepressure NODES_JSON EVENTS_JSON
//
// Exit codes: 0 OK, 1 WARN, 2 CRIT, 3 usage/input error. Contract: ../README.md.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func reportName(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	return strings.TrimSuffix(base, ".nodes")
}

func run(args []string) int {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("nodepressure [ERROR] ")
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: nodepressure NODES_JSON EVENTS_JSON")
		return 3
	}
	nodes, err := os.ReadFile(args[0])
	if err != nil {
		log.Printf("%s: %v", args[0], err)
		return 3
	}
	events, err := os.ReadFile(args[1])
	if err != nil {
		log.Printf("%s: %v", args[1], err)
		return 3
	}
	text, code, err := check(nodes, events, reportName(args[0]))
	if err != nil {
		log.Printf("%v", err)
		return 3
	}
	fmt.Print(text)
	return code
}

func main() { os.Exit(run(os.Args[1:])) }
