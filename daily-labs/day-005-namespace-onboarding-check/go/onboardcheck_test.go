package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var cases = map[string]int{"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0}

func read(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", "samples"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGolden(t *testing.T) {
	for name, wantCode := range cases {
		t.Run(name, func(t *testing.T) {
			got, code, err := check(read(t, name+".spec.json"), read(t, name+".cluster.json"), name)
			if err != nil {
				t.Fatal(err)
			}
			if want := string(read(t, "expected", name+".report")); got != want {
				t.Errorf("report mismatch\n--- got\n%s--- want\n%s", got, want)
			}
			if code != wantCode {
				t.Errorf("exit code = %d, want %d", code, wantCode)
			}
		})
	}
}

// check is pure, so every cluster can be checked at once; run with -race to prove it.
func TestConcurrentClusters(t *testing.T) {
	var wg sync.WaitGroup
	for name, wantCode := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, code, _ := check(read(t, name+".spec.json"), read(t, name+".cluster.json"), name); code != wantCode {
				t.Errorf("%s: exit %d, want %d", name, code, wantCode)
			}
		}()
	}
	wg.Wait()
}

func TestQuantities(t *testing.T) {
	for _, c := range []struct {
		kind, want string
		have       any
		same       bool
	}{
		{"cpu", "16", "16000m", true},
		{"cpu", "500m", "0.5", true},
		{"cpu", "1", "1500m", false},
		{"cpu", "1", "1.5m", false}, // not a valid quantity: never "equal"
		{"cpu", "1", "1.0005", false},
		{"mem", "32Gi", "32768Mi", true},
		{"mem", "1Gi", "1073741824", true},
		{"mem", "1Gi", "1G", false}, // decimal suffixes unsupported: loud drift, not a silent pass
		{"int", "60", "60", true},
		{"int", "60", float64(60), false}, // JSON number instead of string
		{"int", "60", "sixty", false},
	} {
		if got := same(c.kind, c.want, c.have); got != c.same {
			t.Errorf("same(%s, %q, %v) = %v, want %v", c.kind, c.want, c.have, got, c.same)
		}
	}
}

func TestMissingNamespaceStopsFurtherChecks(t *testing.T) {
	spec := `{"namespaces":[{"name":"ghost","tier":"small","admin_group":"a","viewer_group":"v"}]}`
	got, code, err := check([]byte(spec), read(t, "ocp-dev.cluster.json"), "x")
	if err != nil || code != 2 || !strings.Contains(got, "CRIT ghost MISSING Namespace/ghost\n") || strings.Contains(got, "ResourceQuota") {
		t.Errorf("got %q code %d err %v", got, code, err)
	}
}

func TestClusterAdminOutsideSpecIsIgnored(t *testing.T) {
	spec := `{"namespaces":[{"name":"sandbox","tier":"small","admin_group":"sandbox-admins","viewer_group":"sandbox-viewers"}]}`
	got, code, _ := check([]byte(spec), read(t, "ocp-dev.cluster.json"), "x")
	if code != 0 || strings.Contains(got, "EXTRA RoleBinding") {
		t.Errorf("got %q code %d", got, code)
	}
}

func TestBadInputsAreErrors(t *testing.T) {
	cluster := read(t, "ocp-dev.cluster.json")
	for name, spec := range map[string]string{
		"empty":     `{}`,
		"no list":   `{"namespaces":[]}`,
		"no fields": `{"namespaces":[{"name":"a"}]}`,
		"bad tier":  `{"namespaces":[{"name":"a","tier":"huge","admin_group":"x","viewer_group":"y"}]}`,
		"bad json":  `{not json`,
	} {
		if _, _, err := check([]byte(spec), cluster, "x"); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, _, err := check(read(t, "ocp-dev.spec.json"), read(t, "ocp-dev.spec.json"), "x"); err == nil {
		t.Error("a spec used as the cluster snapshot must be refused")
	}
}

func TestRunExitCodes(t *testing.T) {
	if run(nil) != 3 || run([]string{"a"}) != 3 || run([]string{"/nonexistent", "/nonexistent"}) != 3 {
		t.Error("usage and unreadable files must exit 3")
	}
}
