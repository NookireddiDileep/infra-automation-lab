// Package main: PVC Pending / StorageClass binding-mode diagnosis (see ../README.md, "The contract").
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

const (
	defaultAnn  = "storageclass.kubernetes.io/is-default-class"
	selectedAnn = "volume.kubernetes.io/selected-node"
	noProv      = "kubernetes.io/no-provisioner"
	wffc        = "WaitForFirstConsumer"
)

var severity = [3]string{"CRIT", "WARN", "INFO"}

var classOrder = []string{"LOST", "NOSC", "NOPV", "UNKNOWNSC", "SELECTEDSTUCK", "PROVISIONFAILED", "MULTIDEFAULT", "LOCALIMMEDIATE", "WAITFORCONSUMER"}

var hints = map[string]string{
	"LOST":            "the PV behind this claim is gone: oc get pv, check the backend volume; restore from snapshot or recreate, never delete the claim first",
	"NOSC":            "no storageClassName and no default StorageClass: set storageclass.kubernetes.io/is-default-class=true on one class or name a class in the claim",
	"NOPV":            "this claim needs a pre-created PV (empty class or no-provisioner): oc get pv, match size, access mode and class, or create one",
	"UNKNOWNSC":       "the named StorageClass does not exist: oc get sc, fix the typo in the claim or create the class",
	"SELECTEDSTUCK":   "the scheduler picked a node but the provisioner could not create the volume there: oc describe pvc (ProvisioningFailed), check zone and topology of that node",
	"PROVISIONFAILED": "Immediate class, dynamic provisioner, still Pending: oc describe pvc for ProvisioningFailed, check the CSI controller pods and quota on the backend",
	"MULTIDEFAULT":    "more than one default StorageClass: claims without a class get an arbitrary one; keep exactly one default",
	"LOCALIMMEDIATE":  "a no-provisioner class bound Immediate binds before scheduling and strands pods: use volumeBindingMode WaitForFirstConsumer",
	"WAITFORCONSUMER": "not a fault: the claim stays Pending until a pod that uses it is scheduled; if no pod exists yet, this is expected",
}

// InputError means the report is refused (exit 3), never printed half done.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func inputErr(format string, a ...any) error { return &InputError{fmt.Sprintf(format, a...)} }

type obj = map[string]any

// Row is one finding.
type Row struct {
	Rank    int    `json:"rank"`
	Subject string `json:"subject"`
	Class   string `json:"cls"`
	Text    string `json:"text"`
	PVC     bool   `json:"pvc"`
	Idx     int    `json:"idx"`
}

// Result is the judged input.
type Result struct {
	Rows []Row `json:"rows"`
	PVCs int   `json:"pvcs"`
}

type storageClass struct {
	mode, prov string
	isDefault  bool
}

func asObj(v any) obj {
	if m, ok := v.(obj); ok {
		return m
	}
	return obj{}
}

func str(v any) (string, bool) { s, ok := v.(string); return s, ok }

func txt(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "-"
}

func loadList(path, what string) ([]obj, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, inputErr("%s: %v", path, err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, inputErr("%s: %v", path, err)
	}
	m, ok := doc.(obj)
	items, isList := m["items"].([]any)
	if !ok || m["kind"] != "List" || !isList {
		return nil, inputErr("%s: not a List (expected kind List with items[])", what)
	}
	var out []obj
	for _, it := range items {
		if o, ok := it.(obj); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

func parseClasses(items []obj) (map[string]storageClass, error) {
	classes := map[string]storageClass{}
	for _, it := range items {
		if it["kind"] != "StorageClass" {
			continue
		}
		md := asObj(it["metadata"])
		name, ok := str(md["name"])
		if !ok || name == "" {
			return nil, inputErr("storageclasses: StorageClass without metadata.name")
		}
		mode := "Immediate"
		if v := it["volumeBindingMode"]; v != nil {
			if s, ok := v.(string); ok && (s == "Immediate" || s == wffc) {
				mode = s
			} else {
				return nil, inputErr("storageclasses: bad volumeBindingMode in %s", name)
			}
		}
		prov, ok := str(it["provisioner"])
		if !ok {
			prov = "-"
		}
		def := asObj(md["annotations"])[defaultAnn] == "true"
		classes[name] = storageClass{mode: mode, prov: prov, isDefault: def}
	}
	return classes, nil
}

func row(rank int, subject, cls, detail string, pvc bool) *Row {
	return &Row{Rank: rank, Subject: subject, Class: cls, PVC: pvc, Text: fmt.Sprintf("%s %s %s %s", severity[rank], subject, cls, detail)}
}

func subjectOf(p obj) string {
	md := asObj(p["metadata"])
	ns, _ := str(md["namespace"])
	name, _ := str(md["name"])
	return ns + "/" + name
}

// judgePVC returns nil for a healthy (Bound) claim.
func judgePVC(it obj, classes map[string]storageClass, def *string) (*Row, error) {
	md, spec, status := asObj(it["metadata"]), asObj(it["spec"]), asObj(it["status"])
	ns, okNs := str(md["namespace"])
	name, okName := str(md["name"])
	if !okNs || ns == "" || !okName || name == "" {
		return nil, inputErr("pvcs: PersistentVolumeClaim without metadata.namespace or metadata.name")
	}
	subject := ns + "/" + name
	phase, _ := status["phase"].(string)
	if phase != "Pending" && phase != "Bound" && phase != "Lost" {
		return nil, inputErr("pvcs: bad phase in %s", subject)
	}
	raw := spec["storageClassName"]
	if _, isStr := raw.(string); raw != nil && !isStr {
		return nil, inputErr("pvcs: bad storageClassName in %s", subject)
	}
	switch phase {
	case "Bound":
		return nil, nil
	case "Lost":
		return row(0, subject, "LOST", "volume="+txt(spec["volumeName"]), true), nil
	}
	var sc *string
	via := ""
	if s, ok := raw.(string); ok {
		sc = &s
	} else {
		sc, via = def, " via=default"
	}
	if sc == nil {
		return row(0, subject, "NOSC", "class=-", true), nil
	}
	if *sc == "" {
		return row(0, subject, "NOPV", "class=-", true), nil
	}
	cl, ok := classes[*sc]
	if !ok {
		return row(0, subject, "UNKNOWNSC", "class="+*sc, true), nil
	}
	if cl.mode == wffc {
		if node, ok := asObj(md["annotations"])[selectedAnn].(string); ok && node != "" {
			return row(0, subject, "SELECTEDSTUCK", fmt.Sprintf("class=%s node=%s provisioner=%s%s", *sc, node, cl.prov, via), true), nil
		}
		return row(2, subject, "WAITFORCONSUMER", fmt.Sprintf("class=%s mode=%s%s", *sc, wffc, via), true), nil
	}
	if cl.prov == noProv {
		return row(0, subject, "NOPV", "class="+*sc+via, true), nil
	}
	return row(0, subject, "PROVISIONFAILED", fmt.Sprintf("class=%s provisioner=%s%s", *sc, cl.prov, via), true), nil
}

// Check judges every claim (one goroutine each, each writing only its own slot) and every class.
func Check(pvcs, scs []obj) (*Result, error) {
	classes, err := parseClasses(scs)
	if err != nil {
		return nil, err
	}
	var names, defaults []string
	for n, c := range classes {
		names = append(names, n)
		if c.isDefault {
			defaults = append(defaults, n)
		}
	}
	sort.Strings(names)
	sort.Strings(defaults)
	var def *string
	if len(defaults) > 0 {
		def = &defaults[0]
	}

	var claims []obj
	for _, p := range pvcs {
		if p["kind"] == "PersistentVolumeClaim" {
			claims = append(claims, p)
		}
	}
	sort.SliceStable(claims, func(i, j int) bool { return subjectOf(claims[i]) < subjectOf(claims[j]) })

	rows := make([]*Row, len(claims))
	errs := make([]error, len(claims))
	var wg sync.WaitGroup
	for i := range claims {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rows[i], errs[i] = judgePVC(claims[i], classes, def)
		}(i)
	}
	wg.Wait()

	res := &Result{PVCs: len(claims), Rows: []Row{}}
	for i := range claims {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if rows[i] != nil {
			res.Rows = append(res.Rows, *rows[i])
		}
	}
	for _, n := range names {
		c := classes[n]
		if len(defaults) > 1 && c.isDefault {
			res.Rows = append(res.Rows, *row(1, "sc/"+n, "MULTIDEFAULT", "defaults="+strings.Join(defaults, ","), false))
		}
		if c.prov == noProv && c.mode != wffc {
			res.Rows = append(res.Rows, *row(1, "sc/"+n, "LOCALIMMEDIATE", fmt.Sprintf("provisioner=%s mode=%s", noProv, c.mode), false))
		}
	}
	for i := range res.Rows {
		res.Rows[i].Idx = i
	}
	sort.SliceStable(res.Rows, func(i, j int) bool {
		a, b := res.Rows[i], res.Rows[j]
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Idx < b.Idx
	})
	return res, nil
}

// Render returns the report text and the verdict (OK, WARN or CRIT).
func Render(name string, res *Result) (string, string) {
	var n [3]int
	present := map[string]bool{}
	pvcRows := 0
	for _, r := range res.Rows {
		n[r.Rank]++
		present[r.Class] = true
		if r.PVC {
			pvcRows++
		}
	}
	verdict := "OK"
	if n[0] > 0 {
		verdict = "CRIT"
	} else if n[1] > 0 {
		verdict = "WARN"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "pvc-binding report: %s\n", name)
	for _, r := range res.Rows {
		b.WriteString(r.Text + "\n")
	}
	for _, c := range classOrder {
		if present[c] {
			fmt.Fprintf(&b, "HINT %s: %s\n", c, hints[c])
		}
	}
	fmt.Fprintf(&b, "RESULT %s crit=%d warn=%d info=%d pvcs=%d healthy=%d\n", verdict, n[0], n[1], n[2], res.PVCs, res.PVCs-pvcRows)
	return b.String(), verdict
}
