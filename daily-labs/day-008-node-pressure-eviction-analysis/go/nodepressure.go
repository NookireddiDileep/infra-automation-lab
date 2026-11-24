package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const stormAt = 5 // evictions on one node at which the finding becomes CRIT

type pressure struct{ ctype, class, taint string }

var pressures = []pressure{
	{"MemoryPressure", "MEMORYPRESSURE", "node.kubernetes.io/memory-pressure"},
	{"DiskPressure", "DISKPRESSURE", "node.kubernetes.io/disk-pressure"},
	{"PIDPressure", "PIDPRESSURE", "node.kubernetes.io/pid-pressure"},
}

var classes = []string{"NOTREADY", "MEMORYPRESSURE", "DISKPRESSURE", "PIDPRESSURE", "NOTAINT", "EVICTIONSTORM", "EVICTIONS"}

var hints = map[string]string{
	"NOTREADY":       "kubelet is not posting Ready: oc describe node, oc adm node-logs NODE -u kubelet; pods are evicted after the unreachable toleration (300s)",
	"MEMORYPRESSURE": "memory.available is under the eviction threshold: find the top consumers (oc adm top pods), fix requests/limits, check kubelet systemReserved",
	"DISKPRESSURE":   "nodefs/imagefs is low: crictl images / du on /var/lib/containers, rotate logs, check ephemeral-storage requests",
	"PIDPRESSURE":    "pid.available is low: find a fork bomb or leaking process (ps -eLf), raise podPidsLimit only after you know why",
	"NOTAINT":        "pressure is True but the node.kubernetes.io/*-pressure taint is missing: the scheduler may still place pods here",
	"EVICTIONSTORM":  "5+ evictions on one node: the pressure is chronic; cordon, drain and fix the cause before it takes the node down",
	"EVICTIONS":      "kubelet evicted pods here: the pressure came and went; compare victims' usage with their requests (BestEffort/Burstable go first)",
}

var resourceRE = regexp.MustCompile(`low on resource: ([A-Za-z0-9-]+)\.`)

type obj = map[string]any

type finding struct {
	sev, node, class, detail string
	idx                      int
}

func asObj(v any) obj {
	if m, ok := v.(obj); ok {
		return m
	}
	return obj{}
}

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

func text(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "-"
}

// itemsOf returns the object items of a kind List, or an error naming what.
func itemsOf(data []byte, what string) ([]obj, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %v", what, err)
	}
	d, ok := doc.(obj)
	items, isArr := d["items"].([]any)
	if !ok || d["kind"] != "List" || !isArr {
		return nil, fmt.Errorf("%s: not a List (expected kind List with items[])", what)
	}
	var out []obj
	for _, it := range items {
		if m, ok := it.(obj); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// evictionsByHost: host -> resource -> count, from kubelet Evicted events (count defaults to 1).
func evictionsByHost(data []byte) (map[string]map[string]int, error) {
	items, err := itemsOf(data, "events")
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{}
	for _, e := range items {
		if e["reason"] != "Evicted" {
			continue
		}
		host, ok := str(asObj(e["source"])["host"])
		if !ok || host == "" {
			return nil, fmt.Errorf("events: Evicted event without source.host")
		}
		n := 1.0
		if c, present := e["count"]; present {
			f, isNum := c.(float64)
			if !isNum || f < 1 || f != math.Floor(f) {
				return nil, fmt.Errorf("events: bad count in Evicted event on %s", host)
			}
			n = f
		}
		res := "unknown"
		if msg, ok := str(e["message"]); ok {
			if m := resourceRE.FindStringSubmatch(msg); m != nil {
				res = m[1]
			}
		}
		if out[host] == nil {
			out[host] = map[string]int{}
		}
		out[host][res] += int(n)
	}
	return out, nil
}

// nodeFindings returns one node's findings in emission order.
func nodeFindings(n obj, evicted map[string]map[string]int) ([]finding, error) {
	name := n["metadata"].(obj)["name"].(string)
	conds := map[string]obj{}
	if list, ok := asObj(n["status"])["conditions"].([]any); ok {
		for _, c := range list {
			cm, ok := c.(obj)
			if !ok {
				continue
			}
			t, _ := str(cm["type"])
			if t != "Ready" && t != "MemoryPressure" && t != "DiskPressure" && t != "PIDPressure" {
				continue
			}
			if s, _ := str(cm["status"]); s != "True" && s != "False" && s != "Unknown" {
				return nil, fmt.Errorf("nodes: bad condition status in %s: %s", name, t)
			}
			conds[t] = cm
		}
	}
	taints := map[string]bool{}
	if list, ok := asObj(n["spec"])["taints"].([]any); ok {
		for _, t := range list {
			if k, ok := str(asObj(t)["key"]); ok {
				taints[k] = true
			}
		}
	}
	var out []finding
	add := func(sev, class, detail string) {
		out = append(out, finding{sev: sev, node: name, class: class, detail: detail})
	}
	if r, ok := conds["Ready"]; !ok || r["status"] != "True" {
		if !ok {
			add("CRIT", "NOTREADY", "status=Missing reason=-")
		} else {
			add("CRIT", "NOTREADY", fmt.Sprintf("status=%s reason=%s", r["status"], text(r["reason"])))
		}
	}
	for _, p := range pressures {
		if c, ok := conds[p.ctype]; ok && c["status"] == "True" {
			add("CRIT", p.class, fmt.Sprintf("since=%s reason=%s", text(c["lastTransitionTime"]), text(c["reason"])))
			if !taints[p.taint] {
				add("WARN", "NOTAINT", p.taint)
			}
		}
	}
	if ev := evicted[name]; len(ev) > 0 {
		total := 0
		keys := make([]string, 0, len(ev))
		for k, v := range ev {
			total += v
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := []string{fmt.Sprintf("total=%d", total)}
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, ev[k]))
		}
		sev, class := "WARN", "EVICTIONS"
		if total >= stormAt {
			sev, class = "CRIT", "EVICTIONSTORM"
		}
		add(sev, class, strings.Join(parts, " "))
	}
	return out, nil
}

// check builds the report text and exit code. One goroutine per node writes only its own slot.
func check(nodesJSON, eventsJSON []byte, name string) (string, int, error) {
	all, err := itemsOf(nodesJSON, "nodes")
	if err != nil {
		return "", 3, err
	}
	var nodes []obj
	for _, n := range all {
		if n["kind"] != "Node" {
			continue
		}
		if nm, ok := str(asObj(n["metadata"])["name"]); !ok || nm == "" {
			return "", 3, fmt.Errorf("nodes: Node without metadata.name")
		}
		nodes = append(nodes, n)
	}
	evicted, err := evictionsByHost(eventsJSON)
	if err != nil {
		return "", 3, err
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i]["metadata"].(obj)["name"].(string) < nodes[j]["metadata"].(obj)["name"].(string)
	})
	slots := make([][]finding, len(nodes))
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slots[i], errs[i] = nodeFindings(nodes[i], evicted)
		}(i)
	}
	wg.Wait()
	var rows []finding
	for i := range slots {
		if errs[i] != nil {
			return "", 3, errs[i]
		}
		for _, f := range slots[i] {
			f.idx = len(rows)
			rows = append(rows, f)
		}
	}
	rank := func(s string) int {
		if s == "CRIT" {
			return 0
		}
		return 1
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if rank(a.sev) != rank(b.sev) {
			return rank(a.sev) < rank(b.sev)
		}
		if a.node != b.node {
			return a.node < b.node
		}
		return a.idx < b.idx
	})
	crit, flagged, present := 0, map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		if r.sev == "CRIT" {
			crit++
		}
		flagged[r.node] = true
		present[r.class] = true
	}
	warn := len(rows) - crit
	result, code := "OK", 0
	if crit > 0 {
		result, code = "CRIT", 2
	} else if warn > 0 {
		result, code = "WARN", 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "node-pressure report: %s\n", name)
	for _, r := range rows {
		fmt.Fprintf(&b, "%s %s %s %s\n", r.sev, r.node, r.class, r.detail)
	}
	for _, c := range classes {
		if present[c] {
			fmt.Fprintf(&b, "HINT %s: %s\n", c, hints[c])
		}
	}
	fmt.Fprintf(&b, "RESULT %s crit=%d warn=%d nodes=%d healthy=%d\n", result, crit, warn, len(nodes), len(nodes)-len(flagged))
	return b.String(), code, nil
}
