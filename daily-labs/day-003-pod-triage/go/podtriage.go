package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type container struct {
	Name      string `json:"name"`
	Resources struct {
		Limits map[string]string `json:"limits"`
	} `json:"resources"`
}

type termState struct {
	Reason   string `json:"reason"`
	ExitCode *int   `json:"exitCode"`
}

type containerStatus struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	State struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Terminated *termState `json:"terminated"`
	} `json:"state"`
	LastState struct {
		Terminated *termState `json:"terminated"`
	} `json:"lastState"`
	RestartCount int `json:"restartCount"`
}

type pod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string      `json:"nodeName"`
		Containers []container `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string            `json:"phase"`
		Reason            string            `json:"reason"`
		ContainerStatuses []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type podList struct {
	Kind  string `json:"kind"`
	Items []pod  `json:"items"`
}

var hints = map[string]string{
	"ContainerConfigError": "oc describe pod: missing Secret/ConfigMap key or bad securityContext",
	"CrashLoopBackOff":     "oc logs -p <pod> -c <container>; check exit code, probes, startup deps",
	"Evicted":              "node pressure: oc describe node; set ephemeral-storage requests/limits",
	"HighRestarts":         "oc logs -p; check liveness probe and exit codes before it becomes a CrashLoop",
	"ImagePullBackOff":     "oc describe pod: image tag, pull secret, registry reachability",
	"OOMKilled":            "compare limit with working set (oc adm top pod); raise limit or fix the leak",
}

type row struct {
	rank      int
	key, ctr  string
	class, tx string
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// classify returns (rank, class, detail, ok) for one container status. First matching rule wins.
func classify(p *pod, cs *containerStatus, restartWarn int) (int, string, string, bool) {
	waiting := ""
	if cs.State.Waiting != nil {
		waiting = cs.State.Waiting.Reason
	}
	last := cs.LastState.Terminated
	cur := cs.State.Terminated
	switch {
	case waiting == "ImagePullBackOff" || waiting == "ErrImagePull":
		return 0, "ImagePullBackOff", "image=" + orQ(cs.Image), true
	case waiting == "CreateContainerConfigError" || waiting == "CreateContainerError":
		return 0, "ContainerConfigError", "reason=" + waiting, true
	case (last != nil && last.Reason == "OOMKilled") || (cur != nil && cur.Reason == "OOMKilled"):
		limit := "none"
		for _, c := range p.Spec.Containers {
			if c.Name == cs.Name {
				if v, ok := c.Resources.Limits["memory"]; ok {
					limit = v
				} else {
					limit = "none"
				}
			}
		}
		rank := 1
		if waiting != "" || cur != nil {
			rank = 0
		}
		return rank, "OOMKilled", fmt.Sprintf("restarts=%d limit=%s", cs.RestartCount, limit), true
	case waiting == "CrashLoopBackOff":
		exit := "?"
		if last != nil && last.ExitCode != nil {
			exit = fmt.Sprint(*last.ExitCode)
		}
		return 0, "CrashLoopBackOff", fmt.Sprintf("restarts=%d exit=%s", cs.RestartCount, exit), true
	case cs.RestartCount >= restartWarn:
		return 1, "HighRestarts", fmt.Sprintf("restarts=%d", cs.RestartCount), true
	}
	return 0, "", "", false
}

// triage parses the pod list JSON and returns the report text and exit code.
func triage(data []byte, name string, restartWarn int) (string, int, error) {
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
		key := orQ(p.Metadata.Namespace) + "/" + orQ(p.Metadata.Name)
		if p.Status.Phase == "Failed" && p.Status.Reason == "Evicted" {
			rows = append(rows, row{1, key, "-", "Evicted", fmt.Sprintf("WARN Evicted %s - node=%s", key, orQ(p.Spec.NodeName))})
			continue
		}
		if p.Status.Phase != "Running" && p.Status.Phase != "Pending" {
			continue
		}
		for j := range p.Status.ContainerStatuses {
			cs := &p.Status.ContainerStatuses[j]
			rank, class, detail, ok := classify(p, cs, restartWarn)
			if !ok {
				continue
			}
			sev := "CRIT"
			if rank == 1 {
				sev = "WARN"
			}
			rows = append(rows, row{rank, key, orQ(cs.Name), class, fmt.Sprintf("%s %s %s %s %s", sev, class, key, orQ(cs.Name), detail)})
		}
	}
	sort.Slice(rows, func(a, b int) bool {
		x, y := rows[a], rows[b]
		if x.rank != y.rank {
			return x.rank < y.rank
		}
		if x.key != y.key {
			return x.key < y.key
		}
		return x.ctr < y.ctr
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "podtriage report: %s\n", name)
	crit, warn := 0, 0
	seen := map[string]bool{}
	var classes []string
	for _, r := range rows {
		sb.WriteString(r.tx + "\n")
		if r.rank == 0 {
			crit++
		} else {
			warn++
		}
		if !seen[r.class] {
			seen[r.class] = true
			classes = append(classes, r.class)
		}
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
