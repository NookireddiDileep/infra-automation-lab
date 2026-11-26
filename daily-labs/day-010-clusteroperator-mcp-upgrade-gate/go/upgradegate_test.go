package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const samples = "../samples"

func runArgs(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := run(args, &out, &errb)
	return rc, out.String(), errb.String()
}

func TestGolden(t *testing.T) {
	for name, want := range map[string]int{"ocp-prod": 2, "ocp-lab": 2, "ocp-stage": 1, "ocp-dev": 0} {
		t.Run(name, func(t *testing.T) {
			rc, out, _ := runArgs(filepath.Join(samples, name+".clusteroperators.json"), filepath.Join(samples, name+".mcps.json"))
			golden, err := os.ReadFile(filepath.Join(samples, "expected", name+".report"))
			if err != nil {
				t.Fatal(err)
			}
			if rc != want {
				t.Errorf("exit %d, want %d", rc, want)
			}
			if out != string(golden) {
				t.Errorf("report differs from golden:\n%s", out)
			}
		})
	}
}

func TestBadInputRefused(t *testing.T) {
	ok := filepath.Join(samples, "ocp-dev.clusteroperators.json")
	okm := filepath.Join(samples, "ocp-dev.mcps.json")
	bad := func(n string) string { return filepath.Join(samples, "bad", n) }
	cases := []struct{ name, cos, mcps, msg string }{
		{"co not a list", bad("not-a-list.json"), okm, "clusteroperators: not a List"},
		{"mcps not a list", ok, bad("not-a-list.json"), "mcps: not a List"},
		{"bad condition status", bad("co-bad-status.json"), okm, "clusteroperators: bad condition status in etcd"},
		{"bad count", ok, bad("mcps-bad-count.json"), "mcps: bad machineCount in worker"},
		{"missing file", "nope.json", okm, "cannot read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc, out, errb := runArgs(c.cos, c.mcps)
			if rc != 3 || out != "" || !strings.Contains(errb, c.msg) {
				t.Errorf("rc=%d out=%q err=%q, want 3, empty, %q", rc, out, errb, c.msg)
			}
		})
	}
	if rc, _, _ := runArgs(); rc != 3 {
		t.Errorf("no args: rc=%d, want 3", rc)
	}
}

func list(items ...any) any { return obj{"kind": "List", "items": items} }

func co(name string, conds ...string) obj { // conds: "Type=Status"
	var cs []any
	for _, c := range conds {
		k, v, _ := strings.Cut(c, "=")
		cs = append(cs, obj{"type": k, "status": v})
	}
	return obj{"kind": "ClusterOperator", "metadata": obj{"name": name}, "status": obj{"conditions": cs}}
}

func pool(name string, total, upd, un, dg float64, paused bool, conds ...string) obj {
	var cs []any
	for _, c := range conds {
		k, v, _ := strings.Cut(c, "=")
		cs = append(cs, obj{"type": k, "status": v})
	}
	return obj{"kind": "MachineConfigPool", "metadata": obj{"name": name}, "spec": obj{"paused": paused},
		"status": obj{"machineCount": total, "updatedMachineCount": upd, "readyMachineCount": total - un,
			"unavailableMachineCount": un, "degradedMachineCount": dg, "conditions": cs}}
}

func classes(t *testing.T, cos, pools any) []string {
	t.Helper()
	r, err := Check(cos, pools)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, row := range r.Rows {
		out = append(out, row.Cls)
	}
	return out
}

func TestRules(t *testing.T) {
	empty := list()
	cases := []struct {
		name  string
		cos   any
		pools any
		want  string
	}{
		{"unavailable beats degraded", list(co("a", "Available=False", "Degraded=True")), empty, "UNAVAILABLE"},
		{"Unknown available is not ok", list(co("a", "Available=Unknown")), empty, "UNAVAILABLE"},
		{"no conditions", list(co("a")), empty, "NOSTATUS"},
		{"blocked", list(co("a", "Available=True", "Upgradeable=False")), empty, "BLOCKED"},
		{"missing upgradeable is fine", list(co("a", "Available=True", "Degraded=False")), empty, ""},
		{"updating with unavailable node is warn only", empty, list(pool("w", 2, 1, 1, 0, false, "Updating=True")), "UPDATING"},
		{"paused behind pool is PAUSED", empty, list(pool("w", 2, 1, 0, 0, true)), "PAUSED"},
		{"idle pool behind is STALLED", empty, list(pool("w", 2, 1, 0, 0, false)), "STALLED"},
		{"degraded count alone", empty, list(pool("w", 2, 2, 0, 1, false)), "DEGRADED"},
		{"unavailable outside rollout", empty, list(pool("w", 2, 2, 1, 0, false)), "UNAVAILABLE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(classes(t, c.cos, c.pools), ",")
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSortIsBytewise(t *testing.T) {
	r, _ := Check(list(co("b", "Available=False"), co("B", "Available=False")), list())
	if r.Rows[0].Subject != "co/B" {
		t.Errorf("got %v first, want co/B", r.Rows[0].Subject)
	}
}

func TestJSONOutput(t *testing.T) {
	rc, out, _ := runArgs("--json", filepath.Join(samples, "ocp-stage.clusteroperators.json"), filepath.Join(samples, "ocp-stage.mcps.json"))
	var d struct {
		Result string
		Rows   []Row
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || rc != 1 || d.Result != "WARN" || len(d.Rows) != 2 {
		t.Errorf("rc=%d err=%v d=%+v", rc, err, d)
	}
}
