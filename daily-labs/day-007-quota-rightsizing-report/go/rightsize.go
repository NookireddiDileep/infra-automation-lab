package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type obj = map[string]any

type row struct {
	rank int // 0 = CRIT, 1 = WARN
	ns   string
	q    string
	cls  string
	text string
	idx  int
}

var (
	reCPU     = regexp.MustCompile(`^([0-9]+)(m?)$`)
	reMem     = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti)?$`)
	reInt     = regexp.MustCompile(`^[0-9]+$`)
	reUsageCP = regexp.MustCompile(`^([0-9]+)(n|u|m)?$`)
	reUsageMe = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi)?$`)
	units     = map[string]int64{"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}
	cpuN      = map[string]int64{"": 1_000_000_000, "n": 1, "u": 1000, "m": 1_000_000}
	classes   = []string{"FULL", "NEAR", "OVERQUOTA", "OVERREQUEST", "UNDERREQUEST", "NOMETRICS", "INVALID"}
	hints     = map[string]string{
		"FULL":         "new pods are rejected (exceeded quota); find oversized requests first, then raise hard",
		"NEAR":         "under 15% headroom: a rollout surge can be rejected; plan the bump before the deploy",
		"OVERQUOTA":    "hard is 4x or more the use: shrink it and give the capacity back to the cluster",
		"OVERREQUEST":  "requests far above actual use: lower requests (VPA recommend, oc adm top) to free schedulable capacity",
		"UNDERREQUEST": "pods use more than they request: raise requests or they are throttled or evicted first under node pressure",
		"NOMETRICS":    "no PodMetrics for this namespace: check metrics-server before trusting right-sizing",
		"INVALID":      "quota value is not a valid quantity (or hard is 0): fix the object, the check cannot reason about it",
	}
)

type keyDef struct{ key, kind string }

var keyDefs = []keyDef{{"requests.cpu", "cpu"}, {"requests.memory", "mem"}, {"pods", "int"}}

// qty: cpu -> millicores, mem -> bytes, int -> int. ok=false when not a valid quantity (anything but a string is invalid).
func qty(kind string, v any) (int64, bool) {
	s, isStr := v.(string)
	if !isStr {
		return 0, false
	}
	switch kind {
	case "cpu":
		m := reCPU.FindStringSubmatch(s)
		if m == nil {
			return 0, false
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, false
		}
		if m[2] == "" {
			n *= 1000
		}
		return n, true
	case "mem":
		m := reMem.FindStringSubmatch(s)
		if m == nil {
			return 0, false
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return n * units[m[2]], true
	}
	if !reInt.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func raw(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func asObj(v any) obj {
	if o, ok := v.(obj); ok {
		return o
	}
	return obj{}
}

func itemsOf(data []byte, what string) ([]obj, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %v", what, err)
	}
	d, ok := doc.(obj)
	items, isList := d["items"].([]any)
	if !ok || d["kind"] != "List" || !isList {
		return nil, fmt.Errorf("%s: not a List (expected kind List with items[])", what)
	}
	var out []obj
	for _, it := range items {
		if o, ok := it.(obj); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

type usage struct{ cpu, mem int64 }

// namespaceUsage: CPU is summed in nanocores and floored to millicores at the end; memory in bytes.
func namespaceUsage(items []obj) (map[string]usage, error) {
	nano := map[string]int64{}
	mem := map[string]int64{}
	for _, pm := range items {
		if pm["kind"] != "PodMetrics" {
			continue
		}
		ns, _ := asObj(pm["metadata"])["namespace"].(string)
		if ns == "" {
			return nil, fmt.Errorf("metrics: PodMetrics without metadata.namespace")
		}
		nano[ns] += 0
		mem[ns] += 0
		cs, _ := pm["containers"].([]any)
		for _, c := range cs {
			u := asObj(asObj(c)["usage"])
			cpu, _ := u["cpu"].(string)
			mm, _ := u["memory"].(string)
			a, b := reUsageCP.FindStringSubmatch(cpu), reUsageMe.FindStringSubmatch(mm)
			if a == nil || b == nil {
				return nil, fmt.Errorf("metrics: bad container usage in %s: cpu=%s memory=%s", ns, raw(u["cpu"]), raw(u["memory"]))
			}
			x, _ := strconv.ParseInt(a[1], 10, 64)
			y, _ := strconv.ParseInt(b[1], 10, 64)
			nano[ns] += x * cpuN[a[2]]
			mem[ns] += y * units[b[2]]
		}
	}
	out := make(map[string]usage, len(nano))
	for ns, n := range nano {
		out[ns] = usage{cpu: n / 1_000_000, mem: mem[ns]}
	}
	return out, nil
}

func sev(rank int) string {
	if rank == 0 {
		return "CRIT"
	}
	return "WARN"
}

// quotaFindings returns the findings of one ResourceQuota in emission order.
func quotaFindings(q obj, us map[string]usage) ([]row, error) {
	md := asObj(q["metadata"])
	ns, _ := md["namespace"].(string)
	qn, _ := md["name"].(string)
	if ns == "" || qn == "" {
		return nil, fmt.Errorf("quota: ResourceQuota without metadata.name/namespace")
	}
	status := asObj(q["status"])
	hard, used := asObj(status["hard"]), asObj(status["used"])
	var out []row
	add := func(rank int, cls, detail string) {
		out = append(out, row{rank: rank, ns: ns, q: ns + "/" + qn, cls: cls,
			text: fmt.Sprintf("%s %s quota/%s %s %s", sev(rank), ns, qn, cls, detail)})
	}
	u0, haveUsage := us[ns]
	for _, kd := range keyDefs {
		hv, okH := hard[kd.key]
		uv, okU := used[kd.key]
		if !okH || !okU {
			continue
		}
		h, vh := qty(kd.kind, hv)
		u, vu := qty(kd.kind, uv)
		if !vh || !vu || h == 0 {
			add(1, "INVALID", fmt.Sprintf("%s hard=%s used=%s", kd.key, raw(hv), raw(uv)))
			continue
		}
		pct := u * 100 / h
		detail := fmt.Sprintf("%s used=%s hard=%s pct=%d%%", kd.key, raw(uv), raw(hv), pct)
		switch {
		case pct >= 100:
			add(0, "FULL", detail)
		case pct >= 85:
			add(1, "NEAR", detail)
		case kd.kind != "int" && u*4 <= h:
			add(1, "OVERQUOTA", detail)
		}
		if kd.kind != "int" && haveUsage && u > 0 {
			a, shown := u0.cpu, ""
			if kd.kind == "cpu" {
				shown = fmt.Sprintf("%dm", a)
			} else {
				a = u0.mem
				shown = fmt.Sprintf("%dMi", a/1048576)
			}
			e := fmt.Sprintf("%s requested=%s actual=%s eff=%d%%", kd.key, raw(uv), shown, a*100/u)
			if a*100 < u*30 {
				add(1, "OVERREQUEST", e)
			} else if a*100 > u*120 {
				rank := 1
				if kd.kind == "mem" {
					rank = 0
				}
				add(rank, "UNDERREQUEST", e)
			}
		}
	}
	if c, ok := qty("cpu", used["requests.cpu"]); ok && c > 0 && !haveUsage {
		add(1, "NOMETRICS", fmt.Sprintf("requests.cpu used=%s no PodMetrics", raw(used["requests.cpu"])))
	}
	return out, nil
}

// check returns the report text and exit code. One goroutine per quota; each writes only its own result slot.
func check(quotaJSON, metricsJSON []byte, name string) (string, int, error) {
	qitems, err := itemsOf(quotaJSON, "quota")
	if err != nil {
		return "", 3, err
	}
	mitems, err := itemsOf(metricsJSON, "metrics")
	if err != nil {
		return "", 3, err
	}
	us, err := namespaceUsage(mitems)
	if err != nil {
		return "", 3, err
	}
	var quotas []obj
	for _, it := range qitems {
		if it["kind"] == "ResourceQuota" {
			quotas = append(quotas, it)
		}
	}
	key := func(q obj) string {
		md := asObj(q["metadata"])
		ns, _ := md["namespace"].(string)
		n, _ := md["name"].(string)
		return ns + "\x00" + n
	}
	sort.SliceStable(quotas, func(i, j int) bool { return key(quotas[i]) < key(quotas[j]) })

	results := make([][]row, len(quotas))
	errs := make([]error, len(quotas))
	var wg sync.WaitGroup
	for i := range quotas {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = quotaFindings(quotas[i], us)
		}(i)
	}
	wg.Wait()
	var rows []row
	flagged := map[string]bool{}
	for i := range quotas {
		if errs[i] != nil {
			return "", 3, errs[i]
		}
		for _, r := range results[i] {
			flagged[r.q] = true
			r.idx = len(rows)
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.ns != b.ns {
			return a.ns < b.ns
		}
		return a.idx < b.idx
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "rightsize report: %s\n", name)
	crit, warn := 0, 0
	present := map[string]bool{}
	for _, r := range rows {
		sb.WriteString(r.text + "\n")
		present[r.cls] = true
		if r.rank == 0 {
			crit++
		} else {
			warn++
		}
	}
	for _, c := range classes {
		if present[c] {
			fmt.Fprintf(&sb, "HINT %s: %s\n", c, hints[c])
		}
	}
	result, code := "OK", 0
	if crit > 0 {
		result, code = "CRIT", 2
	} else if warn > 0 {
		result, code = "WARN", 1
	}
	fmt.Fprintf(&sb, "RESULT %s crit=%d warn=%d quotas=%d healthy=%d\n", result, crit, warn, len(quotas), len(quotas)-len(flagged))
	return sb.String(), code, nil
}
