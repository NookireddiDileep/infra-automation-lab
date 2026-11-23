// rightsize: ResourceQuota right-sizing report (quota hard/used vs real PodMetrics usage).
//
//	rightsize QUOTA_JSON METRICS_JSON
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
	return strings.TrimSuffix(base, ".quota")
}

func run(args []string) int {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("rightsize [ERROR] ")
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: rightsize QUOTA_JSON METRICS_JSON")
		return 3
	}
	quota, err := os.ReadFile(args[0])
	if err != nil {
		log.Printf("%s: %v", args[0], err)
		return 3
	}
	metrics, err := os.ReadFile(args[1])
	if err != nil {
		log.Printf("%s: %v", args[1], err)
		return 3
	}
	text, code, err := check(quota, metrics, reportName(args[0]))
	if err != nil {
		log.Printf("%v", err)
		return 3
	}
	fmt.Print(text)
	return code
}

func main() { os.Exit(run(os.Args[1:])) }
