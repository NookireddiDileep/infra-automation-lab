package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const S = "../samples/"

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGolden(t *testing.T) {
	for _, c := range []struct {
		name string
		code int
	}{{"ocp-prod", 2}, {"ocp-stage", 1}, {"ocp-dev", 0}} {
		got, code, err := check(read(t, S+c.name+".nodes.json"), read(t, S+c.name+".events.json"), c.name)
		if err != nil || code != c.code {
			t.Fatalf("%s: code=%d err=%v, want %d", c.name, code, err, c.code)
		}
		if want := string(read(t, S+"expected/"+c.name+".report")); got != want {
			t.Errorf("%s: output differs\n--- got\n%s--- want\n%s", c.name, got, want)
		}
	}
}

func mk(nodes, events string) ([]byte, []byte) {
	return []byte(`{"kind":"List","items":[` + nodes + `]}`), []byte(`{"kind":"List","items":[` + events + `]}`)
}

const nodeA = `{"kind":"Node","metadata":{"name":"a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`

func ev(count string) string {
	return `{"reason":"Evicted","message":"The node was low on resource: memory. x","source":{"host":"a"}` + count + `}`
}

func TestStormBoundary(t *testing.T) {
	for _, c := range []struct {
		count string
		code  int
		cls   string
	}{{`,"count":4`, 1, "EVICTIONS"}, {`,"count":5`, 2, "EVICTIONSTORM"}, {``, 1, "EVICTIONS"}} {
		n, e := mk(nodeA, ev(c.count))
		out, code, err := check(n, e, "t")
		if err != nil || code != c.code || !strings.Contains(out, " "+c.cls+" ") {
			t.Errorf("count %q: code=%d err=%v out=%s", c.count, code, err, out)
		}
	}
}

func TestRefuseBadInput(t *testing.T) {
	cases := []struct{ name, nodes, events, want string }{
		{"count 0", nodeA, ev(`,"count":0`), "bad count"},
		{"count float", nodeA, ev(`,"count":1.5`), "bad count"},
		{"count string", nodeA, ev(`,"count":"2"`), "bad count"},
		{"count null", nodeA, ev(`,"count":null`), "bad count"},
		{"no host", nodeA, `{"reason":"Evicted"}`, "without source.host"},
		{"bad status", `{"kind":"Node","metadata":{"name":"a"},"status":{"conditions":[{"type":"Ready","status":"Maybe"}]}}`, ``, "bad condition status"},
		{"no name", `{"kind":"Node","metadata":{}}`, ``, "without metadata.name"},
	}
	for _, c := range cases {
		n, e := mk(c.nodes, c.events)
		if _, code, err := check(n, e, "t"); err == nil || code != 3 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: code=%d err=%v, want %q", c.name, code, err, c.want)
		}
	}
	if _, _, err := check([]byte(`{"kind":"Node"}`), []byte(`{"kind":"List","items":[]}`), "t"); err == nil || !strings.Contains(err.Error(), "nodes: not a List") {
		t.Errorf("not a list: %v", err)
	}
	if _, _, err := check([]byte(`{nope`), []byte(`{}`), "t"); err == nil {
		t.Error("invalid JSON must be refused")
	}
}

func TestNonEvictedEventsAreNotValidated(t *testing.T) {
	n, e := mk(nodeA, `{"reason":"BackOff","count":0}`)
	if _, code, err := check(n, e, "t"); err != nil || code != 0 {
		t.Errorf("code=%d err=%v", code, err)
	}
}

func TestMissingReadyAndRun(t *testing.T) {
	n, e := mk(`{"kind":"Node","metadata":{"name":"a"}}`, ``)
	out, _, _ := check(n, e, "t")
	if !strings.Contains(out, "CRIT a NOTREADY status=Missing reason=-") {
		t.Errorf("out=%s", out)
	}
	if run(nil) != 3 || run([]string{filepath.Join(S, "nope.json"), S + "ocp-dev.events.json"}) != 3 {
		t.Error("bad args must exit 3")
	}
	for _, f := range []string{"bad/not-a-list.json", "bad/nodes-bad-status.json"} {
		if run([]string{S + f, S + "ocp-dev.events.json"}) != 3 {
			t.Errorf("%s must exit 3", f)
		}
	}
	if run([]string{S + "ocp-dev.nodes.json", S + "bad/events-bad-count.json"}) != 3 {
		t.Error("bad count must exit 3")
	}
}
