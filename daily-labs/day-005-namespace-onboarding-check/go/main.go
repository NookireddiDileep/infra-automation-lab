// onboardcheck: does every namespace in the onboarding spec have its quota, limits, RBAC and NetworkPolicy?
//
//	onboardcheck SPEC_JSON CLUSTER_JSON
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
	for _, suf := range []string{".cluster.json", ".json"} {
		if strings.HasSuffix(base, suf) {
			return strings.TrimSuffix(base, suf)
		}
	}
	return base
}

func run(args []string) int {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("onboardcheck [ERROR] ")
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: onboardcheck SPEC_JSON CLUSTER_JSON")
		return 3
	}
	spec, err := os.ReadFile(args[0])
	if err != nil {
		log.Printf("%s: %v", args[0], err)
		return 3
	}
	cluster, err := os.ReadFile(args[1])
	if err != nil {
		log.Printf("%s: %v", args[1], err)
		return 3
	}
	text, code, err := check(spec, cluster, reportName(args[1]))
	if err != nil {
		log.Printf("%v", err)
		return 3
	}
	fmt.Print(text)
	return code
}

func main() { os.Exit(run(os.Args[1:])) }
