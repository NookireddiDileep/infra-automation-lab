package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

var cases = map[string]int{"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}

func TestGolden(t *testing.T) {
	for name, wantCode := range cases {
		t.Run(name, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join("..", "samples", name+".pods.json"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("..", "samples", "expected", name+".report"))
			if err != nil {
				t.Fatal(err)
			}
			got, code, err := triage(in, name)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("report mismatch\n--- got\n%s--- want\n%s", got, want)
			}
			if code != wantCode {
				t.Errorf("exit code = %d, want %d", code, wantCode)
			}
		})
	}
}

// triage is pure, so many snapshots (one per cluster) can be classified concurrently.
func TestConcurrentClusters(t *testing.T) {
	var wg sync.WaitGroup
	for name, wantCode := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, err := os.ReadFile(filepath.Join("..", "samples", name+".pods.json"))
			if err != nil {
				t.Error(err)
				return
			}
			if _, code, _ := triage(in, name); code != wantCode {
				t.Errorf("%s: exit %d, want %d", name, code, wantCode)
			}
		}()
	}
	wg.Wait()
}

func TestClassify(t *testing.T) {
	for text, want := range map[string]string{
		"3 Insufficient nvidia.com/gpu":                          "Capacity",
		"4 Too many pods":                                        "Capacity",
		"8 node(s) had volume node affinity conflict":            "Storage", // must beat the Selector rule
		"2 node(s) had untolerated taint {a: b}":                 "Taint",
		"3 node(s) didn't match Pod's node affinity/selector":    "Selector",
		"10 node(s) didn't match pod anti-affinity rules":        "Placement",
		"2 node(s) were unschedulable":                           "Cordoned",
		"node(s) didn't have free ports for the requested ports": "Ports",
		"something new": "Other",
		"persistentvolumeclaim \"data\" not found": "Storage",
	} {
		if got := classify(text); got != want {
			t.Errorf("classify(%q) = %s, want %s", text, got, want)
		}
	}
}

func TestParseMessage(t *testing.T) {
	// clause without a count applies to every node; the preemption tail is cut
	v := parseMessage("Unschedulable", "0/9 nodes are available: pod has unbound immediate PersistentVolumeClaims. preemption: 0/9 nodes are available: 9 Preemption is not helpful for scheduling.")
	if v.class != "Storage" || v.nodes != "9" || !reflect.DeepEqual(v.causes, []cause{{"Storage", 9}}) {
		t.Errorf("pvc verdict = %+v", v)
	}
	// Insufficient clauses overlap (max), a tie breaks by class name
	v = parseMessage("Unschedulable", "0/6 nodes are available: 4 Insufficient cpu, 5 Insufficient memory, 1 node(s) were unschedulable.")
	if v.class != "Capacity" || v.causes[0].n != 5 || v.need != "cpu+memory" {
		t.Errorf("capacity verdict = %+v", v)
	}
	v = parseMessage("Unschedulable", "0/6 nodes are available: 3 Insufficient cpu, 3 node(s) had untolerated taint {k: v}.")
	if v.class != "Capacity" {
		t.Errorf("tie must go to the alphabetically first class, got %s", v.class)
	}
	if v = parseMessage("SchedulingGated", "x"); v.class != "Gated" {
		t.Errorf("gated = %+v", v)
	}
	if v = parseMessage("Unschedulable", "weird"); v.class != "Unparsed" {
		t.Errorf("unparsed = %+v", v)
	}
}

func TestRejectsNonPodList(t *testing.T) {
	for _, bad := range []string{`[]`, `{"kind":"Pod"}`, `{"kind":"List"}`, `{nope`} {
		if _, code, err := triage([]byte(bad), "x"); err == nil || code != 3 {
			t.Errorf("%q: want error/3, got err=%v code=%d", bad, err, code)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	prod := filepath.Join("..", "samples", "ocp-prod.pods.json")
	if got := run([]string{prod}); got != 2 {
		t.Errorf("prod = %d, want 2", got)
	}
	if got := run([]string{filepath.Join(t.TempDir(), "missing.json")}); got != 3 {
		t.Errorf("missing = %d, want 3", got)
	}
	if got := run(nil); got != 3 {
		t.Errorf("no args = %d, want 3", got)
	}
}
