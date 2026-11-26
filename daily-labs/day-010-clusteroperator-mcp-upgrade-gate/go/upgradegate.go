// Package main: pre-upgrade gate for OpenShift from saved `oc get co` and `oc get mcp` JSON.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

type obj = map[string]any

const (
	rankCrit = 0
	rankWarn = 1
)

var levels = [2]string{"CRIT", "WARN"}

var hintOrder = []string{"UNAVAILABLE", "NOSTATUS", "DEGRADED", "BLOCKED", "STALLED", "PROGRESSING", "UPDATING", "PAUSED"}

var hints = map[string]string{
	"UNAVAILABLE": "operator Available is not True, or pool nodes are down outside a rollout: oc describe co/mcp, oc get nodes; do not upgrade until it is resolved",
	"NOSTATUS":    "the operator reports no Available condition, so it is not reporting at all: check its pod and events in its own namespace",
	"DEGRADED":    "Degraded=True: oc describe co/<name> for an operator, oc describe mcp plus machine-config-daemon logs for a pool; fix it before the upgrade",
	"BLOCKED":     "Upgradeable=False blocks minor-version updates (patch updates still run): read the condition message, fix it or give the admin acknowledgement it asks for",
	"STALLED":     "updated is below machineCount, the pool is not updating and not paused: it is stuck, check nodes for a stale desiredConfig and the machine-config-controller logs",
	"PROGRESSING": "an operator is mid-rollout: wait for Progressing=False, or the upgrade stacks a second rollout on top",
	"UPDATING":    "the pool is rolling out a rendered config: let it finish, a cluster upgrade now would queue a second change on the same nodes",
	"PAUSED":      "spec.paused=true: the pool ignores new rendered configs and stays on the old version; unpause before the upgrade unless it is a deliberate canary",
}

// Row is one finding. JSON tags keep --json output stable.
type Row struct {
	Rank    int    `json:"rank"`
	Subject string `json:"subject"`
	Cls     string `json:"cls"`
	Detail  string `json:"detail"`
}

func (r Row) Text() string {
	return fmt.Sprintf("%s %s %s %s", levels[r.Rank], r.Subject, r.Cls, r.Detail)
}

// Result is what Check returns.
type Result struct {
	Rows      []Row `json:"rows"`
	Operators int   `json:"operators"`
	Pools     int   `json:"pools"`
}

func asObj(v any) obj {
	if m, ok := v.(obj); ok {
		return m
	}
	return obj{}
}

func txt(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "-"
}

func listOf(doc any, what, kind string) ([]obj, error) {
	m, ok := doc.(obj)
	items, ok2 := m["items"].([]any)
	if !ok || m["kind"] != "List" || !ok2 {
		return nil, fmt.Errorf("%s: not a List (expected kind List with items[])", what)
	}
	var out []obj
	for _, it := range items {
		if o, ok := it.(obj); ok && o["kind"] == kind {
			out = append(out, o)
		}
	}
	return out, nil
}

func nameOf(it obj, what string) (string, error) {
	n, ok := asObj(it["metadata"])["name"].(string)
	if !ok || n == "" {
		return "", fmt.Errorf("%s: item without metadata.name", what)
	}
	return n, nil
}

type cond struct{ status, reason string }

func conditions(it obj, what, name string) (map[string]cond, error) {
	raw, present := asObj(it["status"])["conditions"]
	out := map[string]cond{}
	if !present || raw == nil {
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: bad conditions in %s", what, name)
	}
	for _, e := range list {
		c, isObj := e.(obj)
		t, isStr := c["type"].(string)
		s, _ := c["status"].(string)
		if !isObj || !isStr || (s != "True" && s != "False" && s != "Unknown") {
			return nil, fmt.Errorf("%s: bad condition status in %s", what, name)
		}
		out[t] = cond{s, txt(c["reason"])} // last one wins
	}
	return out, nil
}

func count(it obj, field, name string) (int, error) {
	v, ok := asObj(it["status"])[field].(float64)
	if !ok || v != math.Floor(v) || v < 0 {
		return 0, fmt.Errorf("mcps: bad %s in %s", field, name)
	}
	return int(v), nil
}

func judgeCO(it obj) (*Row, error) {
	n, err := nameOf(it, "clusteroperators")
	if err != nil {
		return nil, err
	}
	c, err := conditions(it, "clusteroperators", n)
	if err != nil {
		return nil, err
	}
	s := "co/" + n
	row := func(rank int, cls, reason string) (*Row, error) {
		return &Row{rank, s, cls, "reason=" + reason}, nil
	}
	switch {
	case c["Available"].status == "":
		return row(rankCrit, "NOSTATUS", "-")
	case c["Available"].status != "True":
		return row(rankCrit, "UNAVAILABLE", c["Available"].reason)
	case c["Degraded"].status == "True":
		return row(rankCrit, "DEGRADED", c["Degraded"].reason)
	case c["Upgradeable"].status == "False":
		return row(rankCrit, "BLOCKED", c["Upgradeable"].reason)
	case c["Progressing"].status == "True":
		return row(rankWarn, "PROGRESSING", c["Progressing"].reason)
	}
	return nil, nil
}

func judgeMCP(it obj) (*Row, error) {
	n, err := nameOf(it, "mcps")
	if err != nil {
		return nil, err
	}
	c, err := conditions(it, "mcps", n)
	if err != nil {
		return nil, err
	}
	var v [5]int
	for i, f := range []string{"machineCount", "updatedMachineCount", "readyMachineCount", "unavailableMachineCount", "degradedMachineCount"} {
		if v[i], err = count(it, f, n); err != nil {
			return nil, err
		}
	}
	total, upd, un, dg := v[0], v[1], v[3], v[4]
	s := "mcp/" + n
	d := fmt.Sprintf("degraded=%d unavailable=%d updated=%d/%d", dg, un, upd, total)
	row := func(rank int, cls string) (*Row, error) { return &Row{rank, s, cls, d}, nil }
	switch {
	case dg > 0 || c["Degraded"].status == "True":
		return row(rankCrit, "DEGRADED")
	case c["Updating"].status == "True":
		return row(rankWarn, "UPDATING")
	case un > 0:
		return row(rankCrit, "UNAVAILABLE")
	case asObj(it["spec"])["paused"] == true:
		return row(rankWarn, "PAUSED")
	case upd < total:
		return row(rankCrit, "STALLED")
	}
	return nil, nil
}

// Check judges both lists. Any bad input aborts: no half reports.
func Check(cosDoc, mcpsDoc any) (*Result, error) {
	cos, err := listOf(cosDoc, "clusteroperators", "ClusterOperator")
	if err != nil {
		return nil, err
	}
	pools, err := listOf(mcpsDoc, "mcps", "MachineConfigPool")
	if err != nil {
		return nil, err
	}
	res := &Result{Rows: []Row{}, Operators: len(cos), Pools: len(pools)}
	for _, it := range cos {
		r, err := judgeCO(it)
		if err != nil {
			return nil, err
		}
		if r != nil {
			res.Rows = append(res.Rows, *r)
		}
	}
	for _, it := range pools {
		r, err := judgeMCP(it)
		if err != nil {
			return nil, err
		}
		if r != nil {
			res.Rows = append(res.Rows, *r)
		}
	}
	sort.SliceStable(res.Rows, func(i, j int) bool { // bytewise, like LC_ALL=C
		a, b := res.Rows[i], res.Rows[j]
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		return a.Subject < b.Subject
	})
	return res, nil
}

// Render returns the report text and the OK|WARN|CRIT verdict.
func Render(name string, res *Result) (string, string) {
	crit, warn := 0, 0
	present := map[string]bool{}
	for _, r := range res.Rows {
		if r.Rank == rankCrit {
			crit++
		} else {
			warn++
		}
		present[r.Cls] = true
	}
	verdict, word := "OK", "GO"
	if crit > 0 {
		verdict, word = "CRIT", "NO-GO"
	} else if warn > 0 {
		verdict, word = "WARN", "CAUTION"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "upgrade-gate report: %s\n", name)
	for _, r := range res.Rows {
		b.WriteString(r.Text() + "\n")
	}
	for _, c := range hintOrder {
		if present[c] {
			fmt.Fprintf(&b, "HINT %s: %s\n", c, hints[c])
		}
	}
	fmt.Fprintf(&b, "RESULT %s verdict=%s crit=%d warn=%d operators=%d pools=%d healthy=%d\n",
		verdict, word, crit, warn, res.Operators, res.Pools, res.Operators+res.Pools-len(res.Rows))
	return b.String(), verdict
}

var errNoRead = errors.New("cannot read")
