package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type pod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Status struct {
		Phase      string      `json:"phase"`
		Conditions []condition `json:"conditions"`
	} `json:"status"`
}

type podList struct {
	Kind  string `json:"kind"`
	Items []pod  `json:"items"`
}

var hints = map[string]string{
	"Capacity":  "oc describe node | grep -A8 'Allocated resources'; add nodes or shrink requests",
	"Cordoned":  "oc get nodes; finish the maintenance, then oc adm uncordon",
	"Gated":     "scheduling gate (Kueue/quota admission?): check the queue and its quota",
	"Other":     "read the full event: oc describe pod",
	"Placement": "check pod (anti-)affinity and topologySpreadConstraints against node/zone count",
	"Ports":     "hostPort conflict: use a Service/NodePort or drop hostPort",
	"Selector":  "oc get nodes --show-labels; fix nodeSelector/affinity or label the nodes",
	"Storage":   "oc get pvc,pv,sc; check StorageClass, volumeBindingMode and the PV zone",
	"Taint":     "oc describe node | grep Taints; add a toleration or remove the taint",
	"Unparsed":  "read the full event: oc describe pod",
}

var (
	prefixRe  = regexp.MustCompile(`^0/([0-9]+) nodes are available: (.*)$`)
	countedRe = regexp.MustCompile(`^([0-9]+) (.*)$`)
)

type cause struct {
	class string
	n     int
}

type verdict struct {
	class, nodes, need string
	causes             []cause
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func hasAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// classify maps one scheduler clause to a class. First matching rule wins (order is part of the contract).
func classify(text string) string {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(text, "Insufficient ") || strings.Contains(text, "Too many pods"):
		return "Capacity"
	case hasAny(low, "persistentvolume", "unbound", "volume node affinity"):
		return "Storage"
	case strings.Contains(low, "taint"):
		return "Taint"
	case hasAny(low, "node affinity", "node selector"):
		return "Selector"
	case hasAny(low, "pod affinity", "anti-affinity", "topology spread"):
		return "Placement"
	case strings.Contains(low, "unschedulable"):
		return "Cordoned"
	case strings.Contains(low, "free ports"):
		return "Ports"
	}
	return "Other"
}

func resource(text string) string {
	if _, after, ok := strings.Cut(text, "Insufficient "); ok {
		return after
	}
	if strings.Contains(text, "Too many pods") {
		return "pods"
	}
	return ""
}

// parseMessage turns a PodScheduled=False condition into a verdict.
func parseMessage(reason, message string) verdict {
	if reason == "SchedulingGated" {
		return verdict{class: "Gated", nodes: "-"}
	}
	m := prefixRe.FindStringSubmatch(message)
	if m == nil {
		return verdict{class: "Unparsed", nodes: "-"}
	}
	total, _ := strconv.Atoi(m[1])
	body, _, _ := strings.Cut(m[2], ". ")
	body = strings.TrimSuffix(body, ".")
	agg := map[string]int{}
	needs := map[string]bool{}
	seen := 0
	for _, raw := range strings.Split(body, ", ") {
		if raw == "" {
			continue
		}
		seen++
		count, text := total, raw
		if q := countedRe.FindStringSubmatch(raw); q != nil {
			count, _ = strconv.Atoi(q[1])
			text = q[2]
		}
		cls := classify(text)
		// Insufficient clauses may count one node twice (cpu AND memory): max. Others are once per node: sum.
		if cls == "Capacity" {
			if count > agg[cls] {
				agg[cls] = count
			}
		} else {
			agg[cls] += count
		}
		if r := resource(text); r != "" {
			needs[r] = true
		}
	}
	if seen == 0 {
		return verdict{class: "Unparsed", nodes: "-"}
	}
	var causes []cause
	for c, n := range agg {
		causes = append(causes, cause{c, n})
	}
	sort.Slice(causes, func(a, b int) bool {
		if causes[a].n != causes[b].n {
			return causes[a].n > causes[b].n
		}
		return causes[a].class < causes[b].class
	})
	var need []string
	for r := range needs {
		need = append(need, r)
	}
	sort.Strings(need)
	return verdict{causes[0].class, strconv.Itoa(total), strings.Join(need, "+"), causes}
}

type row struct {
	rank       int
	key, class string
	text       string
}

// triage parses the pod list JSON and returns the report text and exit code.
func triage(data []byte, name string) (string, int, error) {
	var doc podList
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", 3, err
	}
	if (doc.Kind != "List" && doc.Kind != "PodList") || doc.Items == nil {
		return "", 3, fmt.Errorf("not a Pod List (expected kind List/PodList with items[])")
	}
	var rows []row
	for i := range doc.Items {
		p := &doc.Items[i]
		if p.Status.Phase != "Pending" {
			continue
		}
		var cond *condition
		for j := range p.Status.Conditions {
			if c := &p.Status.Conditions[j]; c.Type == "PodScheduled" && c.Status == "False" {
				cond = c
				break
			}
		}
		if cond == nil {
			continue // already scheduled: image pull / container creation is Day 3's job
		}
		key := orQ(p.Metadata.Namespace) + "/" + orQ(p.Metadata.Name)
		v := parseMessage(cond.Reason, cond.Message)
		rank, sev := 1, "WARN"
		if v.class == "Selector" || v.class == "Storage" || v.class == "Taint" {
			rank, sev = 0, "CRIT"
		}
		parts := make([]string, 0, len(v.causes))
		for _, c := range v.causes {
			parts = append(parts, fmt.Sprintf("%s:%d", c.class, c.n))
		}
		causes := strings.Join(parts, ",")
		if causes == "" {
			causes = "-"
		}
		text := fmt.Sprintf("%s %s %s nodes=%s causes=%s", sev, v.class, key, v.nodes, causes)
		if v.need != "" {
			text += " need=" + v.need
		}
		rows = append(rows, row{rank, key, v.class, text})
	}
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].rank != rows[b].rank {
			return rows[a].rank < rows[b].rank
		}
		return rows[a].key < rows[b].key
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "pendingtriage report: %s\n", name)
	crit, warn := 0, 0
	set := map[string]bool{}
	for _, r := range rows {
		sb.WriteString(r.text + "\n")
		if r.rank == 0 {
			crit++
		} else {
			warn++
		}
		set[r.class] = true
	}
	classes := make([]string, 0, len(set))
	for c := range set {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for _, c := range classes {
		fmt.Fprintf(&sb, "HINT %s: %s\n", c, hints[c])
	}
	result, code := "OK", 0
	if crit > 0 {
		result, code = "CRIT", 2
	} else if warn > 0 {
		result, code = "WARN", 1
	}
	fmt.Fprintf(&sb, "RESULT %s crit=%d warn=%d pods=%d\n", result, crit, warn, len(doc.Items))
	return sb.String(), code, nil
}
