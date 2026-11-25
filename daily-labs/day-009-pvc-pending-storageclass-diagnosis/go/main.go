package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr *os.File) int {
	asJSON := false
	if len(args) > 0 && args[0] == "--json" {
		asJSON, args = true, args[1:]
	}
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: pvcbind [--json] PVCS_JSON STORAGECLASSES_JSON")
		return 3
	}
	pvcs, err := loadList(args[0], "pvcs")
	var scs []obj
	if err == nil {
		scs, err = loadList(args[1], "storageclasses")
	}
	var res *Result
	if err == nil {
		res, err = Check(pvcs, scs)
	}
	if err != nil {
		fmt.Fprintln(stderr, "pvcbind [ERROR]", err)
		return 3
	}
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(args[0]), ".json"), ".pvcs")
	report, verdict := Render(name, res)
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"name": name, "result": verdict, "rows": res.Rows, "pvcs": res.PVCs}, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		fmt.Fprint(stdout, report)
	}
	return map[string]int{"OK": 0, "WARN": 1, "CRIT": 2}[verdict]
}
