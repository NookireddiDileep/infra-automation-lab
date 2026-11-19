package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type tier struct{ cpu, mem, pods, limCPU, limMem string }

var tiers = map[string]tier{
	"small":  {"4", "8Gi", "20", "500m", "512Mi"},
	"medium": {"16", "32Gi", "60", "1", "1Gi"},
	"large":  {"64", "128Gi", "200", "2", "2Gi"},
}

var hints = map[string]string{
	"DRIFT":   "re-apply the spec (ansible-playbook apply_onboarding.yml), then re-run this check",
	"EXTRA":   "oc get rolebindings -A | grep -i admin; remove it or record the exception",
	"MISSING": "onboarding never ran or was reverted: apply the spec (ansible-playbook apply_onboarding.yml)",
}

var (
	cpuRE = regexp.MustCompile(`^([0-9]+m|[0-9]+(\.[0-9]{1,3})?)$`)
	memRE = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti)?$`)
	intRE = regexp.MustCompile(`^[0-9]+$`)
	units = map[string]uint64{"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}
)

type nsSpec struct{ name, tier, admin, viewer string }

type finding struct {
	rank      int // 0 = CRIT, 1 = WARN
	ns, class string
	text      string
}

// --- tiny helpers over decoded JSON (map[string]any / []any / string)

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m } // nil when absent or not an object
func list(v any) []any         { l, _ := v.([]any); return l }
func str(v any) string         { s, _ := v.(string); return s }

// qty normalises a quantity: cpu -> millicores, mem -> bytes, int -> int. ok=false when invalid
// (the API serialises quantities as strings; any other JSON type is invalid).
func qty(kind string, v any) (uint64, bool) {
	s, isStr := v.(string)
	if !isStr {
		return 0, false
	}
	switch kind {
	case "cpu":
		if !cpuRE.MatchString(s) {
			return 0, false
		}
		if strings.HasSuffix(s, "m") {
			n, err := strconv.ParseUint(strings.TrimSuffix(s, "m"), 10, 64)
			return n, err == nil
		}
		f, err := strconv.ParseFloat(s, 64)
		return uint64(math.Round(f * 1000)), err == nil
	case "mem":
		m := memRE.FindStringSubmatch(s)
		if m == nil {
			return 0, false
		}
		n, err := strconv.ParseUint(m[1], 10, 64)
		return n * units[m[2]], err == nil
	default:
		if !intRE.MatchString(s) {
			return 0, false
		}
		n, err := strconv.ParseUint(s, 10, 64)
		return n, err == nil
	}
}

func same(kind, want string, have any) bool {
	w, ok1 := qty(kind, want)
	h, ok2 := qty(kind, have)
	return ok1 && ok2 && w == h
}

func show(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func subj(subjects any) string {
	var parts []string
	for _, s := range list(subjects) {
		m := obj(s)
		if m == nil {
			continue
		}
		k, n := str(m["kind"]), str(m["name"])
		if k == "" {
			k = "?"
		}
		if n == "" {
			n = "?"
		}
		parts = append(parts, k+":"+n)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func validateSpec(data []byte) ([]nsSpec, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	items := list(obj(doc)["namespaces"])
	if len(items) == 0 {
		return nil, errors.New("spec: expected an object with a non-empty namespaces[]")
	}
	out := make([]nsSpec, 0, len(items))
	for _, it := range items {
		m := obj(it)
		s := nsSpec{str(m["name"]), str(m["tier"]), str(m["admin_group"]), str(m["viewer_group"])}
		if m == nil || s.name == "" || s.tier == "" || s.admin == "" || s.viewer == "" {
			return nil, errors.New("spec: every namespace needs name, tier, admin_group and viewer_group (non-empty strings)")
		}
		if _, ok := tiers[s.tier]; !ok {
			return nil, fmt.Errorf("spec: %s: unknown tier %q (small|medium|large)", s.name, s.tier)
		}
		out = append(out, s)
	}
	return out, nil
}

type key struct{ kind, name string }

// checkNamespace returns the findings of one namespace in emission order. Pure: safe to run concurrently.
func checkNamespace(n nsSpec, objs map[key]map[string]any, order []map[string]any) []finding {
	var out []finding
	add := func(rank int, class, object, detail string) {
		sev := "WARN"
		if rank == 0 {
			sev = "CRIT"
		}
		t := sev + " " + n.name + " " + class + " " + object
		if detail != "" {
			t += " " + detail
		}
		out = append(out, finding{rank, n.name, class, t})
	}
	if _, ok := objs[key{"Namespace", n.name}]; !ok {
		add(0, "MISSING", "Namespace/"+n.name, "")
		return out
	}
	t := tiers[n.tier]

	if q, ok := objs[key{"ResourceQuota", "onboarding-quota"}]; !ok {
		add(0, "MISSING", "ResourceQuota/onboarding-quota", "")
	} else {
		hard := obj(obj(q["spec"])["hard"])
		for _, c := range [][3]string{{"requests.cpu", t.cpu, "cpu"}, {"requests.memory", t.mem, "mem"}, {"pods", t.pods, "int"}} {
			have, present := hard[c[0]]
			switch {
			case !present:
				add(0, "DRIFT", "ResourceQuota/onboarding-quota", fmt.Sprintf("%s want=%s have=-", c[0], c[1])) // unbounded: dangerous
			case !same(c[2], c[1], have):
				add(1, "DRIFT", "ResourceQuota/onboarding-quota", fmt.Sprintf("%s want=%s have=%s", c[0], c[1], show(have)))
			}
		}
	}

	if l, ok := objs[key{"LimitRange", "onboarding-limits"}]; !ok {
		add(1, "MISSING", "LimitRange/onboarding-limits", "")
	} else {
		var dflt map[string]any
		for _, it := range list(obj(l["spec"])["limits"]) {
			if m := obj(it); str(m["type"]) == "Container" {
				dflt = obj(m["default"])
				break
			}
		}
		for _, c := range [][4]string{{"default.cpu", t.limCPU, "cpu", "cpu"}, {"default.memory", t.limMem, "mem", "memory"}} {
			have := dflt[c[3]]
			switch {
			case have == nil:
				add(1, "DRIFT", "LimitRange/onboarding-limits", fmt.Sprintf("%s want=%s have=-", c[0], c[1]))
			case !same(c[2], c[1], have):
				add(1, "DRIFT", "LimitRange/onboarding-limits", fmt.Sprintf("%s want=%s have=%s", c[0], c[1], show(have)))
			}
		}
	}

	for _, c := range [][3]string{{"onboarding-admin", "admin", n.admin}, {"onboarding-view", "view", n.viewer}} {
		rb, ok := objs[key{"RoleBinding", c[0]}]
		if !ok {
			add(0, "MISSING", "RoleBinding/"+c[0], "")
			continue
		}
		haveRole := str(obj(rb["roleRef"])["name"])
		if haveRole == "" {
			haveRole = "-"
		}
		if haveRole != c[1] {
			add(0, "DRIFT", "RoleBinding/"+c[0], fmt.Sprintf("roleRef want=%s have=%s", c[1], haveRole))
		}
		found := false
		for _, s := range list(rb["subjects"]) {
			if m := obj(s); str(m["kind"]) == "Group" && str(m["name"]) == c[2] {
				found = true
			}
		}
		if !found {
			add(0, "DRIFT", "RoleBinding/"+c[0], fmt.Sprintf("subjects want=Group:%s have=%s", c[2], subj(rb["subjects"])))
		}
	}
	for _, rb := range order { // every RoleBinding of this namespace, in cluster order
		if str(obj(rb["roleRef"])["name"]) == "cluster-admin" {
			name := str(obj(rb["metadata"])["name"])
			if name == "" {
				name = "?"
			}
			add(0, "EXTRA", "RoleBinding/"+name, "cluster-admin to "+subj(rb["subjects"]))
		}
	}

	if p, ok := objs[key{"NetworkPolicy", "default-deny-ingress"}]; !ok {
		add(0, "MISSING", "NetworkPolicy/default-deny-ingress", "")
	} else {
		sp := obj(p["spec"])
		hasIngressType := false
		for _, pt := range list(sp["policyTypes"]) {
			if str(pt) == "Ingress" {
				hasIngressType = true
			}
		}
		if !(len(obj(sp["podSelector"])) == 0 && len(list(sp["ingress"])) == 0 && hasIngressType) {
			add(0, "DRIFT", "NetworkPolicy/default-deny-ingress", "not deny-all")
		}
	}
	if _, ok := objs[key{"NetworkPolicy", "allow-from-openshift-ingress"}]; !ok {
		add(1, "MISSING", "NetworkPolicy/allow-from-openshift-ingress", "")
	}
	return out
}

// check returns the report and the exit code. Namespaces are checked concurrently (one goroutine each);
// results land in per-index slots, so the output never depends on goroutine scheduling.
func check(specData, clusterData []byte, name string) (string, int, error) {
	namespaces, err := validateSpec(specData)
	if err != nil {
		return "", 0, err
	}
	var doc any
	if err := json.Unmarshal(clusterData, &doc); err != nil {
		return "", 0, fmt.Errorf("cluster: %w", err)
	}
	d := obj(doc)
	items, isList := d["items"].([]any)
	if d == nil || str(d["kind"]) != "List" || !isList {
		return "", 0, errors.New("cluster: not a List (expected kind List with items[])")
	}
	byNS := map[string]map[key]map[string]any{}
	rbOrder := map[string][]map[string]any{}
	for _, it := range items {
		m := obj(it)
		if m == nil {
			continue
		}
		kind, meta := str(m["kind"]), obj(m["metadata"])
		oname, owner := str(meta["name"]), str(meta["namespace"])
		if kind == "Namespace" {
			owner = oname
		}
		switch kind {
		case "Namespace", "ResourceQuota", "LimitRange", "RoleBinding", "NetworkPolicy":
		default:
			continue
		}
		if owner == "" || oname == "" {
			continue
		}
		if byNS[owner] == nil {
			byNS[owner] = map[key]map[string]any{}
		}
		k := key{kind, oname}
		if _, dup := byNS[owner][k]; !dup && kind == "RoleBinding" {
			rbOrder[owner] = append(rbOrder[owner], m)
		}
		byNS[owner][k] = m
	}

	results := make([][]finding, len(namespaces))
	var wg sync.WaitGroup
	for i, n := range namespaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = checkNamespace(n, byNS[n.name], rbOrder[n.name])
		}()
	}
	wg.Wait()

	var rows []finding
	for _, r := range results {
		rows = append(rows, r...)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		return rows[i].ns < rows[j].ns
	})
	crit, bad, classes := 0, map[string]bool{}, map[string]bool{}
	var sb strings.Builder
	fmt.Fprintf(&sb, "onboardcheck report: %s\n", name)
	for _, r := range rows {
		sb.WriteString(r.text + "\n")
		if r.rank == 0 {
			crit++
		}
		bad[r.ns], classes[r.class] = true, true
	}
	warn := len(rows) - crit
	names := make([]string, 0, len(classes))
	for c := range classes {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		fmt.Fprintf(&sb, "HINT %s: %s\n", c, hints[c])
	}
	result, code := "OK", 0
	if crit > 0 {
		result, code = "CRIT", 2
	} else if warn > 0 {
		result, code = "WARN", 1
	}
	fmt.Fprintf(&sb, "RESULT %s crit=%d warn=%d namespaces=%d clean=%d\n", result, crit, warn, len(namespaces), len(namespaces)-len(bad))
	return sb.String(), code, nil
}
