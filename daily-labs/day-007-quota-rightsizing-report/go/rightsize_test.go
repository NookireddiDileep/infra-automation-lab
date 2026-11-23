package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "samples", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGolden(t *testing.T) {
	for name, want := range map[string]int{"ocp-prod": 2, "ocp-stage": 1, "ocp-dev": 0} {
		text, code, err := check(sample(t, name+".quota.json"), sample(t, name+".metrics.json"), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if golden := string(sample(t, "expected/"+name+".report")); text != golden {
			t.Errorf("%s: report differs from golden\n--- got\n%s--- want\n%s", name, text, golden)
		}
		if code != want {
			t.Errorf("%s: exit %d, want %d", name, code, want)
		}
	}
}

func TestQuantities(t *testing.T) {
	cases := []struct {
		kind string
		v    any
		want int64
		ok   bool
	}{
		{"cpu", "16", 16000, true}, {"cpu", "500m", 500, true}, {"cpu", "1.5", 0, false}, {"cpu", "", 0, false}, {"cpu", 16.0, 0, false},
		{"mem", "1Gi", 1 << 30, true}, {"mem", "512Mi", 512 << 20, true}, {"mem", "1G", 0, false}, {"mem", "1.5Gi", 0, false},
		{"int", "60", 60, true}, {"int", 60.0, 0, false}, {"int", "-1", 0, false}, {"int", "6\n", 0, false},
	}
	for _, c := range cases {
		got, ok := qty(c.kind, c.v)
		if got != c.want || ok != c.ok {
			t.Errorf("qty(%s,%v) = %d,%v want %d,%v", c.kind, c.v, got, ok, c.want, c.ok)
		}
	}
}

func lst(items ...string) []byte {
	s := `{"kind":"List","items":[`
	for i, it := range items {
		if i > 0 {
			s += ","
		}
		s += it
	}
	return []byte(s + `]}`)
}

const q10 = `{"kind":"ResourceQuota","metadata":{"namespace":"a","name":"q"},"status":{"hard":{"requests.cpu":"10","requests.memory":"10Gi"},"used":{"requests.cpu":"5","requests.memory":"5Gi"}}}`

func pm(cpu, mem string) string {
	return `{"kind":"PodMetrics","metadata":{"namespace":"a"},"containers":[{"usage":{"cpu":"` + cpu + `","memory":"` + mem + `"}}]}`
}

func classesOf(t *testing.T, q, m []byte) string {
	t.Helper()
	text, _, err := check(q, m, "t")
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestEfficiencyBoundaries(t *testing.T) {
	for _, c := range []struct{ cpu, mem, want string }{
		{"1500m", "1536Mi", "RESULT OK"},       // exactly 30%: not OVERREQUEST
		{"1499m", "1536Mi", "OVERREQUEST"},     // just under
		{"6000m", "6Gi", "RESULT OK"},          // exactly 120%: not UNDERREQUEST
		{"6001m", "6Gi", "UNDERREQUEST"},       // just over
		{"500000n", "1Ki", "actual=0m eff=0%"}, // sub-millicore usage floors to 0
		{"1000000n", "1Ki", "OVERREQUEST"},     // units are normalised
	} {
		if got := classesOf(t, lst(q10), lst(pm(c.cpu, c.mem))); !strings.Contains(got, c.want) {
			t.Errorf("cpu=%s mem=%s: want %q in\n%s", c.cpu, c.mem, c.want, got)
		}
	}
}

func TestMemoryUnderrequestIsCrit(t *testing.T) {
	text, code, err := check(lst(q10), lst(pm("5000m", "9Gi")), "t")
	if err != nil || code != 2 || !strings.Contains(text, "CRIT a quota/q UNDERREQUEST requests.memory") {
		t.Errorf("got code=%d err=%v\n%s", code, err, text)
	}
}

func TestBadInputIsRefused(t *testing.T) {
	cases := map[string][2][]byte{
		"not a list":         {[]byte(`{"kind":"Pod"}`), lst()},
		"metrics not a list": {lst(), []byte(`[]`)},
		"quota no namespace": {lst(`{"kind":"ResourceQuota","metadata":{"name":"x"}}`), lst()},
		"bad cpu usage":      {lst(), lst(pm("1.5", "1Mi"))},
		"bad JSON":           {[]byte(`{not json`), lst()},
	}
	for name, c := range cases {
		if _, code, err := check(c[0], c[1], "t"); err == nil || code != 3 {
			t.Errorf("%s: want refusal (code 3), got code=%d err=%v", name, code, err)
		}
	}
}

func TestReportName(t *testing.T) {
	for in, want := range map[string]string{"x/ocp-prod.quota.json": "ocp-prod", "a.json": "a", "plain": "plain"} {
		if got := reportName(in); got != want {
			t.Errorf("reportName(%q) = %q, want %q", in, got, want)
		}
	}
}
