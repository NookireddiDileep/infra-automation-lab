package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGolden(t *testing.T) {
	cases := map[string]int{"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}
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
			got, code, err := triage(in, name, 5)
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

func TestClassify(t *testing.T) {
	p := &pod{}
	p.Spec.Containers = []container{{Name: "c"}}
	running := &containerStatus{Name: "c", RestartCount: 1}
	running.LastState.Terminated = &termState{Reason: "OOMKilled"}
	if r, c, _, ok := classify(p, running, 5); !ok || r != 1 || c != "OOMKilled" {
		t.Errorf("running+OOM = %d %s %v, want WARN OOMKilled", r, c, ok)
	}
	running.State.Waiting = &struct {
		Reason string `json:"reason"`
	}{Reason: "CrashLoopBackOff"}
	if r, c, d, _ := classify(p, running, 5); r != 0 || c != "OOMKilled" || d != "restarts=1 limit=none" {
		t.Errorf("down+OOM = %d %s %q", r, c, d)
	}
	hi := &containerStatus{Name: "c", RestartCount: 3}
	if _, _, _, ok := classify(p, hi, 5); ok {
		t.Error("3 restarts must not flag at threshold 5")
	}
	if _, c, _, ok := classify(p, hi, 3); !ok || c != "HighRestarts" {
		t.Error("3 restarts must flag at threshold 3")
	}
}

func TestRejectsNonPodList(t *testing.T) {
	for _, bad := range []string{`[]`, `{"kind":"Pod"}`, `{"kind":"List"}`, `{nope`} {
		if _, code, err := triage([]byte(bad), "x", 5); err == nil || code != 3 {
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
	if got := run([]string{"-r", "0", prod}); got != 3 {
		t.Errorf("-r 0 = %d, want 3", got)
	}
}
